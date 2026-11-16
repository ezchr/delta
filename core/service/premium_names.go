package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// premiumNames answers, for account_mode = "hybrid", whether a Java name belongs to a premium
// (paid Mojang/Microsoft) account. Such a name must log in the online way (encryption + the
// session server's hasJoined), so its owner keeps the identity he has in online mode and nobody
// else can take it; every other name is let in offline, as a cracked player.
//
// Answers are cached (premiumTTL / crackedTTL) in memory and in a file, so a restart or an API
// outage does not turn known premium names into free ones. When no API answers and nothing is
// cached, the name is treated as premium (fail closed): a premium player can still log in, a
// cracked player with that name is refused until the API answers again. Each case is logged.
type premiumNames struct {
	urls []string // lookup endpoints; the escaped name is appended
	http *http.Client
	log  *slog.Logger
	file string // cache file, "" = memory only
	now  func() time.Time

	premiumTTL, crackedTTL time.Duration

	sem     chan struct{} // lookups in flight at once
	perMin  int           // lookups per minute, over it = treated as a failed lookup
	limMu   sync.Mutex
	limFrom time.Time
	limN    int

	mu    sync.Mutex
	cache map[string]premiumEntry
}

type premiumEntry struct {
	Premium bool      `json:"premium"`
	Checked time.Time `json:"checked"`
}

// defaultLookupURLs are tried in order. The Minecraft services endpoint is the current one; the
// old api.mojang.com one is the fallback (it is rate limited per address, shared with other
// software on the same machine).
var defaultLookupURLs = []string{
	"https://api.minecraftservices.com/minecraft/profile/lookup/name/",
	"https://api.mojang.com/users/profiles/minecraft/",
}

func newPremiumNames(log *slog.Logger, file string, urls []string) *premiumNames {
	if len(urls) == 0 {
		urls = defaultLookupURLs
	}
	p := &premiumNames{
		urls:       urls,
		http:       &http.Client{Timeout: 5 * time.Second},
		log:        log,
		file:       file,
		now:        time.Now,
		premiumTTL: 24 * time.Hour,
		crackedTTL: time.Hour,
		sem:        make(chan struct{}, 4),
		perMin:     120,
		cache:      map[string]premiumEntry{},
	}
	p.load()
	return p
}

// errLookupLimit is a lookup refused by our own limit (premiumNames.perMin).
var errLookupLimit = errors.New("premium lookup limit reached")

// RequiresLogin reports whether name must log in with Mojang (the listener's OnlineModeFor).
func (p *premiumNames) RequiresLogin(name string) bool {
	key := strings.ToLower(name)
	p.mu.Lock()
	e, cached := p.cache[key]
	p.mu.Unlock()
	if cached && p.fresh(e) {
		p.logDecision(name, e.Premium, "cache")
		return e.Premium
	}
	premium, err := p.lookup(name)
	if err == nil {
		p.put(key, premium)
		p.logDecision(name, premium, "lookup")
		return premium
	}
	if cached {
		p.log.Warn("hybrid login: premium lookup failed, using the cached answer",
			"name", name, "premium", e.Premium, "checked", e.Checked.UTC().Format(time.RFC3339), "err", err)
		return e.Premium
	}
	p.log.Warn("hybrid login: premium lookup failed and the name is not cached - treating it as premium "+
		"(Mojang login required; a cracked player with this name is refused until the lookup works)",
		"name", name, "err", err)
	return true
}

func (p *premiumNames) logDecision(name string, premium bool, source string) {
	if premium {
		p.log.Info("hybrid login: premium name, Mojang login required", "name", name, "source", source)
	} else {
		p.log.Info("hybrid login: not a premium name, admitted offline (cracked)", "name", name, "source", source)
	}
}

func (p *premiumNames) fresh(e premiumEntry) bool {
	ttl := p.crackedTTL
	if e.Premium {
		ttl = p.premiumTTL
	}
	return p.now().Sub(e.Checked) < ttl
}

// lookup asks the endpoints in order; the first definite answer wins.
func (p *premiumNames) lookup(name string) (bool, error) {
	if !p.allow() {
		return false, errLookupLimit
	}
	p.sem <- struct{}{}
	defer func() { <-p.sem }()
	var errs []error
	for _, base := range p.urls {
		premium, err := p.ask(base, name)
		if err == nil {
			return premium, nil
		}
		errs = append(errs, err)
	}
	return false, errors.Join(errs...)
}

// ask is one endpoint: 200 with a profile id = premium, 204/404 = no such account, anything
// else (429 rate limit, 5xx, network, odd body) = an error.
func (p *premiumNames) ask(base, name string) (bool, error) {
	resp, err := p.http.Get(base + url.PathEscape(name))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var prof struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<14)).Decode(&prof); err != nil {
			return false, fmt.Errorf("%s: %w", base, err)
		}
		if len(strings.ReplaceAll(prof.ID, "-", "")) != 32 || !strings.EqualFold(prof.Name, name) {
			return false, fmt.Errorf("%s: unexpected profile %q/%q", base, prof.ID, prof.Name)
		}
		return true, nil
	case http.StatusNoContent, http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("%s: HTTP %d", base, resp.StatusCode)
	}
}

// allow counts a lookup against perMin (a fixed one-minute window).
func (p *premiumNames) allow() bool {
	if p.perMin <= 0 {
		return true
	}
	p.limMu.Lock()
	defer p.limMu.Unlock()
	now := p.now()
	if now.Sub(p.limFrom) >= time.Minute {
		p.limFrom, p.limN = now, 0
	}
	if p.limN >= p.perMin {
		return false
	}
	p.limN++
	return true
}

func (p *premiumNames) put(key string, premium bool) {
	p.mu.Lock()
	p.cache[key] = premiumEntry{Premium: premium, Checked: p.now()}
	data, err := json.MarshalIndent(p.cache, "", " ")
	p.mu.Unlock()
	if err != nil || p.file == "" {
		return
	}
	tmp := p.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		p.log.Warn("hybrid login: could not write the premium cache", "file", p.file, "err", err)
		return
	}
	if err := os.Rename(tmp, p.file); err != nil {
		p.log.Warn("hybrid login: could not write the premium cache", "file", p.file, "err", err)
	}
}

// load reads the cache file. Entries older than 30 days are dropped; newer stale ones stay as the
// fallback answer for when the API cannot be reached.
func (p *premiumNames) load() {
	if p.file == "" {
		return
	}
	data, err := os.ReadFile(p.file)
	if err != nil {
		return
	}
	var m map[string]premiumEntry
	if err := json.Unmarshal(data, &m); err != nil {
		p.log.Warn("hybrid login: ignoring an unreadable premium cache", "file", p.file, "err", err)
		return
	}
	for k, e := range m {
		if p.now().Sub(e.Checked) < 30*24*time.Hour {
			p.cache[strings.ToLower(k)] = e
		}
	}
}

// premiumCacheFile is where the cache lives in the plugin's data folder.
func premiumCacheFile(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "premium_cache.json")
}

// hybridRefusal is the disconnect text for a premium name whose Mojang login failed.
func hybridRefusal(name string) string {
	return name + " is a premium Minecraft account. Log in with that account to use this name; " +
		"cracked players must choose another name."
}
