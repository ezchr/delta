// Package packconv builds a Java Edition resource pack from Bedrock resource packs (and the
// custom item/block definitions of behaviour packs or of the server's runtime registries), plus
// the tables a Bedrock->Java translator needs to use it: which vanilla Java item each custom
// item is drawn on and with which item model, which spare Java block state each custom block is
// drawn as, how custom entities are drawn with item displays, and the custom sounds and names.
//
// It converts vanilla texture overrides, custom item icons and 3D attachables (item models with
// the item_model component), armour (equipment assets), custom blocks (models on spare Java
// block states), custom entities (item models built from their geometry: the whole model and
// one per bone), sounds and languages. It knows nothing about connections: the caller gives it
// the packs and definitions and gets a zip, the tables and a report back.
package packconv

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Version changes whenever the converter's output for the same input changes (callers use it in
// their conversion cache key; mapping.json carries it).
const Version = 2

// Java resource pack format range written to pack.mcmeta by default: Java 26.3's
// (version.json pack_version.resource_major/minor); 26.2 clients (88) are in the range too.
var (
	DefaultMinFormat = 88
	DefaultMaxFormat = []int{97, 1}
)

// Namespace is the default Java namespace of the converted assets.
const Namespace = "javabridge"

// Input is what a conversion needs.
type Input struct {
	// Packs are the resource packs in stack order, the bottom first: later packs win.
	Packs []*Source
	// BehaviorPacks are behaviour packs whose items/, blocks/ and entities/ definitions are
	// used for custom content the caller does not give in Items/Blocks (optional).
	BehaviorPacks []*Source
	// Items are the server's custom items as the translator sees them at runtime: an
	// ItemRegistry entry's identifier and its component data ({"components": ...}). Data may be
	// nil for an item known by identifier only. Behaviour pack items are added to these.
	Items []Item
	// Blocks are the server's custom blocks (StartGame's block list); behaviour pack blocks are
	// added to these.
	Blocks []Block
	// Slots are the free Java block states custom blocks may be drawn on, in order of
	// preference: Candidates() without the states the caller's own block mapping uses (a
	// standalone conversion passes all of Candidates()). None: custom blocks get no state.
	Slots []Slot
	// Description is the pack's description line.
	Description string
	// Namespace is the Java namespace of the converted assets ("" Namespace).
	Namespace string
	// MinFormat and MaxFormat are pack.mcmeta's min_format/max_format (0/nil: the defaults).
	MinFormat int
	MaxFormat []int
}

// Item is a custom item definition.
type Item struct {
	ID   string
	Data map[string]any // the ItemRegistry entry's data ({"components": ...}); nil: unknown
}

// Block is a custom block definition.
type Block struct {
	Name  string
	Props map[string]any // the BlockEntry's properties ({"components": ..., "permutations": ...})
}

// Entity is how a custom entity is drawn: the whole-model item model an item_display shows and
// the display transformation that puts it where the Bedrock model is, plus the per-bone parts
// (see EntityPart) for callers that draw it bone by bone.
type Entity struct {
	Model       string     // item_model id of the whole model
	Scale       float64    // display scale (the model was shrunk by 1/Scale to fit Java's box)
	Translation [3]float64 // display translation, blocks
	Name        string     // the lang name ("" if none)
	// Parts are the model's bones that have cubes, parents first.
	Parts []EntityPart
	// Bones is the whole bone hierarchy (also bones without cubes), for animation later.
	Bones []EntityBone
	// Width/Height are a guess of the entity's size in blocks (from the geometry), and StandIn a
	// vanilla Java entity of about that size.
	Width, Height float64
	StandIn       string
}

// EntityPart is one bone of a custom entity drawn as its own item display.
type EntityPart struct {
	Bone   string // bone name
	Parent string // parent bone ("" root)
	Model  string // item_model id: a model whose (8, 8, 8) is the bone's pivot
	// Offset is where the bone's pivot is relative to the entity's position, in blocks, for an
	// entity facing south (yaw 0): the display's translation.
	Offset [3]float64
	// Pivot and Rotation are the bone's pivot (pixels) and rest rotation (degrees) as the
	// Bedrock geometry has them; the rest rotations of the bone and its parents are already in
	// the part's model.
	Pivot, Rotation [3]float64
	// Scale is how much the part's model was shrunk to fit Java's -16..32 box; its "fixed"
	// display transform scales it back (shown with ItemDisplayContext fixed).
	Scale float64
}

// EntityBone is a bone of a custom entity's geometry.
type EntityBone struct {
	Name     string     `json:"name"`
	Parent   string     `json:"parent,omitempty"`
	Pivot    [3]float64 `json:"pivot"`
	Rotation [3]float64 `json:"rotation"`
	Cubes    int        `json:"cubes"`
}

// Result is a converted pack.
type Result struct {
	Zip  []byte
	SHA1 string // hex

	ItemModels map[string]string // custom item id -> item_model
	ItemNames  map[string]string // custom item id -> display name (en_US)
	Blocks     map[string]int    // custom block name -> index in Input.Slots (its default look)
	Entities   map[string]Entity // custom entity identifier -> model
	Lang       map[string]string // en_US lang keys

	// Mapping is everything above in the documented mapping.json form, with the item, block,
	// sound and language details the translator may also need.
	Mapping *Mapping
	// Report lists what was converted, approximated and skipped.
	Report *Report

	Stats Stats
}

// Stats counts what a conversion made.
type Stats struct {
	Packs, Overrides, Items, Blocks, Entities, LangKeys, Sounds int
	Missing                                                     []string // what could not be converted (first few)
}

func (s *Stats) miss(format string, a ...any) {
	if len(s.Missing) < 20 {
		s.Missing = append(s.Missing, fmt.Sprintf(format, a...))
	}
}

// Empty reports whether the conversion made nothing worth sending.
func (r *Result) Empty() bool {
	s := r.Stats
	return s.Overrides+s.Items+s.Blocks+s.Entities+s.LangKeys+s.Sounds == 0
}

type builder struct {
	in    *Input
	ns    string
	files map[string][]byte
	res   *Result
	rep   *Report
	m     *Mapping

	lang        map[string]map[string]string // java lang code -> key -> text
	itemTex     map[string]texEntry          // item_texture.json short name -> texture
	terrainTex  map[string]texEntry          // terrain_texture.json short name -> texture
	flipbooks   map[string]flipbook          // texture path (lower, no ext) -> animation
	blocksJSON  map[string]blocksJSONEntry   // blocks.json entries by identifier
	geos        map[string]*geometry
	attach      map[string]*attachable // item id -> attachable
	texDone     map[string]texInfo     // source path + kind -> Java texture
	usedSafe    map[string]bool
	blockStates map[string]map[string]stateModel // Java block -> state key -> model
	lookNames   map[string]string                // block look signature -> model id
	lookApprox  map[string]string                // block look signature -> approximation note
	customTex   map[string]bool                  // texture paths custom content uses
}

type texEntry struct {
	path     string
	variants int
}

type texInfo struct {
	id   string
	size image.Point
}

// Convert builds the Java pack.
func Convert(in *Input) (*Result, error) {
	ns := in.Namespace
	if ns == "" {
		ns = Namespace
	}
	b := &builder{in: in, ns: ns, files: map[string][]byte{}, lang: map[string]map[string]string{},
		itemTex: map[string]texEntry{}, terrainTex: map[string]texEntry{}, flipbooks: map[string]flipbook{},
		blocksJSON: map[string]blocksJSONEntry{}, geos: map[string]*geometry{}, attach: map[string]*attachable{},
		texDone: map[string]texInfo{}, usedSafe: map[string]bool{},
		blockStates: map[string]map[string]stateModel{}, lookNames: map[string]string{}, lookApprox: map[string]string{},
		res: &Result{ItemModels: map[string]string{}, ItemNames: map[string]string{}, Blocks: map[string]int{},
			Entities: map[string]Entity{}},
		rep: &Report{},
		m:   newMapping(ns)}
	b.res.Report, b.res.Mapping = b.rep, b.m
	b.res.Stats.Packs = len(in.Packs)
	for _, s := range in.Packs {
		b.rep.Packs = append(b.rep.Packs, packInfo(s, "resources"))
		if s.Encrypted {
			b.rep.add(catPack, s.Name, Skipped, "encrypted (contents.json) and no content key given: not converted")
			continue
		}
		b.readLang(s)
		b.readTextureList(s, "textures/item_texture.json", b.itemTex)
		b.readTextureList(s, "textures/terrain_texture.json", b.terrainTex)
		b.readFlipbooks(s)
		b.readBlocksJSON(s)
		for _, n := range s.Files("models/") {
			if strings.HasSuffix(n, ".json") {
				f, _ := s.File(n)
				parseGeometries(f, b.geos)
			}
		}
		b.readAttachables(s)
		b.notConverted(s)
	}
	for _, s := range in.BehaviorPacks {
		b.rep.Packs = append(b.rep.Packs, packInfo(s, "behaviour"))
		if s.Encrypted {
			b.rep.add(catPack, s.Name, Skipped, "encrypted (contents.json) and no content key given: not read")
		}
	}
	b.overrides()
	b.blocks()
	b.items()
	b.entities()
	b.sounds()
	b.writeLang()
	desc := in.Description
	if desc == "" {
		desc = "Bedrock resource packs for Java"
	}
	minF, maxF := in.MinFormat, in.MaxFormat
	if minF == 0 {
		minF = DefaultMinFormat
	}
	if maxF == nil {
		maxF = DefaultMaxFormat
	}
	b.json("pack.mcmeta", map[string]any{"pack": map[string]any{
		"description": desc, "min_format": minF, "max_format": maxF,
	}})
	z, err := b.zip()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(z)
	b.res.Zip, b.res.SHA1 = z, hex.EncodeToString(sum[:])
	b.m.PackSHA1 = b.res.SHA1
	b.res.Stats.LangKeys = len(b.lang["en_us"])
	b.res.Lang = b.lang["en_us"]
	if b.res.Lang == nil {
		b.res.Lang = map[string]string{}
	}
	b.rep.finish(b.res)
	return b.res, nil
}

func (b *builder) json(name string, v any) {
	data, _ := json.MarshalIndent(v, "", " ")
	b.files[name] = data
}

// assetPath is the zip path of an asset of the pack's namespace.
func (b *builder) assetPath(kind, rel string) string {
	return "assets/" + b.ns + "/" + kind + "/" + rel
}

// zipTime is the fixed modification time of every zip entry (the same input gives the same
// bytes, so the same SHA-1).
var zipTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// zip writes the files in name order with a fixed time, so the same input gives the same hash.
func (b *builder) zip() ([]byte, error) {
	names := make([]string, 0, len(b.files))
	for n := range b.files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate, Modified: zipTime})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(b.files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readLang merges a pack's texts/*.lang ("key=value", "##" comments, tab comments after values)
// under Java's lower-case language codes.
func (b *builder) readLang(s *Source) {
	for _, n := range s.Files("texts/") {
		if !strings.HasSuffix(n, ".lang") || strings.Count(n, "/") != 1 {
			continue
		}
		code := strings.TrimSuffix(path.Base(n), ".lang")
		data, _ := s.File(n)
		m := b.lang[code]
		if m == nil {
			m = map[string]string{}
			b.lang[code] = m
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf")
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(k) == "" {
				continue
			}
			if i := strings.Index(v, "\t#"); i >= 0 {
				v = v[:i]
			} else if i := strings.Index(v, "\t##"); i >= 0 {
				v = v[:i]
			}
			m[strings.TrimSpace(k)] = strings.TrimRight(v, " \t\r")
		}
	}
}

// en is the en_US text of a lang key.
func (b *builder) en(key string) (string, bool) {
	v, ok := b.lang["en_us"][key]
	return v, ok
}

// writeLang writes every language's keys as assets/<ns>/lang/<code>.json.
func (b *builder) writeLang() {
	codes := make([]string, 0, len(b.lang))
	for c := range b.lang {
		if len(b.lang[c]) > 0 {
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	for _, c := range codes {
		b.json(b.assetPath("lang", c+".json"), b.lang[c])
	}
	b.m.Languages = codes
	if len(codes) > 0 {
		b.rep.add(catLang, strings.Join(codes, " "), Converted, fmt.Sprintf("%d languages, %d en_us keys", len(codes), len(b.lang["en_us"])))
	}
}

// readTextureList merges item_texture.json / terrain_texture.json (short name -> texture path).
func (b *builder) readTextureList(s *Source, name string, into map[string]texEntry) {
	var f struct {
		TextureData map[string]struct {
			Textures json.RawMessage `json:"textures"`
		} `json:"texture_data"`
	}
	if !s.readJSON(name, &f) {
		return
	}
	for k, v := range f.TextureData {
		paths := texturePaths(v.Textures)
		if len(paths) > 0 {
			into[k] = texEntry{path: paths[0], variants: len(paths)}
		}
	}
}

// texturePaths reads a "textures" value: a path, a list of paths (variants), an object with
// "path" or "variations", or a list of those.
func texturePaths(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var out []string
		for _, e := range list {
			out = append(out, texturePaths(e)...)
		}
		return out
	}
	var obj struct {
		Path     string          `json:"path"`
		Variants json.RawMessage `json:"variations"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.Path != "" {
			return []string{obj.Path}
		}
		if len(obj.Variants) > 0 {
			return texturePaths(obj.Variants)
		}
	}
	return nil
}

// flipbook is an animated texture (textures/flipbook_textures.json).
type flipbook struct {
	ticks  int
	frames []int
}

func (b *builder) readFlipbooks(s *Source) {
	var list []struct {
		Texture string          `json:"flipbook_texture"`
		Ticks   int             `json:"ticks_per_frame"`
		Frames  json.RawMessage `json:"frames"`
	}
	if !s.readJSON("textures/flipbook_textures.json", &list) {
		return
	}
	for _, f := range list {
		fb := flipbook{ticks: f.Ticks}
		var frames []int
		if json.Unmarshal(f.Frames, &frames) == nil {
			fb.frames = frames
		}
		b.flipbooks[texKey(f.Texture)] = fb
	}
}

func texKey(p string) string {
	p = strings.ToLower(strings.TrimPrefix(p, "/"))
	return strings.TrimSuffix(strings.TrimSuffix(p, ".png"), ".tga")
}

// findTexture finds a texture by Bedrock path (without extension) in the packs, the last pack
// first. It returns the file and whether it is a TGA.
func (b *builder) findTexture(p string) ([]byte, bool, bool) {
	p = texKey(p)
	for i := len(b.in.Packs) - 1; i >= 0; i-- {
		s := b.in.Packs[i]
		if s.Encrypted {
			continue
		}
		if f, ok := s.File(p + ".png"); ok {
			return f, false, true
		}
		if f, ok := s.File(p + ".tga"); ok {
			return f, true, true
		}
	}
	return nil, false, false
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9/._-]+`)

// safeName turns an identifier into a unique Java resource path ("ns:void_scythe" ->
// "ns/void_scythe").
func (b *builder) safeName(kind, id string) string {
	s := strings.ToLower(strings.ReplaceAll(id, ":", "/"))
	s = unsafeChars.ReplaceAllString(s, "_")
	s = strings.Trim(strings.ReplaceAll(s, "..", "_"), "/")
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	if s == "" {
		s = "x"
	}
	base, n := s, 2
	for b.usedSafe[kind+"|"+s] {
		s = fmt.Sprintf("%s_%d", base, n)
		n++
	}
	b.usedSafe[kind+"|"+s] = true
	return s
}

// texture adds the texture at Bedrock path p under assets/<ns>/textures/<dir>/ and returns its
// Java id ("<ns>:item/models/weapons/katana"). Flipbook textures get their .mcmeta.
func (b *builder) texture(dir, p string) (texInfo, bool) {
	key := dir + "|" + texKey(p)
	if t, ok := b.texDone[key]; ok {
		return t, true
	}
	data, tga, ok := b.findTexture(p)
	if !ok {
		return texInfo{}, false
	}
	png, size, err := toPNG(data, tga)
	if err != nil {
		return texInfo{}, false
	}
	name := strings.TrimPrefix(texKey(p), "textures/")
	safe := b.safeName("tex/"+dir, name)
	file := b.assetPath("textures", dir+"/"+safe+".png")
	b.files[file] = png
	if fb, ok := b.flipbooks[texKey(p)]; ok && size.X > 0 && size.Y > size.X && size.Y%size.X == 0 {
		anim := map[string]any{"frametime": max(1, fb.ticks)}
		if len(fb.frames) > 0 {
			anim["frames"] = fb.frames
		}
		b.json(file+".mcmeta", map[string]any{"animation": anim})
	}
	t := texInfo{id: b.ns + ":" + dir + "/" + safe, size: size}
	b.texDone[key] = t
	return t, true
}

// itemModelID is the item_model id of items/<safe>.json.
func (b *builder) itemModelID(safe string) string { return b.ns + ":" + safe }

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case uint8:
		return t != 0
	case int8:
		return t != 0
	case int16:
		return t != 0
	case int32:
		return t != 0
	case int64:
		return t != 0
	case int:
		return t != 0
	case float32:
		return t != 0
	case float64:
		return t != 0
	case map[string]any:
		if v, ok := t["value"]; ok {
			return truthy(v)
		}
		return true // a present component object
	}
	return false
}

func num(v any) float64 {
	switch t := v.(type) {
	case float32:
		return float64(t)
	case float64:
		return t
	case int:
		return float64(t)
	case int8:
		return float64(t)
	case int16:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case uint8:
		return float64(t)
	case uint16:
		return float64(t)
	case uint32:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case map[string]any:
		return num(t["value"])
	}
	return 0
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["value"].(string); ok {
			return s
		}
	}
	return ""
}

func sortStrings(s []string) { sort.Strings(s) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
