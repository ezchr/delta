package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/resource"
)

// localPackCache is the Dialer's pack cache when the bridge runs next to the server (inside BDS
// as a plugin): the server's resource packs are read from its own folders instead of being
// downloaded over the connection, which for a server with large packs takes longer than a
// login may. Packs it does not have on disk go to the download cache (dir).
type localPackCache struct {
	dir minecraft.DirResourcePackCache

	root string

	once  sync.Once
	paths map[string]string // "uuid_version" -> folder or archive

	mu    sync.Mutex
	packs map[string]*resource.Pack // read so far
}

var (
	localCacheOnce sync.Once
	localCache     *localPackCache
)

// serverPackCache returns the local cache for the server root (option bds_dir, default the
// working directory, which is the BDS root for the plugin), or nil when it has no packs folder.
func serverPackCache(dir minecraft.DirResourcePackCache) minecraft.ResourcePackCache {
	localCacheOnce.Do(func() {
		root := option("bds_dir", "")
		if root == "" {
			root, _ = os.Getwd()
		}
		if strings.EqualFold(option("local_packs", "on"), "off") || root == "" {
			return
		}
		if _, err := os.Stat(filepath.Join(root, "resource_packs")); err != nil {
			return
		}
		localCache = &localPackCache{dir: dir, packs: map[string]*resource.Pack{}, paths: map[string]string{}}
		localCache.root = root
	})
	if localCache == nil {
		return dir
	}
	return localCache
}

// serverHasPack reports whether the pack is in the server's own folders.
func serverHasPack(id uuid.UUID, version string) bool {
	c, ok := packCache().(*localPackCache)
	if !ok {
		return false
	}
	c.index()
	_, ok = c.paths[strings.ToLower(id.String())+"_"+version]
	return ok
}

// IgnoreSize tells the connection a pack from this cache may differ in size from the one the
// server advertises: the server zips its folder itself, so only uuid and version are compared.
func (c *localPackCache) IgnoreSize() bool { return true }

func (c *localPackCache) index() {
	c.once.Do(func() {
		dirs := []string{filepath.Join(c.root, "resource_packs"), filepath.Join(c.root, "development_resource_packs")}
		if worlds, err := os.ReadDir(filepath.Join(c.root, "worlds")); err == nil {
			for _, w := range worlds {
				dirs = append(dirs, filepath.Join(c.root, "worlds", w.Name(), "resource_packs"))
			}
		}
		for _, d := range dirs {
			ents, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range ents {
				p := filepath.Join(d, e.Name())
				if e.IsDir() {
					if id, ver, ok := manifestID(filepath.Join(p, "manifest.json")); ok {
						c.paths[id+"_"+ver] = p
					}
					continue
				}
				switch strings.ToLower(filepath.Ext(e.Name())) {
				case ".zip", ".mcpack":
					if pk, err := resource.ReadPath(p); err == nil {
						k := pk.UUID().String() + "_" + pk.Version()
						c.paths[k] = p
						c.packs[k] = pk
					}
				}
			}
		}
		if cfg.Log != nil {
			cfg.Log.Info("resource packs read from the server folder", "root", c.root, "packs", len(c.paths))
		}
	})
}

// manifestID reads a pack manifest's header uuid and version ("1.2.3").
func manifestID(path string) (string, string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	var m struct {
		Header struct {
			UUID    string `json:"uuid"`
			Version []int  `json:"version"`
		} `json:"header"`
	}
	if json.Unmarshal(stripJSONComments(b), &m) != nil || m.Header.UUID == "" || len(m.Header.Version) != 3 {
		return "", "", false
	}
	v := m.Header.Version
	return strings.ToLower(m.Header.UUID), itoa(v[0]) + "." + itoa(v[1]) + "." + itoa(v[2]), true
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// stripJSONComments drops // line comments outside strings (pack manifests often have them).
func stripJSONComments(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		ch := b[i]
		if inStr {
			out = append(out, ch)
			switch {
			case esc:
				esc = false
			case ch == 0x5c:
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
		} else if ch == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			continue
		}
		out = append(out, ch)
	}
	if len(out) >= 3 && out[0] == 0xEF && out[1] == 0xBB && out[2] == 0xBF {
		out = out[3:]
	}
	return out
}

// prewarmPacks reads the world add-on packs (worlds/*/resource_packs) in the background when the
// bridge starts. A pack kept as a folder is zipped in memory on first read, which for a big add-on
// (Cobblemon's resource pack is ~280 MB) takes tens of seconds; done inside the first login, it
// pushed that login past its time limit. The server's own vanilla packs are never sent to a
// client, so they are left alone.
func prewarmPacks() {
	c, ok := packCache().(*localPackCache)
	if !ok {
		return
	}
	go func() {
		c.index()
		worlds := filepath.Join(c.root, "worlds") + string(filepath.Separator)
		start, n := time.Now(), 0
		for k, p := range c.paths {
			if !strings.HasPrefix(p, worlds) {
				continue
			}
			id, ver, _ := strings.Cut(k, "_")
			u, err := uuid.Parse(id)
			if err != nil {
				continue
			}
			if _, err := c.Load(context.Background(), minecraft.ResourcePackCacheKey{UUID: u, Version: ver}); err == nil {
				n++
			}
		}
		if cfg.Log != nil && n > 0 {
			cfg.Log.Info("world resource packs ready for Java players", "packs", n, "took", time.Since(start).Round(time.Millisecond))
		}
	}()
}

func (c *localPackCache) Load(ctx context.Context, key minecraft.ResourcePackCacheKey) (*resource.Pack, error) {
	c.index()
	k := strings.ToLower(key.UUID.String()) + "_" + key.Version
	c.mu.Lock()
	defer c.mu.Unlock()
	if pk, ok := c.packs[k]; ok {
		return pk, nil
	}
	if p, ok := c.paths[k]; ok {
		pk, err := resource.ReadPath(p)
		if err != nil {
			if st, serr := os.Stat(p); serr == nil && st.IsDir() {
				pk, err = readPackLenient(p)
			}
		}
		if err == nil {
			c.packs[k] = pk
			return pk, nil
		}
		if cfg.Log != nil {
			cfg.Log.Warn("server resource pack unreadable; downloading it", "path", p, "err", err)
		}
	}
	return c.dir.Load(ctx, key)
}

func (c *localPackCache) Store(ctx context.Context, key minecraft.ResourcePackCacheKey, pack *resource.Pack) error {
	return c.dir.Store(ctx, key, pack)
}
