package bridge

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"javabridge/core/internal/blocks"
	"javabridge/core/internal/javapack"

	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Resource packs: the Bedrock server's packs are downloaded (gophertunnel's ResourcePackChunkRequest
// flow, cached on disk by UUID + version), decrypted and converted to one Java pack
// (internal/javapack), which the Java client gets with resource_pack_push from a small HTTP
// server. Custom items get the pack's item models (item_model component, items.go), custom
// blocks spare Java states (the palette), custom entities item_display models
// (entities_custom.go).
//
// Settings (Config.Options, or JB_<KEY> in the environment):
//
//	packs          "on" to convert and offer the server's packs (default off for now)
//	pack_dir       cache directory (default <data dir>/packs: bedrock/ downloads, java/ built packs)
//	pack_cache_mb  cache size limit, oldest files go first (default 512)
//	pack_max_mb    packs bigger than this are not downloaded (default 100: a download must fit
//	               in the dial timeout)
//	pack_listen    HTTP address the Java clients download packs from (default 127.0.0.1:0)
//	pack_url       public base URL of that address (default http://<pack_listen>)

type packConfig struct {
	on        bool
	dir       string
	limit     int64
	listen    string
	url       string
	maxPack   uint64
	whyNot    string
	startOnce sync.Once
	startErr  error
	srv       *http.Server
}

var packCfgMu sync.Mutex
var packCfgCur *packConfig

// packCfg is the pack configuration of the running bridge (made from cfg on first use).
func packCfg() *packConfig {
	packCfgMu.Lock()
	defer packCfgMu.Unlock()
	if packCfgCur != nil {
		return packCfgCur
	}
	dir := option("pack_dir", "")
	if dir == "" && cfg.DataDir != "" {
		dir = filepath.Join(cfg.DataDir, "packs")
	}
	c := &packConfig{dir: dir, limit: 512 << 20,
		listen: option("pack_listen", "127.0.0.1:0"), url: strings.TrimSuffix(option("pack_url", ""), "/")}
	if n, err := strconv.Atoi(option("pack_cache_mb", "")); err == nil && n > 0 {
		c.limit = int64(n) << 20
	}
	c.maxPack = 100 << 20
	if n, err := strconv.Atoi(option("pack_max_mb", "")); err == nil && n > 0 {
		c.maxPack = uint64(n) << 20
	}
	switch {
	case !strings.EqualFold(option("packs", "off"), "on"):
		c.whyNot = "packs are off"
	case c.dir == "":
		c.whyNot = "no data directory"
	default:
		c.on = true
	}
	packCfgCur = c
	return c
}

// resetPackConfig forgets the configuration (a new Run).
func resetPackConfig() {
	packCfgMu.Lock()
	packCfgCur = nil
	packCfgMu.Unlock()
}

// stopPackServer closes the pack HTTP server if it runs.
func stopPackServer() {
	packCfgMu.Lock()
	c := packCfgCur
	packCfgMu.Unlock()
	if c != nil && c.srv != nil {
		_ = c.srv.Close()
	}
}

// packsOn reports whether packs are downloaded and converted (and starts the HTTP server).
func packsOn() bool {
	c := packCfg()
	if !c.on {
		return false
	}
	c.startOnce.Do(func() { c.startErr = startPackServer(c) })
	return c.startErr == nil
}

// packSizes are the sizes ResourcePacksInfo gave, by "uuid_version" (DownloadResourcePack is
// not told them).
var packSizes sync.Map

// packDownload is the Dialer's DownloadResourcePack.
func packDownload(id uuid.UUID, version string, _, _ int) bool {
	if !packsOn() {
		return false
	}
	if v, ok := packSizes.Load(id.String() + "_" + version); ok && v.(uint64) > packCfg().maxPack {
		cfg.Log.Info("resource pack too big; not downloaded", "uuid", id, "version", version, "bytes", v, "max", packCfg().maxPack)
		return false
	}
	return true
}

// packCache is the Dialer's ResourcePackCache (nil without packs).
func packCache() minecraft.ResourcePackCache {
	if !packsOn() {
		return nil
	}
	return serverPackCache(minecraft.DirResourcePackCache{Dir: filepath.Join(packCfg().dir, "bedrock")})
}

// packsInfo is what a connection's ResourcePacksInfo and ResourcePackStack said
// (gophertunnel answers them itself; minecraft.ReceiveHook sees them first).
type packsInfo struct {
	at       time.Time
	required bool
	stack    []string // texture pack UUIDs in stack order (first: on top)
}

var connPacks sync.Map // *minecraft.Conn -> *packsInfo

func init() {
	prev := minecraft.ReceiveHook
	minecraft.ReceiveHook = func(conn *minecraft.Conn, id uint32, payload []byte) {
		if prev != nil {
			prev(conn, id, payload)
		}
		if id != packet.IDResourcePacksInfo && id != packet.IDResourcePackStack {
			return
		}
		func() {
			defer func() { _ = recover() }()
			v, loaded := connPacks.LoadOrStore(conn, &packsInfo{at: time.Now()})
			info := v.(*packsInfo)
			if !loaded {
				// Infos of dials that failed before javaPackFor took them.
				connPacks.Range(func(k, v any) bool {
					if time.Since(v.(*packsInfo).at) > 5*time.Minute {
						connPacks.Delete(k)
					}
					return true
				})
			}
			r := protocol.NewReader(bytes.NewReader(payload), 0, false)
			if id == packet.IDResourcePacksInfo {
				var pk packet.ResourcePacksInfo
				pk.Marshal(r)
				info.required = pk.TexturePackRequired
				for _, tp := range pk.TexturePacks {
					packSizes.Store(tp.UUID.String()+"_"+tp.Version, tp.Size)
				}
				return
			}
			var pk packet.ResourcePackStack
			pk.Marshal(r)
			info.stack = info.stack[:0]
			for _, s := range pk.TexturePacks {
				info.stack = append(info.stack, s.UUID)
			}
		}()
	}
	OnTick(packsTick)
	OnJava(v777.ServerboundPlayResourcePack, func(p *Player, r *wireReader) error {
		id := r.UUID()
		status := r.VarInt()
		if r.Err != nil {
			return r.Err
		}
		p.packStatus(uuid.UUID(id), status)
		return nil
	})
}

// javaPack is a converted pack as one connection uses it.
type javaPack struct {
	*packBuild
	required bool
}

// packBuild is one conversion, shared by the connections whose server sends the same packs and
// definitions.
type packBuild struct {
	res   *javapack.Result // Zip dropped once it is on disk
	sha   string
	id    uuid.UUID
	url   string
	slots []javapack.Slot
	state []uint32 // Java state of each slot
	took  time.Duration
}

var (
	buildMu sync.Mutex
	builds  = map[string]*packBuild{} // conversion key -> build
	// buildLocks serialise conversions of the same key (several players joining at once).
	buildLocks = map[string]*sync.Mutex{}
)

// javaPackFor converts the packs conn downloaded and gives the palette's custom blocks their
// Java states. It runs without p.mu (dialRemote). nil: nothing to send.
func (p *Player) javaPackFor(conn *minecraft.Conn, gd minecraft.GameData, pal *blocks.Palette) *javaPack {
	var info packsInfo
	if v, ok := connPacks.LoadAndDelete(conn); ok {
		info = *v.(*packsInfo)
	}
	if !packsOn() {
		return nil
	}
	packs := conn.ResourcePacks()
	if len(packs) == 0 {
		return nil
	}
	// Later packs override earlier ones in a conversion; the stack lists the top pack first.
	rank := map[string]int{}
	for i, id := range info.stack {
		rank[id] = len(info.stack) - i
	}
	sort.SliceStable(packs, func(i, j int) bool { return rank[packs[i].UUID().String()] < rank[packs[j].UUID().String()] })

	in := &javapack.Input{}
	var names []string
	key := sha1.New()
	fmt.Fprintf(key, "v%d|", javapack.Version)
	for _, pk := range packs {
		fmt.Fprintf(key, "%s_%s_%d|", pk.UUID(), pk.Version(), pk.Len())
		names = append(names, pk.Name())
	}
	for _, it := range gd.Items {
		if it.ComponentBased && !strings.HasPrefix(it.Name, "minecraft:") { // BDS lists vanilla items with components too
			in.Items = append(in.Items, javapack.Item{ID: it.Name, Data: it.Data})
			fmt.Fprintf(key, "i%s%v|", it.Name, it.Data)
		}
	}
	for _, b := range gd.CustomBlocks {
		if strings.HasPrefix(b.Name, "minecraft:") {
			continue // vanilla data-driven blocks (BDS lists them): Java has them
		}
		in.Blocks = append(in.Blocks, javapack.Block{Name: b.Name, Props: b.Properties})
		fmt.Fprintf(key, "b%s%v|", b.Name, b.Properties)
	}
	var state []uint32
	in.Slots, state = freeSlots(pal)
	fmt.Fprintf(key, "s%d", len(in.Slots))
	k := hex.EncodeToString(key.Sum(nil))

	buildMu.Lock()
	lk := buildLocks[k]
	if lk == nil {
		lk = &sync.Mutex{}
		buildLocks[k] = lk
	}
	buildMu.Unlock()
	lk.Lock()
	defer lk.Unlock()
	buildMu.Lock()
	b := builds[k]
	buildMu.Unlock()
	if b == nil {
		start := time.Now()
		for _, pk := range packs {
			src, err := javapack.ReadSource(pk, int64(pk.Len()), pk.UUID().String(), pk.Version(), pk.ContentKey())
			if err != nil {
				p.log.Warn("resource pack unreadable", "pack", pk.Name(), "uuid", pk.UUID(), "err", err)
				continue
			}
			in.Packs = append(in.Packs, src)
		}
		in.Description = "Bedrock: " + strings.Join(names, ", ")
		if len(in.Description) > 200 {
			in.Description = in.Description[:200]
		}
		res, err := javapack.Convert(in)
		if err != nil {
			p.log.Warn("resource pack conversion failed", "err", err)
			return nil
		}
		b = &packBuild{res: res, sha: res.SHA1, id: uuid.NewSHA1(uuid.NameSpaceURL, []byte(ProductName+"-pack:"+res.SHA1)),
			slots: in.Slots, state: state, took: time.Since(start)}
		b.url = packCfg().url + "/" + res.SHA1 + ".zip"
		if !res.Empty() {
			if err := storeJavaPack(res.SHA1, res.Zip); err != nil {
				p.log.Warn("could not store the java pack", "err", err)
				return nil
			}
		}
		st := res.Stats
		p.log.Info("converted resource packs", "packs", strings.Join(names, ", "), "sha1", res.SHA1, "bytes", len(res.Zip),
			"overrides", st.Overrides, "items", st.Items, "armour", st.Equipment, "held3d", st.Held, "blocks", st.Blocks, "entities", st.Entities, "lang", st.LangKeys,
			"took", b.took.Round(time.Millisecond))
		for _, m := range st.Missing {
			p.log.Info("resource pack conversion: not converted", "what", m)
		}
		res.Zip = nil
		addPackLang(res.Lang)
		buildMu.Lock()
		if len(builds) >= 64 {
			for old := range builds { // forget one: connections that use it keep their pointer
				delete(builds, old)
				delete(buildLocks, old)
				break
			}
		}
		builds[k] = b
		buildMu.Unlock()
		go prunePacks(p.log)
	}
	if b.res.Empty() {
		return nil
	}
	for name, slot := range b.res.Blocks {
		pal.SetCustomJava(name, b.state[slot], b.slots[slot].Solid)
	}
	return &javaPack{packBuild: b, required: info.required}
}

// freeSlots are the Java states custom blocks may be drawn on: javapack's candidates the
// palette's block mapping does not use, with their Java state ids.
func freeSlots(pal *blocks.Palette) ([]javapack.Slot, []uint32) {
	used := map[uint32]bool{}
	pal.JavaStates(func(j uint32) { used[j] = true })
	var slots []javapack.Slot
	var state []uint32
	for _, s := range javapack.Candidates() {
		jb := blocks.JavaBlockNamed(s.Block)
		if jb == nil {
			continue
		}
		st, ok := jb.Default, true
		for k, v := range s.State {
			if st, ok = jb.With(st, k, v); !ok {
				break
			}
		}
		if !ok || used[st] {
			continue
		}
		slots = append(slots, s)
		state = append(state, st)
	}
	return slots, state
}

// packLang is the bridge's lang with the keys of every converted pack that vanilla does not
// have (copy on write: translate reads it without a lock).
var (
	packLang   atomic.Pointer[map[string]string]
	packLangMu sync.Mutex
)

// currentLang is the lang translate uses.
func currentLang() map[string]string {
	if m := packLang.Load(); m != nil {
		return *m
	}
	return lang
}

func addPackLang(keys map[string]string) {
	langOnce.Do(loadLang)
	packLangMu.Lock()
	defer packLangMu.Unlock()
	base := currentLang()
	var m map[string]string
	for k, v := range keys {
		if _, ok := base[k]; ok {
			continue
		}
		if m == nil {
			m = maps.Clone(base)
		}
		m[k] = v
	}
	if m != nil {
		packLang.Store(&m)
	}
}

// packState is the Java client's pack (p.mu).
type packState struct {
	sent   *javaPack // pushed last (nil: none)
	status int32     // the client's last answer for it (-1: none yet)
	failed bool      // declined or failed: item models are not used
	// ents are the custom entities shown with a model, by runtime id (entities_custom.go).
	ents map[uint64]*modelEntity
}

// Java resource_pack statuses.
const (
	packLoaded       = 0
	packDeclined     = 1
	packFailedDL     = 2
	packAccepted     = 3
	packDownloaded   = 4
	packInvalidURL   = 5
	packFailedReload = 6
	packDiscarded    = 7
)

// packsTick pushes the current server's pack when it differs from the client's: a new server
// pops the old pack first. In the waiting area the client keeps what it has.
func packsTick(p *Player) {
	if p.rem == nil {
		return
	}
	want := p.rem.jpack
	cur := p.packs.sent
	if want == cur || (want != nil && cur != nil && want.sha == cur.sha) {
		p.packs.sent = want
		return
	}
	if cur != nil {
		w := p.pk()
		w.Bool(true)
		w.UUID(cur.id)
		p.send(v777.ClientboundPlayResourcePackPop, w)
	}
	p.packs.sent, p.packs.status, p.packs.failed = want, -1, false
	if want == nil {
		return
	}
	touchJavaPack(want.sha)
	w := p.pk()
	w.UUID(want.id)
	w.String(want.url)
	w.String(want.sha)
	w.Bool(want.required)
	w.Bool(true)
	if want.required {
		writeText(w, "§e"+targetName(p.rem.target)+"§r needs its resource pack (custom items, blocks and mobs).")
	} else {
		writeText(w, "§e"+targetName(p.rem.target)+"§r has a resource pack with custom items, blocks and mobs.")
	}
	p.send(v777.ClientboundPlayResourcePackPush, w)
	p.log.Info("sent resource pack", "url", want.url, "required", want.required)
}

// packStatus handles the client's resource_pack answer. p.mu is held.
func (p *Player) packStatus(id uuid.UUID, status int32) {
	cur := p.packs.sent
	if cur == nil || cur.id != id {
		return
	}
	p.packs.status = status
	switch status {
	case packLoaded:
		p.log.Info("java client loaded the resource pack")
	case packDeclined:
		p.packs.failed = true
		p.log.Info("java client declined the resource pack", "required", cur.required)
	case packFailedDL, packInvalidURL, packFailedReload, packDiscarded:
		p.packs.failed = true
		p.log.Warn("java client could not use the resource pack", "status", status, "url", cur.url)
		p.BridgeMessage("§cYour client could not load the server's resource pack: custom items, blocks and mobs look plain.")
	}
}

// packFor is the pack the player's current server's items and entities are drawn with (nil:
// none, or the client refused it). p.mu is held.
func (p *Player) packFor() *javaPack {
	if p.rem == nil || p.rem.jpack == nil || (p.packs.sent == p.rem.jpack && p.packs.failed) {
		return nil
	}
	return p.rem.jpack
}

// packItem gives a custom item (items.go: shown as paper) the name from the server lang and
// the converted pack model. p.mu is held.
func (p *Player) packItem(name string, js *jitem.Stack) {
	if p.rem == nil || p.rem.jpack == nil {
		return
	}
	res := p.rem.jpack.res
	if n, ok := res.ItemNames[name]; ok {
		js.ItemName = itemText(n, true)
	}
	jp := p.packFor()
	if jp != nil {
		if m, ok := res.ItemModels[name]; ok {
			js.ItemModel = m
			js.Add(jitem.CompItemModel)
		}
	}
	if eq, ok := res.ItemEquip[name]; ok {
		// Worn add-on armour: the slot it goes in (Java's armour slots take it) and the equipment
		// asset that draws it on the body instead of the item on the head.
		e := &jitem.Equippable{Slot: javaEquipSlot[eq.Slot]}
		if jp != nil {
			e.Model = eq.Asset
		}
		js.Equippable = e
		js.Add(jitem.CompEquippable)
	}
}

// javaEquipSlot maps javapack's armour slots to Java's equipment slots.
var javaEquipSlot = map[string]int32{
	"head": jitem.EquipSlotHead, "chest": jitem.EquipSlotChest, "legs": jitem.EquipSlotLegs, "feet": jitem.EquipSlotFeet,
}

// The pack HTTP server serves /<sha1>.zip from <pack_dir>/java and nothing else.

var packFile = regexp.MustCompile(`^/([0-9a-f]{40})(\.zip)?$`)

func startPackServer(c *packConfig) error {
	if err := os.MkdirAll(filepath.Join(c.dir, "java"), 0o755); err != nil {
		cfg.Log.Warn("resource packs off: cache directory", "err", err)
		return err
	}
	l, err := net.Listen("tcp", c.listen)
	if err != nil {
		cfg.Log.Warn("resource packs off: pack server", "addr", c.listen, "err", err)
		return err
	}
	if c.url == "" {
		host, port, _ := net.SplitHostPort(l.Addr().String())
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			cfg.Log.Warn("pack_listen is on every interface: set pack_url to the address clients reach it at")
			host = "127.0.0.1"
		}
		c.url = "http://" + net.JoinHostPort(host, port)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(servePack),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      5 * time.Minute,
	}
	c.srv = srv
	goSafe("pack server", func() { _ = srv.Serve(l) }, nil)
	cfg.Log.Info("resource pack server", "listen", l.Addr().String(), "url", c.url, "dir", c.dir)
	prunePacks(cfg.Log)
	return nil
}

func servePack(w http.ResponseWriter, r *http.Request) {
	m := packFile.FindStringSubmatch(r.URL.Path)
	if m == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(packCfg().dir, "java", m[1]+".zip"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	http.ServeContent(w, r, m[1]+".zip", st.ModTime(), f)
}

func javaPackPath(sha string) string { return filepath.Join(packCfg().dir, "java", sha+".zip") }

func storeJavaPack(sha string, zip []byte) error {
	dst := javaPackPath(sha)
	if _, err := os.Stat(dst); err == nil {
		touchJavaPack(sha)
		return nil
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, zip, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// touchJavaPack marks a pack as used now (pruning removes the oldest files first).
func touchJavaPack(sha string) {
	now := time.Now()
	_ = os.Chtimes(javaPackPath(sha), now, now)
}

var pruneMu sync.Mutex

// prunePacks deletes the oldest cached files while the cache is over its limit.
func prunePacks(log *slog.Logger) {
	pruneMu.Lock()
	defer pruneMu.Unlock()
	c := packCfg()
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	_ = filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, file{path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= c.limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= c.limit {
			break
		}
		if time.Since(f.mod) < 10*time.Minute {
			continue // in use by a login now
		}
		if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		total -= f.size
		log.Info("pruned resource pack cache", "file", f.path, "bytes", f.size)
	}
}
