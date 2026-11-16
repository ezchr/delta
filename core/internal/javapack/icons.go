package javapack

import (
	"bytes"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Form icons: a Bedrock form button can carry a picture, a texture path of the server's packs or
// of the game ("textures/items/diamond"). A Java dialog button's label is text, so the pictures
// are glyphs of a font of the converted pack (IconFont): the bridge puts a button's glyph, in
// that font, before its label.
//
// A form names its pictures when it is shown, long after the pack was made, and the scripts that
// name them are not in the resource packs (and build the paths: "icons/" + id). So the font has
// a glyph for every texture a form could name:
//
//   - the packs' textures, all folders but the ones that hold no pictures for a screen (entity
//     skins, particles, the sky...), drawn into sheets of 16 x 16 pixel cells;
//   - the game's own item and block textures, which the Java client has (under Java's name,
//     textures.go): a provider that points at the client's file, nothing is copied;
//   - a few of the game's UI pictures that Java has a look-alike sprite of (uiSprites).
//
// Pictures that come out the same share a glyph. Glyph n of the packs' textures has the
// character iconRune(n) in IconFont. The font is its own, so the characters mean nothing else;
// they are kept in the basic plane because a string of a network NBT tag is Java's modified
// UTF-8.
//
// A client drops a WHOLE font when one of its providers cannot be loaded (FontManager's
// UnresolvedBuilderBundle.resolve gives nothing when any builder was rejected), and every glyph
// of it is the missing-glyph box then. A provider that points at a file of the client is
// rejected when that client version has no such file. So:
//
//   - IconFont has the pack's own sheets only: nothing in it can be missing;
//   - the client's textures are in fonts of their own, gameFontSize glyphs each (gameFont), so
//     a file a client lacks costs that one font's glyphs and not the others';
//   - only textures every supported client version has get a glyph (newJava are left out).

// IconFont is the font of the form icons that are the packs' own textures.
const IconFont = Namespace + ":icons"

// gameFontSize is how many of the client's own textures one font has glyphs of.
const gameFontSize = 64

// gameFont is the font and the character of glyph i of the client's own textures.
func gameFont(i int) (font string, c rune) {
	return Namespace + ":game" + strconv.Itoa(i/gameFontSize), 0xE000 + rune(i%gameFontSize)
}

// newJava are the textures of javaTextureNames (Java 26.3) that a 26.2 client does not have: no
// glyph, a font that names one is dropped whole by that client.
var newJava = sync.OnceValue(func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`block/destroy_oit_stage_0 block/destroy_oit_stage_1 block/destroy_oit_stage_2
block/destroy_oit_stage_3 block/destroy_oit_stage_4 block/destroy_oit_stage_5 block/destroy_oit_stage_6
block/destroy_oit_stage_7 block/destroy_oit_stage_8 block/destroy_oit_stage_9 block/orange_poplar_leaves
block/poplar_door_bottom block/poplar_door_top block/poplar_hanging_sign block/poplar_log block/poplar_log_top
block/poplar_planks block/poplar_sapling block/poplar_shelf block/poplar_sign block/poplar_trapdoor
block/red_poplar_leaves block/red_shrub block/shelf_mushroom_particle block/shelf_mushroom_stage0
block/shelf_mushroom_stage1 block/straw_bed block/straw_bed_particle block/stripped_poplar_log
block/stripped_poplar_log_top block/yellow_poplar_leaves item/abandoned_camp_map item/black_cushion
item/blue_cushion item/brown_cushion item/buried_ancient_city_map item/buried_mineshaft_map
item/buried_treasure_map item/buried_trial_chambers_map item/cyan_cushion item/desert_pyramid_map
item/desert_village_map item/gray_cushion item/green_cushion item/jungle_pyramid_map item/light_blue_cushion
item/light_gray_cushion item/lime_cushion item/magenta_cushion item/ocean_monument_map item/orange_cushion
item/pink_cushion item/plains_village_map item/poplar_boat item/poplar_chest_boat item/poplar_door
item/poplar_hanging_sign item/poplar_sign item/purple_cushion item/red_cushion item/savanna_village_map
item/snowy_village_map item/swamp_hut_map item/taiga_village_map item/warm_ocean_ruins_map item/white_cushion
item/woodland_mansion_map item/yellow_cushion`) {
		m[n] = true
	}
	return m
})

const (
	// iconCell is a glyph's size, in the sheets and on the screen. A button is 20 pixels high
	// and its text's baseline is 13 below its top: 16 pixels with 12 above the baseline sit in
	// the middle.
	iconCell   = 16
	iconAscent = 12
	sheetCols  = 16 // glyphs in a row of a sheet
	sheetRows  = 16
	// maxIconSource is the largest texture, per side, that is read for a glyph.
	maxIconSource = 2048
)

// iconRanges are the characters the glyphs take, in this order.
var iconRanges = [...][2]rune{{0xE000, 0xF8FF}, {0x4E00, 0x9FFF}, {0xAC00, 0xD7A3}}

// iconRune is the character of glyph n; false when the font is full.
func iconRune(n int) (rune, bool) {
	if n < 0 {
		return 0, false
	}
	for _, r := range iconRanges {
		size := int(r[1]-r[0]) + 1
		if n < size {
			return r[0] + rune(n), true
		}
		n -= size
	}
	return 0, false
}

// iconSkipDirs are the folders under textures/ whose files are not pictures for a screen.
var iconSkipDirs = []string{"entity/", "models/", "particle/", "particles/", "environment/", "colormap/",
	"painting/", "persona_thumbnails/", "misc/"}

// tallJava are Java textures that are not square (animation strips): no glyph, a bitmap glyph
// is the whole file.
var tallJava = sync.OnceValue(func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`blast_furnace_front_on campfire_fire campfire_log_lit chain_command_block_back
chain_command_block_conditional chain_command_block_front chain_command_block_side command_block_back
command_block_conditional command_block_front command_block_side copper_lantern crimson_stem
exposed_copper_lantern fire_0 fire_1 firefly_bush_emissive kelp kelp_plant lantern lava_flow lava_still magma
nether_portal oxidized_copper_lantern prismarine repeating_command_block_back
repeating_command_block_conditional repeating_command_block_front repeating_command_block_side
respawn_anchor_top sculk sculk_catalyst_side_bloom sculk_catalyst_top_bloom sculk_sensor_tendril_active
sculk_sensor_tendril_inactive sculk_shrieker_can_summon_inner_top sculk_shrieker_inner_top sculk_vein
sea_lantern seagrass smoker_front_on soul_campfire_fire soul_campfire_log_lit soul_fire_0 soul_fire_1
soul_lantern stonecutter_saw tall_seagrass_bottom tall_seagrass_top warped_stem water_flow water_still
weathered_copper_lantern`) {
		m["block/"+n] = true
	}
	return m
})

// uiSprites are pictures of Bedrock's own textures/ui that Java has a sprite like: the name
// under textures/ui (lower case) and the Java texture. Bedrock's UI pictures are not in a
// server's packs and not in a Java client, so the others have no glyph.
var uiSprites = map[string]string{
	"check": "gui/sprites/icon/checkmark", "checkmark": "gui/sprites/icon/checkmark",
	"realms_green_check": "gui/sprites/icon/checkmark", "confirm": "gui/sprites/container/beacon/confirm",
	"cancel": "gui/sprites/container/beacon/cancel", "redx1": "gui/sprites/container/beacon/cancel",
	"realms_red_x": "gui/sprites/container/beacon/cancel", "crossout": "gui/sprites/container/beacon/cancel",
	"magnifyingglass": "gui/sprites/icon/search", "magnifying_glass": "gui/sprites/icon/search",
	"arrow_left": "gui/sprites/spectator/scroll_left", "arrowleft": "gui/sprites/spectator/scroll_left",
	"arrow_right": "gui/sprites/spectator/scroll_right", "arrowright": "gui/sprites/spectator/scroll_right",
	"arrow_up": "gui/sprites/statistics/sort_up", "arrowup": "gui/sprites/statistics/sort_up", "up_arrow": "gui/sprites/statistics/sort_up",
	"arrow_down": "gui/sprites/statistics/sort_down", "arrowdown": "gui/sprites/statistics/sort_down", "down_arrow": "gui/sprites/statistics/sort_down",
	"lock": "gui/sprites/widget/locked_button", "icon_lock": "gui/sprites/widget/locked_button", "lock_color": "gui/sprites/widget/locked_button",
	"icon_unlocked": "gui/sprites/widget/unlocked_button",
	"heart":         "gui/sprites/hud/heart/full", "heart_new": "gui/sprites/hud/heart/full",
	"hunger_full": "gui/sprites/hud/food_full", "armor_full": "gui/sprites/hud/armor_full",
	"infobulb": "gui/sprites/icon/info", "op": "gui/sprites/player_list/make_operator",
	"permissions_op_crown": "gui/sprites/player_list/make_operator",
	"mute_on":              "gui/sprites/social_interactions/mute_button", "mute_off": "gui/sprites/social_interactions/unmute_button",
	"world_glyph_color": "gui/sprites/icon/language", "world_glyph": "gui/sprites/icon/language",
	"accessibility_glyph_color": "gui/sprites/icon/accessibility", "accessibility_glyph": "gui/sprites/icon/accessibility",
	"warningglyph": "gui/sprites/world_list/warning", "errorglyph": "gui/sprites/world_list/error",
}

// vanillaIconFiles are the client's own textures that have a glyph, in glyph order: glyph i
// (gameFont) is file i. Every one is a file of every supported client version (26.2 and 26.3:
// the sprites of uiSprites are in both).
var vanillaIconFiles = sync.OnceValue(func() []string {
	var out []string
	tall, newer := tallJava(), newJava()
	for _, n := range strings.Fields(javaTextureNames) {
		if !tall[n] && !newer[n] {
			out = append(out, n)
		}
	}
	seen := map[string]bool{}
	var sprites []string
	for _, s := range uiSprites {
		if !seen[s] {
			seen[s] = true
			sprites = append(sprites, s)
		}
	}
	sort.Strings(sprites)
	return append(out, sprites...)
})

var vanillaIconIndex = sync.OnceValue(func() map[string]int {
	m := map[string]int{}
	for i, f := range vanillaIconFiles() {
		m[f] = i
	}
	return m
})

// iconKey is a picture path as the icons are looked up by: lower case, no extension.
func iconKey(p string) string {
	p = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(p, "\\", "/")))
	p = strings.TrimPrefix(p, "/")
	for _, ext := range []string{".png", ".tga", ".jpg", ".jpeg"} {
		p = strings.TrimSuffix(p, ext)
	}
	return p
}

// vanillaIcon is the glyph of one of the game's own textures by its Bedrock path (an iconKey).
func vanillaIcon(key string) (string, rune, bool) {
	var file string
	if rel, ok := strings.CutPrefix(key, "textures/items/"); ok {
		file, _ = javaTextureFor("item", rel)
	} else if rel, ok := strings.CutPrefix(key, "textures/blocks/"); ok {
		file, _ = javaTextureFor("block", rel)
	} else if rel, ok := strings.CutPrefix(key, "textures/ui/"); ok {
		if s, ok := uiSprites[rel]; ok {
			file = s
		} else if item, ok := strings.CutPrefix(rel, "icon_"); ok && !strings.Contains(item, "/") {
			file, _ = javaTextureFor("item", item) // "icon_apple", "icon_iron_pickaxe": the item
		}
	}
	if file == "" {
		return "", 0, false
	}
	n, ok := vanillaIconIndex()[file]
	if !ok {
		return "", 0, false
	}
	font, c := gameFont(n)
	return font, c, true
}

// Icon is the font and the character that draw the texture a form names with path; false when
// the pack has no glyph of it.
func (r *Result) Icon(path string) (font string, c rune, ok bool) {
	if r == nil || !r.HasIcons {
		return "", 0, false
	}
	key := iconKey(path)
	if c, ok := r.FormIcons[key]; ok {
		return IconFont, c, true
	}
	return vanillaIcon(key)
}

// iconTexture reports whether a pack's file is a texture that gets a glyph, and its key.
func iconTexture(name string) (key string, tga, ok bool) {
	rel, ok := strings.CutPrefix(name, "textures/")
	if !ok {
		return "", false, false
	}
	switch path.Ext(name) {
	case ".png":
	case ".tga":
		tga = true
	default:
		return "", false, false
	}
	for _, d := range iconSkipDirs {
		if strings.HasPrefix(rel, d) {
			return "", false, false
		}
	}
	return iconKey(name), tga, true
}

// formIcons writes the icon fonts: the sheets of the packs' textures and their font, and the
// fonts of the client's own textures.
func (b *builder) formIcons() {
	type source struct {
		data []byte
		tga  bool
		pack int
	}
	src := map[string]source{}
	for i, s := range b.in.Packs {
		for _, n := range s.Files("textures/") {
			key, tga, ok := iconTexture(n)
			if !ok {
				continue
			}
			if old, ok := src[key]; ok && old.pack == i && !old.tga {
				continue // a pack with both: the PNG, as findTexture
			}
			f, _ := s.File(n)
			src[key] = source{f, tga, i}
		}
	}
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// The textures are read and shrunk side by side: a big add-on has ten thousand of them.
	cells := make([]*image.NRGBA, len(keys))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < max(1, runtime.GOMAXPROCS(0)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				s := src[keys[i]]
				strip := strings.HasPrefix(keys[i], "textures/blocks/") || strings.HasPrefix(keys[i], "textures/items/")
				cells[i] = iconCellSafe(s.data, s.tga, strip)
			}
		}()
	}
	for i := range keys {
		next <- i
	}
	close(next)
	wg.Wait()

	type provider struct {
		Type   string   `json:"type"`
		File   string   `json:"file"`
		Height int      `json:"height"`
		Ascent int      `json:"ascent"`
		Chars  []string `json:"chars"`
	}
	var providers []provider
	icons := map[string]rune{}
	var sheet *image.NRGBA
	var rows [][]rune
	flush := func() {
		if sheet == nil {
			return
		}
		used := sheet.SubImage(image.Rect(0, 0, sheetCols*iconCell, len(rows)*iconCell))
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, used); err == nil {
			name := "font/icons_" + strconv.Itoa(len(providers))
			b.files["assets/"+Namespace+"/textures/"+name+".png"] = buf.Bytes()
			b.res.Stats.IconBytes += buf.Len()
			p := provider{Type: "bitmap", File: Namespace + ":" + name + ".png", Height: iconCell, Ascent: iconAscent}
			for _, r := range rows {
				for len(r) < sheetCols {
					r = append(r, 0) // no glyph
				}
				p.Chars = append(p.Chars, string(r))
			}
			providers = append(providers, p)
		}
		sheet, rows = nil, nil
	}
	n, glyphs := 0, 0
	same := map[string]rune{}
	for i, key := range keys {
		if cells[i] == nil {
			continue
		}
		// Pictures that come out the same share a glyph (the frames of a bar, copies of an icon).
		if c, ok := same[string(cells[i].Pix)]; ok {
			icons[key] = c
			continue
		}
		c, ok := iconRune(n)
		if !ok {
			b.res.Stats.miss("form icons: the font is full, textures from %s on have no glyph", key)
			break
		}
		n++
		if sheet == nil {
			sheet = image.NewNRGBA(image.Rect(0, 0, sheetCols*iconCell, sheetRows*iconCell))
		}
		if len(rows) == 0 || len(rows[len(rows)-1]) == sheetCols {
			rows = append(rows, nil)
		}
		row := len(rows) - 1
		at := image.Pt(len(rows[row])*iconCell, row*iconCell)
		draw.Draw(sheet, image.Rectangle{Min: at, Max: at.Add(image.Pt(iconCell, iconCell))}, cells[i], image.Point{}, draw.Src)
		rows[row] = append(rows[row], c)
		icons[key] = c
		same[string(cells[i].Pix)] = c
		glyphs++
		if len(rows) == sheetRows && len(rows[row]) == sheetCols {
			flush()
		}
	}
	flush()
	fontFile := func(font string, providers []provider) {
		if len(providers) == 0 {
			return // a font file needs none to be no font at all
		}
		data, _ := json.Marshal(map[string]any{"providers": providers})
		b.files["assets/"+Namespace+"/font/"+strings.TrimPrefix(font, Namespace+":")+".json"] = data
	}
	fontFile(IconFont, providers)
	game := map[string][]provider{}
	for i, f := range vanillaIconFiles() {
		font, c := gameFont(i)
		game[font] = append(game[font], provider{Type: "bitmap", File: "minecraft:" + f + ".png", Height: iconCell, Ascent: iconAscent,
			Chars: []string{string(c)}})
	}
	for font, providers := range game {
		fontFile(font, providers)
	}
	b.res.FormIcons, b.res.HasIcons = icons, true
	b.res.Stats.FormIcons, b.res.Stats.IconGlyphs = len(icons), glyphs
}

// iconCellSafe is iconCellOf with a file the decoder trips over giving no glyph.
func iconCellSafe(data []byte, tga, strip bool) (cell *image.NRGBA) {
	defer func() {
		if recover() != nil {
			cell = nil
		}
	}()
	return iconCellOf(data, tga, strip)
}

// iconCellOf draws a texture as a glyph cell: shrunk to fit (a small one enlarged by whole
// steps), at the left, in the middle of the height. strip: an animation strip (frames below each
// other) gives its first frame. nil: no picture (unreadable, empty, a dot or a line).
func iconCellOf(data []byte, tga, strip bool) *image.NRGBA {
	var img image.Image
	var err error
	if tga {
		img, err = decodeTGA(data)
	} else {
		var cfg image.Config
		if cfg, err = png.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width > maxIconSource || cfg.Height > maxIconSource {
			return nil
		}
		img, err = png.Decode(bytes.NewReader(data))
	}
	if err != nil || img == nil {
		return nil
	}
	r := img.Bounds()
	if r.Dx() > maxIconSource || r.Dy() > maxIconSource || r.Dx() < 1 || r.Dy() < 1 {
		return nil
	}
	if strip && r.Dy() > r.Dx() && r.Dy()%r.Dx() == 0 {
		r.Max.Y = r.Min.Y + r.Dx()
	}
	src := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(src, src.Bounds(), img, r.Min, draw.Src)
	// A picture bigger than a cell is cut to what is drawn of it: the empty edge of a 128 pixel
	// picture would leave a few pixels of glyph.
	if r.Dx() > iconCell || r.Dy() > iconCell {
		if src = cropOpaque(src); src == nil {
			return nil
		}
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w <= 2 && h <= 2 {
		return nil
	}
	var tw, th int
	if w <= iconCell && h <= iconCell {
		k := min(iconCell/w, iconCell/h)
		tw, th = w*k, h*k
	} else if w >= h {
		tw, th = iconCell, (h*iconCell+w/2)/w
	} else {
		tw, th = (w*iconCell+h/2)/h, iconCell
	}
	if tw < 3 || th < 3 {
		return nil
	}
	cell := image.NewNRGBA(image.Rect(0, 0, iconCell, iconCell))
	oy := (iconCell - th) / 2
	seen := false
	for y := 0; y < th; y++ {
		y0, y1 := y*h/th, max((y+1)*h/th, y*h/th+1)
		for x := 0; x < tw; x++ {
			x0, x1 := x*w/tw, max((x+1)*w/tw, x*w/tw+1)
			var rr, gg, bb, aa, cnt uint32
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					p := src.Pix[src.PixOffset(sx+src.Rect.Min.X, sy+src.Rect.Min.Y):]
					a := uint32(p[3])
					rr, gg, bb, aa = rr+uint32(p[0])*a, gg+uint32(p[1])*a, bb+uint32(p[2])*a, aa+a
					cnt++
				}
			}
			if aa == 0 {
				continue
			}
			a := (aa + cnt/2) / cnt
			if a < 8 {
				continue // a trace: it would only widen the glyph
			}
			seen = true
			o := cell.PixOffset(x, y+oy)
			cell.Pix[o], cell.Pix[o+1], cell.Pix[o+2], cell.Pix[o+3] = uint8(rr/aa), uint8(gg/aa), uint8(bb/aa), uint8(a)
		}
	}
	if !seen {
		return nil
	}
	return cell
}

// cropOpaque is img cut to the pixels that are not clear; nil when all are.
func cropOpaque(img *image.NRGBA) *image.NRGBA {
	b := img.Bounds()
	box := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.Pix[img.PixOffset(x, y)+3] >= 8 {
				box.Min.X, box.Min.Y = min(box.Min.X, x), min(box.Min.Y, y)
				box.Max.X, box.Max.Y = max(box.Max.X, x+1), max(box.Max.Y, y+1)
			}
		}
	}
	if box.Min.X >= box.Max.X || box.Min.Y >= box.Max.Y {
		return nil
	}
	return img.SubImage(box).(*image.NRGBA)
}
