package javapack

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

	"delta/core/internal/anim"
)

// Version changes when the converter's output changes (part of the conversion cache key).
const Version = 6

// Java 26.3's resource pack format (version.json pack_version.resource_major/minor); 26.2 clients
// (88) are in the range too.
var (
	minFormat = 88
	maxFormat = []int{97, 1}
)

// Namespace is the Java namespace the converted assets live in.
const Namespace = "delta"

// Input is what a conversion needs.
type Input struct {
	// Packs in the order the server's stack applies them: later packs win.
	Packs []*Source
	// Items are the server's custom items (ItemRegistry entries with components).
	Items []Item
	// Blocks are the server's custom blocks (StartGame's block list).
	Blocks []Block
	// Slots are the free Java block states custom blocks may be drawn on, in order of preference
	// (see Candidates).
	Slots []Slot
	// MaxParts is the most displays an animated entity is split into (0: anim.MaxParts;
	// anim.AllParts: a part for every bone that moves).
	MaxParts int
	// FullParts also makes the full-detail part set (a part for every bone that moves) of the
	// entities MaxParts merges: Entity.Anim.Full.
	FullParts bool
	// FormIcons adds the font of the form icons (icons.go): a glyph for every texture a form's
	// button could name.
	FormIcons bool
	// Description is the pack's description line.
	Description string
}

// Item is a custom item definition.
type Item struct {
	ID   string
	Data map[string]any // the ItemRegistry entry's data ({"components": ...})
}

// Block is a custom block definition.
type Block struct {
	Name  string
	Props map[string]any // the BlockEntry's properties ({"components": ..., "permutations": ...})
}

// Entity is how a custom entity is drawn: the item model an item_display shows and the display
// transformation that puts it where the Bedrock model is (see geometry.go for the axes).
type Entity struct {
	Model       string     // item_model id
	Scale       float64    // display scale (the model was shrunk by 1/Scale to fit Java's box)
	Translation [3]float64 // display translation, blocks
	Name        string     // the lang name ("" if none)
	// Anim is set for an entity whose file has scripts: its rig (scripts.scale, animations) and,
	// when animations move it, the per-part models (Anim.Moves).
	Anim *anim.Model
}

// Result is a converted pack.
type Result struct {
	Zip  []byte
	SHA1 string // hex

	ItemModels map[string]string // custom item id -> item_model
	ItemNames  map[string]string // custom item id -> display name
	ItemEquip  map[string]Equip  // wearable custom item id -> slot and equipment asset
	Blocks     map[string]int    // custom block name -> index in Input.Slots
	Entities   map[string]Entity // custom entity identifier -> model
	// StandIns are the custom entities shown as a Java entity of their (vanilla) shape.
	StandIns map[string]StandIn
	Lang     map[string]string
	// HudMarkers are the texts a pack's own HUD (ui/*.json) looks for in the title, subtitle or
	// action bar: a title holding one is data for that HUD, not text for the player.
	HudMarkers []string
	// FormScreens are the screens a pack draws for server forms (ui/server_form.json): what the
	// bridge needs to show such a form as text instead of the packed data it carries.
	FormScreens []FormScreen
	// FormIcons are the glyphs of the packs' textures in the icon font, by texture path (lower
	// case, no extension); HasIcons says the pack has that font (see Icon).
	FormIcons map[string]rune
	HasIcons  bool

	Stats Stats
}

// Stats counts what a conversion made.
type Stats struct {
	Packs, Overrides, Items, Blocks, Entities, LangKeys int
	Equipment, Held                                     int // armour pieces with a layer, 3D held models
	Animated, Parts                                     int // entities with animated parts, and their part models
	FullParts                                           int // part models of the full-detail sets
	StandIns                                            int // entities shown as a Java entity of their vanilla shape
	AnimBytes                                           int // animation files kept (compressed) for rigs compiled later
	// The packs' textures with a glyph in the icon font, the glyphs (like pictures share one)
	// and the size of their sheets.
	FormIcons, IconGlyphs, IconBytes int
	EntitiesTook                     time.Duration
	Missing                          []string // what could not be converted (first few)
}

func (s *Stats) miss(format string, a ...any) {
	if len(s.Missing) < 20 {
		s.Missing = append(s.Missing, fmt.Sprintf(format, a...))
	}
}

// Empty reports whether the conversion made nothing worth sending.
func (r *Result) Empty() bool {
	s := r.Stats
	return s.Overrides+s.Items+s.Blocks+s.Entities+s.StandIns+s.LangKeys+s.FormIcons == 0
}

type builder struct {
	in    *Input
	files map[string][]byte
	res   *Result

	lang        map[string]string
	itemTex     map[string]string            // item_texture.json short name -> path
	terrainTex  map[string]string            // terrain_texture.json short name -> path
	blocksTex   map[string]map[string]string // blocks.json: block -> face (or "*") -> terrain short name
	geos        map[string]*geometry
	texDone     map[string]texInfo // source path + kind -> Java texture
	usedSafe    map[string]bool
	blockStates map[string]map[string]string // Java block -> state key -> model

	attMain   map[string]*attachable   // item -> its attachable
	attSub    map[string][]*attachable // item -> attachables in subpacks
	anims     map[string]map[string]animBone
	equipDone map[string]string // layer|root|path -> equipment texture name
	equipName map[string]string // root|path -> equipment texture name
	itemSafe  map[string]string // item -> its models/item and items/ name
	itemFlat  map[string]bool   // items with a flat icon model
}

type texInfo struct {
	id   string
	size image.Point
}

// Convert builds the Java pack.
func Convert(in *Input) (*Result, error) {
	b := &builder{in: in, files: map[string][]byte{}, lang: map[string]string{},
		itemTex: map[string]string{}, terrainTex: map[string]string{}, blocksTex: map[string]map[string]string{}, geos: map[string]*geometry{},
		texDone: map[string]texInfo{}, usedSafe: map[string]bool{}, blockStates: map[string]map[string]string{},
		equipDone: map[string]string{}, equipName: map[string]string{}, itemSafe: map[string]string{}, itemFlat: map[string]bool{},
		res: &Result{ItemModels: map[string]string{}, ItemNames: map[string]string{}, ItemEquip: map[string]Equip{}, Blocks: map[string]int{},
			Entities: map[string]Entity{}, StandIns: map[string]StandIn{}}}
	b.res.Stats.Packs = len(in.Packs)
	for _, s := range in.Packs {
		b.readLang(s)
		b.readTextureList(s, "textures/item_texture.json", b.itemTex)
		b.readTextureList(s, "textures/terrain_texture.json", b.terrainTex)
		b.readBlocksJSON(s)
		for _, n := range s.Files("models/") {
			if strings.HasSuffix(n, ".json") {
				f, _ := s.File(n)
				parseGeometries(f, b.geos)
			}
		}
	}
	b.overrides()
	b.readAttachables()
	b.items()
	b.armor()
	b.held()
	b.blocks()
	start := time.Now()
	b.entities()
	b.res.Stats.EntitiesTook = time.Since(start)
	if in.FormIcons {
		b.formIcons()
	}
	if len(b.lang) > 0 {
		b.json("assets/"+Namespace+"/lang/en_us.json", b.lang)
		b.res.Stats.LangKeys = len(b.lang)
	}
	b.res.Lang = b.lang
	desc := in.Description
	if desc == "" {
		desc = "Bedrock server resources (delta)"
	}
	b.json("pack.mcmeta", map[string]any{"pack": map[string]any{
		"description": desc, "min_format": minFormat, "max_format": maxFormat,
	}})
	z, err := b.zip()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(z)
	b.res.Zip, b.res.SHA1 = z, hex.EncodeToString(sum[:])
	return b.res, nil
}

func (b *builder) json(name string, v any) {
	data, _ := json.MarshalIndent(v, "", " ")
	b.files[name] = data
}

// zip writes the files in name order with a fixed time, so the same input gives the same hash.
func (b *builder) zip() ([]byte, error) {
	names := make([]string, 0, len(b.files))
	for n := range b.files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate, Modified: t})
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

// readLang merges a pack's en_US.lang ("key=value", "##" comments, tab comments after values).
func (b *builder) readLang(s *Source) {
	data, ok := s.File("texts/en_US.lang")
	if !ok {
		return
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
		}
		b.lang[strings.TrimSpace(k)] = strings.TrimRight(v, " \t\r")
	}
}

// readTextureList merges item_texture.json / terrain_texture.json (short name -> texture path).
func (b *builder) readTextureList(s *Source, name string, into map[string]string) {
	var f struct {
		TextureData map[string]struct {
			Textures json.RawMessage `json:"textures"`
		} `json:"texture_data"`
	}
	if !s.readJSON(name, &f) {
		return
	}
	for k, v := range f.TextureData {
		if p := firstTexturePath(v.Textures); p != "" {
			into[k] = p
		}
	}
}

// firstTexturePath reads a "textures" value: a path, a list of paths (variants: the first), an
// object with "path", or a list of those.
func firstTexturePath(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		for _, e := range list {
			if p := firstTexturePath(e); p != "" {
				return p
			}
		}
		return ""
	}
	var obj struct {
		Path     string          `json:"path"`
		Variants json.RawMessage `json:"variations"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.Path != "" {
			return obj.Path
		}
		if len(obj.Variants) > 0 {
			return firstTexturePath(obj.Variants)
		}
	}
	return ""
}

// findTexture finds a texture by Bedrock path (without extension) in the packs, the last pack
// first. It returns the file and whether it is a TGA.
func (b *builder) findTexture(p string) ([]byte, bool, bool) {
	p = strings.TrimSuffix(strings.TrimPrefix(p, "/"), ".png")
	p = strings.TrimSuffix(p, ".tga")
	for i := len(b.in.Packs) - 1; i >= 0; i-- {
		s := b.in.Packs[i]
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

// safeName turns an identifier into a unique Java resource path ("example:void_scythe" ->
// "example/void_scythe").
func (b *builder) safeName(kind, id string) string {
	s := strings.ToLower(strings.ReplaceAll(id, ":", "/"))
	s = unsafeChars.ReplaceAllString(s, "_")
	s = strings.Trim(strings.ReplaceAll(s, "..", "_"), "/")
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

// texture adds the texture at Bedrock path p under assets/delta/textures/<dir>/ and returns
// its Java id ("delta:item/example/void_scythe").
func (b *builder) texture(dir, p string) (texInfo, bool) { return b.textureAt(dir, "", p) }

// textureAt is texture for a path looked up in a subpack (root, "subpacks/<name>/") first.
func (b *builder) textureAt(dir, root, p string) (texInfo, bool) {
	key := dir + "|" + root + "|" + strings.ToLower(p)
	if t, ok := b.texDone[key]; ok {
		return t, true
	}
	data, tga, ok := b.findTextureIn(root, p)
	if !ok {
		// Not in the packs: a vanilla texture the add-on reuses by its Bedrock name
		// ("nether_star", "book_writable"), which the Java client has under its own name.
		kind := dir
		if java, ok := javaTextureFor(kind, strings.TrimPrefix(strings.ToLower(p), "textures/")); ok {
			t := texInfo{id: "minecraft:" + java, size: image.Point{16, 16}}
			b.texDone[key] = t
			return t, true
		}
		return texInfo{}, false
	}
	png, size, err := toPNG(data, tga)
	if err != nil {
		return texInfo{}, false
	}
	name := strings.TrimPrefix(strings.ToLower(p), "textures/")
	safe := b.safeName("tex/"+dir, name)
	b.files["assets/"+Namespace+"/textures/"+dir+"/"+safe+".png"] = png
	t := texInfo{id: Namespace + ":" + dir + "/" + safe, size: size}
	b.texDone[key] = t
	return t, true
}

// overrides copies textures that replace vanilla ones (textures/blocks, textures/items) to their
// Java names.
func (b *builder) overrides() {
	seen := map[string]bool{}
	for i := len(b.in.Packs) - 1; i >= 0; i-- {
		s := b.in.Packs[i]
		for _, kind := range []struct{ from, to string }{{"textures/blocks/", "block"}, {"textures/items/", "item"}} {
			for _, n := range s.Files(kind.from) {
				ext := path.Ext(n)
				if ext != ".png" && ext != ".tga" {
					continue
				}
				rel := strings.TrimSuffix(strings.TrimPrefix(n, kind.from), ext)
				java, ok := javaTextureFor(kind.to, rel)
				if !ok {
					continue
				}
				dst := "assets/minecraft/textures/" + java + ".png"
				if seen[dst] {
					continue
				}
				data, _ := s.File(n)
				png, _, err := toPNG(data, ext == ".tga")
				if err != nil {
					continue
				}
				seen[dst] = true
				b.files[dst] = png
				b.res.Stats.Overrides++
			}
		}
	}
}

// items makes an item model per custom item with an icon.
func (b *builder) items() {
	for _, it := range b.in.Items {
		comps, _ := it.Data["components"].(map[string]any)
		props, _ := comps["item_properties"].(map[string]any)
		icon := iconName(comps["minecraft:icon"])
		if icon == "" {
			icon = iconName(props["minecraft:icon"])
		}
		if name := b.itemName(it.ID, comps); name != "" {
			b.res.ItemNames[it.ID] = name
		}
		if icon == "" {
			b.res.Stats.miss("item %s: no icon", it.ID)
			continue
		}
		p, ok := b.itemTex[icon]
		if !ok {
			p = icon // some servers give the path itself
		}
		tex, ok := b.texture("item", p)
		if !ok {
			b.res.Stats.miss("item %s: texture %s not found", it.ID, p)
			continue
		}
		parent := "minecraft:item/generated"
		if truthy(props["hand_equipped"]) || truthy(comps["minecraft:hand_equipped"]) {
			parent = "minecraft:item/handheld"
		}
		safe := b.safeName("item", it.ID)
		b.itemSafe[it.ID], b.itemFlat[it.ID] = safe, true
		b.json("assets/"+Namespace+"/models/item/"+safe+".json", map[string]any{
			"parent": parent, "textures": map[string]any{"layer0": tex.id},
		})
		b.json("assets/"+Namespace+"/items/"+safe+".json", map[string]any{
			"model": map[string]any{"type": "minecraft:model", "model": Namespace + ":item/" + safe},
		})
		b.res.ItemModels[it.ID] = Namespace + ":" + safe
		b.res.Stats.Items++
	}
}

// iconName reads a minecraft:icon value: {"textures": {"default": x}}, {"texture": x} or x.
func iconName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s, ok := t["texture"].(string); ok {
			return s
		}
		if m, ok := t["textures"].(map[string]any); ok {
			if s, ok := m["default"].(string); ok {
				return s
			}
			for _, x := range m {
				if s, ok := x.(string); ok {
					return s
				}
			}
		}
		if s, ok := t["textures"].(string); ok {
			return s
		}
	}
	return ""
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case uint8:
		return t != 0
	case int8:
		return t != 0
	case int32:
		return t != 0
	case int64:
		return t != 0
	case map[string]any:
		return truthy(t["value"])
	}
	return false
}

// itemName is a custom item's name: its display_name component (a lang key or text), else the
// lang's item.<id>.name.
func (b *builder) itemName(id string, comps map[string]any) string {
	if dn, ok := comps["minecraft:display_name"].(map[string]any); ok {
		if v, ok := dn["value"].(string); ok && v != "" {
			if t, ok := b.lang[v]; ok {
				return t
			}
			if t, ok := b.lang[strings.TrimSuffix(v, ".name")+".name"]; ok && strings.HasPrefix(v, "item.") {
				return t
			}
			return v
		}
	}
	for _, k := range []string{"item." + id + ".name", "item." + id, "item." + strings.SplitN(id, ":", 2)[len(strings.SplitN(id, ":", 2))-1] + ".name"} {
		if t, ok := b.lang[k]; ok {
			return t
		}
	}
	return ""
}

// readBlocksJSON reads a pack's blocks.json, where older packs (and many add-ons) name a custom
// block's textures instead of the block's material_instances: "textures" is one terrain texture
// for every face or an object by face ("up", "down", "side", or each side by name).
func (b *builder) readBlocksJSON(s *Source) {
	var f map[string]json.RawMessage
	if !s.readJSON("blocks.json", &f) {
		return
	}
	for name, raw := range f {
		var e struct {
			Textures json.RawMessage `json:"textures"`
		}
		if json.Unmarshal(raw, &e) != nil || len(e.Textures) == 0 {
			continue
		}
		faces := map[string]string{}
		var one string
		var byFace map[string]string
		switch {
		case json.Unmarshal(e.Textures, &one) == nil && one != "":
			faces["*"] = one
		case json.Unmarshal(e.Textures, &byFace) == nil:
			for k, v := range byFace {
				if k == "side" {
					for _, sd := range []string{"north", "south", "west", "east"} {
						if _, ok := byFace[sd]; !ok {
							faces[sd] = v
						}
					}
					continue
				}
				faces[k] = v
			}
		}
		if len(faces) > 0 {
			b.blocksTex[name] = faces
		}
	}
}

// hudMarker finds the texts a JSON UI file takes out of the HUD's title strings, the way packs test
// "does the title hold my marker": (#hud_title_text_string - 'cbm:party').
var hudMarker = regexp.MustCompile(`#hud_(?:title|subtitle|actionbar)_text_string\s*-\s*'([^']{3,64})'`)

// HudMarkersOf lists the HUD markers of the packs' ui files, sorted.
func HudMarkersOf(packs []*Source) []string {
	seen := map[string]bool{}
	for _, s := range packs {
		for name, b := range s.files {
			if !strings.HasPrefix(name, "ui/") || !strings.HasSuffix(name, ".json") {
				continue
			}
			for _, m := range hudMarker.FindAllSubmatch(b, -1) {
				seen[string(m[1])] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
