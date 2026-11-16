package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"javabridge/core/bridge"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/google/uuid"
)

// Tests of account_mode = "hybrid" with a stubbed Mojang profile lookup and session server:
// premium names log in the online way and keep their online identity, other names are let in
// offline, and a failed lookup is treated conservatively.

var (
	ezchrUUID = uuid.MustParse("e8f07710-4e3e-4cb1-a0b3-2167ede484e6") // the owner's premium account
	quietLog  = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// stubLookup serves a Mojang-like profile lookup: the names in premium answer 200, "Flaky" 500,
// "Limited" 429, everything else 404. hits counts the requests.
func stubLookup(premium map[string]uuid.UUID, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch name {
		case "Flaky":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "Limited":
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		for n, id := range premium {
			if strings.EqualFold(n, name) {
				_ = json.NewEncoder(w).Encode(map[string]string{"id": hexNoDash(id), "name": n})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func testNames(t *testing.T, lookupURL string) *premiumNames {
	return newPremiumNames(quietLog, filepath.Join(t.TempDir(), "premium_cache.json"), []string{lookupURL + "/lookup/"})
}

// startHybridListener starts a listener configured the way startBridge does for hybrid.
func startHybridListener(t *testing.T, sessionURL string, names *premiumNames) (string, func()) {
	cfg := jserver.Config{
		CompressionThreshold: 256,
		SessionServer:        sessionURL,
		Log:                  quietLog,
		Status:               func() jserver.Status { return jserver.Status{MOTD: "test", MaxPlayers: 5} },
	}
	applyJavaAuth(&cfg, accountSettings{Mode: "hybrid", JavaAuth: javaAuthPremium, Hook: true}, names)
	l, err := jserver.Listen("127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			if _, err := l.Accept(); err != nil {
				return
			}
		}
	}()
	return l.Addr().String(), func() { _ = l.Close() }
}

// offlineResult is what a cracked client (one that cannot answer an encryption request) sees.
type offlineResult struct {
	admitted     bool      // login_finished without encryption
	authRequired bool      // the server sent an encryption request (Mojang login)
	uuid         uuid.UUID // the profile UUID in login_finished
	reason       string    // login_disconnect text
}

// doOfflineLogin logs in like a cracked client: handshake and hello, no encryption.
func doOfflineLogin(t *testing.T, addr, name string) offlineResult {
	nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
	c := wire.NewConn(nc)
	var hs wire.Writer
	hs.VarInt(testProtocol)
	hs.String("127.0.0.1")
	hs.B = append(hs.B, 0x63, 0xdd)
	hs.VarInt(2)
	if err := c.Send(v777.ServerboundHandshakeIntention, hs.B); err != nil {
		t.Fatalf("send handshake: %v", err)
	}
	var hello wire.Writer
	hello.String(name)
	hello.UUID(jserver.OfflineUUID(name))
	if err := c.Send(v777.ServerboundLoginHello, hello.B); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	for {
		id, body, err := c.ReadPacket()
		if err != nil {
			t.Fatalf("read after hello: %v", err)
		}
		switch id {
		case v777.ClientboundLoginHello:
			return offlineResult{authRequired: true}
		case v777.ClientboundLoginLoginCompression:
			c.SetThreshold(int(wire.NewReader(body).VarInt()))
		case v777.ClientboundLoginLoginFinished:
			u := wire.NewReader(body).UUID()
			_ = c.Send(v777.ServerboundLoginLoginAcknowledged, nil)
			return offlineResult{admitted: true, uuid: uuid.UUID(u)}
		case v777.ClientboundLoginLoginDisconnect:
			return offlineResult{reason: readDisconnect(body)}
		default:
			t.Fatalf("unexpected packet after hello: %#x", id)
		}
	}
}

// xuidFor is the derived XUID the bridge gives a Java UUID (what the hook stages on BDS).
func xuidFor(t *testing.T, u uuid.UUID) string {
	bl, err := bridge.OnlineIdentity{Secret: "s"}.BedrockLogin(context.Background(), bridge.JavaPlayer{Name: "x", UUID: u})
	if err != nil {
		t.Fatal(err)
	}
	return bl.IdentityData.XUID
}

func TestHybridPremiumNameWithValidSessionKeepsPremiumIdentity(t *testing.T) {
	var hits atomic.Int32
	lk := stubLookup(map[string]uuid.UUID{"Ezchr": ezchrUUID}, &hits)
	defer lk.Close()
	ss := stubSession(t, ezchrUUID, "Ezchr", http.StatusOK)
	defer ss.Close()
	addr, stop := startHybridListener(t, ss.URL, testNames(t, lk.URL))
	defer stop()

	res := doOnlineLogin(t, addr, "Ezchr")
	if !res.accepted {
		t.Fatalf("premium login with a valid session was refused: %q", res.reason)
	}
	if res.profUUID != ezchrUUID || res.profName != "Ezchr" {
		t.Fatalf("got %s/%q, want the verified premium profile %s/Ezchr", res.profUUID, res.profName, ezchrUUID)
	}
	// Same XUID as in online mode (the owner's 2535483566072468), not the offline-UUID one.
	if got := xuidFor(t, res.profUUID); got != "2535483566072468" {
		t.Fatalf("premium XUID %s, want 2535483566072468", got)
	}
	if off := xuidFor(t, uuid.UUID(jserver.OfflineUUID("Ezchr"))); off != "2535905407707780" {
		t.Fatalf("offline-UUID XUID %s, want 2535905407707780 (the identity hybrid must avoid)", off)
	}
}

func TestHybridPremiumNameWithoutAuthIsRefused(t *testing.T) {
	var hits atomic.Int32
	lk := stubLookup(map[string]uuid.UUID{"Ezchr": ezchrUUID}, &hits)
	defer lk.Close()
	ss := stubSession(t, uuid.Nil, "", http.StatusForbidden) // nobody logged in with Mojang
	defer ss.Close()
	addr, stop := startHybridListener(t, ss.URL, testNames(t, lk.URL))
	defer stop()

	// A cracked client is never admitted offline under a premium name (any case): it is asked
	// for the Mojang login it cannot do.
	for _, n := range []string{"Ezchr", "ezchr"} {
		if r := doOfflineLogin(t, addr, n); r.admitted || !r.authRequired {
			t.Fatalf("cracked client named %q: %+v, want a Mojang login request", n, r)
		}
	}
	// A client that goes through the encryption but has no Mojang session is refused, with the
	// hybrid explanation.
	res := doOnlineLogin(t, addr, "Ezchr")
	if res.accepted {
		t.Fatal("premium name without a Mojang session was ACCEPTED")
	}
	if !strings.Contains(res.reason, "premium Minecraft account") {
		t.Fatalf("refusal text %q does not explain the premium name", res.reason)
	}
}

func TestHybridCrackedNameIsAdmittedOffline(t *testing.T) {
	var hits atomic.Int32
	lk := stubLookup(map[string]uuid.UUID{"Ezchr": ezchrUUID}, &hits)
	defer lk.Close()
	ss := stubSession(t, uuid.Nil, "", http.StatusForbidden)
	defer ss.Close()
	addr, stop := startHybridListener(t, ss.URL, testNames(t, lk.URL))
	defer stop()

	r := doOfflineLogin(t, addr, "S2JavaTest")
	if !r.admitted {
		t.Fatalf("cracked name not admitted: %+v", r)
	}
	if want := uuid.UUID(jserver.OfflineUUID("S2JavaTest")); r.uuid != want {
		t.Fatalf("cracked player got UUID %s, want the offline UUID %s", r.uuid, want)
	}
	// A second login uses the cache.
	doOfflineLogin(t, addr, "S2JavaTest")
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d lookups for two logins, want 1 (cached)", n)
	}
}

func TestHybridLookupFailure(t *testing.T) {
	var hits atomic.Int32
	lk := stubLookup(nil, &hits)
	defer lk.Close()

	// No answer and nothing cached: fail closed (Mojang login required).
	names := testNames(t, lk.URL)
	for _, n := range []string{"Flaky", "Limited"} {
		if !names.RequiresLogin(n) {
			t.Fatalf("%s: a failed lookup with no cache let the name in offline", n)
		}
	}
	// ... and not cached as an answer: the next login asks again.
	before := hits.Load()
	names.RequiresLogin("Flaky")
	if hits.Load() == before {
		t.Fatal("a failed lookup was cached")
	}

	// A stale cached answer is used while the API fails.
	names.mu.Lock()
	names.cache["flaky"] = premiumEntry{Premium: false, Checked: time.Now().Add(-48 * time.Hour)}
	names.mu.Unlock()
	if names.RequiresLogin("Flaky") {
		t.Fatal("stale cached cracked answer not used when the lookup failed")
	}

	// The second endpoint is asked when the first fails.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer broken.Close()
	two := newPremiumNames(quietLog, "", []string{broken.URL + "/a/", lk.URL + "/b/"})
	if two.RequiresLogin("SomeCrackedName") {
		t.Fatal("fallback endpoint's 404 not used")
	}

	// Our own lookup limit counts as a failure (fail closed).
	lim := newPremiumNames(quietLog, "", []string{lk.URL + "/c/"})
	lim.perMin = 1
	if lim.RequiresLogin("FirstName") {
		t.Fatal("first lookup should answer cracked")
	}
	if !lim.RequiresLogin("SecondName") {
		t.Fatal("a lookup over the limit let the name in offline")
	}

	// Through the listener: a cracked client whose name cannot be checked is asked to log in.
	ss := stubSession(t, uuid.Nil, "", http.StatusForbidden)
	defer ss.Close()
	addr, stop := startHybridListener(t, ss.URL, testNames(t, lk.URL))
	defer stop()
	if r := doOfflineLogin(t, addr, "Flaky"); r.admitted || !r.authRequired {
		t.Fatalf("unknown name during an API failure: %+v, want a Mojang login request", r)
	}
}

func TestPremiumNamesCacheTTLAndFile(t *testing.T) {
	var hits atomic.Int32
	lk := stubLookup(map[string]uuid.UUID{"Ezchr": ezchrUUID}, &hits)
	defer lk.Close()
	file := filepath.Join(t.TempDir(), "premium_cache.json")
	now := time.Now()
	a := newPremiumNames(quietLog, file, []string{lk.URL + "/x/"})
	a.now = func() time.Time { return now }
	if !a.RequiresLogin("Ezchr") || a.RequiresLogin("Cracky") {
		t.Fatal("wrong answers")
	}
	// A restart reads the file: no new lookups within the TTLs.
	b := newPremiumNames(quietLog, file, []string{lk.URL + "/x/"})
	b.now = func() time.Time { return now.Add(30 * time.Minute) }
	if !b.RequiresLogin("EZCHR") || b.RequiresLogin("cracky") {
		t.Fatal("wrong answers from the cache file")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("%d lookups, want 2", n)
	}
	// After the cracked TTL (1 h) the cracked name is checked again, the premium one is not.
	b.now = func() time.Time { return now.Add(2 * time.Hour) }
	b.RequiresLogin("Ezchr")
	b.RequiresLogin("Cracky")
	if n := hits.Load(); n != 3 {
		t.Fatalf("%d lookups, want 3", n)
	}
}

func TestResolveAccountMode(t *testing.T) {
	cases := []struct {
		opts map[string]string
		want accountSettings
	}{
		{nil, accountSettings{Mode: "online", JavaAuth: javaAuthAll, Hook: true}},
		{map[string]string{"account_mode": "hybrid"}, accountSettings{Mode: "hybrid", JavaAuth: javaAuthPremium, Hook: true}},
		{map[string]string{"account_mode": "offline"}, accountSettings{Mode: "offline", JavaAuth: javaAuthNone}},
		{map[string]string{"online_mode": "false"}, accountSettings{Mode: "offline", JavaAuth: javaAuthNone}},
		// java_online = "false" no longer drops the check for premium names: it is hybrid.
		{map[string]string{"java_online": "false", "identity_hook": "true"}, accountSettings{Mode: "online", JavaAuth: javaAuthPremium, Hook: true}},
		{map[string]string{"java_online": "unverified"}, accountSettings{Mode: "online", JavaAuth: javaAuthNone, Hook: true}},
		{map[string]string{"account_mode": "hybrid", "java_online": "true"}, accountSettings{Mode: "hybrid", JavaAuth: javaAuthAll, Hook: true}},
		{map[string]string{"account_mode": "weird"}, accountSettings{Mode: "online", JavaAuth: javaAuthAll, Hook: true, Unknown: "weird"}},
	}
	for _, c := range cases {
		got := resolveAccountMode(func(k string) string { return c.opts[k] })
		if got != c.want {
			t.Errorf("%v: got %+v, want %+v", c.opts, got, c.want)
		}
	}
	// Velocity forwarding: the proxy authenticates, the listener does not.
	cfg := jserver.Config{VelocitySecret: []byte("s")}
	applyJavaAuth(&cfg, accountSettings{JavaAuth: javaAuthPremium}, nil)
	if cfg.OnlineMode || cfg.OnlineModeFor != nil {
		t.Fatal("velocity forwarding must not add Mojang checks")
	}
}
