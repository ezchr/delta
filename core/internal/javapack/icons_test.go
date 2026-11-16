package javapack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func paintedPNG(w, h int, fill func(x, y int) color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, fill(x, y))
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// TestFormIcons: the packs' textures become glyphs of the icon font (the folders that hold no
// pictures for a screen and empty pictures do not), the game's own are glyphs that point at the
// client's files, and a path is found whatever its case and extension.
func TestFormIcons(t *testing.T) {
	red := func(x, y int) color.NRGBA { return color.NRGBA{200, 0, 0, 255} }
	// A 128 pixel picture with a clear edge: the drawn middle fills the cell.
	edge := func(x, y int) color.NRGBA {
		if x >= 32 && x < 96 && y >= 32 && y < 96 {
			return color.NRGBA{0, 0, 200, 255}
		}
		return color.NRGBA{}
	}
	z := zipOf(t, map[string][]byte{
		"manifest.json":                []byte(`{"header": {"name": "Icons", "uuid": "x"}}`),
		"textures/ui/shop/Sword.png":   paintedPNG(16, 16, red),
		"textures/ui/shop/big.png":     paintedPNG(128, 128, edge),
		"textures/ui/shop/dot.png":     paintedPNG(1, 1, red),
		"textures/ui/shop/clear.png":   paintedPNG(16, 16, func(x, y int) color.NRGBA { return color.NRGBA{} }),
		"textures/ui/shop/line.png":    paintedPNG(110, 10, red),
		"textures/icon/gem.png":        paintedPNG(8, 8, red),
		"textures/items/diamond.png":   paintedPNG(16, 16, red),
		"textures/blocks/anim.png":     paintedPNG(16, 64, red),
		"textures/entity/skin.png":     paintedPNG(64, 64, red),
		"textures/particles/spark.png": paintedPNG(16, 16, red),
		"textures/ui/shop/broken.png":  []byte("not a picture"),
	})
	src, err := ReadSource(bytes.NewReader(z), int64(len(z)), "x", "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(&Input{Packs: []*Source{src}, FormIcons: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Empty() {
		t.Fatal("a pack with form icons is worth sending")
	}
	want := []string{"textures/ui/shop/sword", "textures/ui/shop/big", "textures/icon/gem",
		"textures/items/diamond", "textures/blocks/anim"}
	if len(res.FormIcons) != len(want) {
		t.Fatalf("icons: %v", res.FormIcons)
	}
	for _, w := range want {
		if _, ok := res.FormIcons[w]; !ok {
			t.Errorf("no glyph of %s", w)
		}
	}
	// Like pictures share a glyph: the sword and the diamond are the same red square, the gem enlarged and the strip's
	// first frame too.
	seen := map[rune]string{}
	for k, c := range res.FormIcons {
		seen[c] = k
	}
	if len(seen) != 2 || res.FormIcons["textures/ui/shop/sword"] != res.FormIcons["textures/items/diamond"] ||
		res.FormIcons["textures/ui/shop/sword"] == res.FormIcons["textures/ui/shop/big"] || res.Stats.IconGlyphs != 2 {
		t.Errorf("glyphs: %v", res.FormIcons)
	}

	// Lookups: the pack's own (and its own diamond before the game's), the game's by Bedrock
	// names, nothing for what neither has.
	type glyph struct {
		font string
		c    rune
	}
	icon := func(p string) glyph {
		font, c, ok := res.Icon(p)
		if !ok {
			t.Errorf("no glyph of %s", p)
		}
		return glyph{font, c}
	}
	sword := icon("textures/ui/shop/Sword.png")
	if sword != (glyph{IconFont, res.FormIcons["textures/ui/shop/sword"]}) {
		t.Errorf("sword: %v", sword)
	}
	if d := icon("textures/items/diamond"); d != sword {
		t.Errorf("the pack's diamond: %v", d)
	}
	apple, planks, check := icon("textures/items/apple"), icon("textures/blocks/planks_oak.png"), icon("textures/ui/check")
	if apple.font == IconFont || planks.font == IconFont || check.font == IconFont {
		t.Errorf("the client's textures in the pack's font: %v %v %v", apple, planks, check)
	}
	// Nothing for what neither has, for a strip of the client's, and for a texture a 26.2 client
	// lacks (a font that names a missing file is dropped whole by the client).
	for _, p := range []string{"textures/ui/nothing_like_it", "textures/blocks/lava_still", "textures/entity/skin", "",
		"https://example.com/a.png", "textures/items/white_cushion", "textures/blocks/poplar_planks"} {
		if _, c, ok := res.Icon(p); ok {
			t.Errorf("%q has a glyph %U", p, c)
		}
	}
	for _, f := range vanillaIconFiles() {
		if newJava()[f] || tallJava()[f] {
			t.Errorf("%s has a glyph", f)
		}
	}

	// The font file: every glyph has a provider, the vanilla ones point at the client's files.
	zr, err := zip.NewReader(bytes.NewReader(res.Zip), int64(len(res.Zip)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rc)
		rc.Close()
		files[f.Name] = buf.Bytes()
	}
	// The font files: every glyph has a provider. The pack's font has its own sheets only; the
	// client's files are in fonts of their own, a few each.
	fileOf := map[glyph]string{}
	fonts := 0
	for name, data := range files {
		id, ok := strings.CutPrefix(name, "assets/delta/font/")
		if !ok {
			continue
		}
		fonts++
		id = "delta:" + strings.TrimSuffix(id, ".json")
		var font struct {
			Providers []struct {
				Type, File     string
				Height, Ascent int
				Chars          []string
			}
		}
		if err := json.Unmarshal(data, &font); err != nil {
			t.Fatal(err)
		}
		if len(font.Providers) == 0 || (id != IconFont && len(font.Providers) > gameFontSize) {
			t.Errorf("%s has %d providers", id, len(font.Providers))
		}
		for _, p := range font.Providers {
			if p.Type != "bitmap" || p.Height != 16 || p.Ascent != 12 || len(p.Chars) == 0 {
				t.Fatalf("provider %+v", p)
			}
			if own := strings.HasPrefix(p.File, "delta:"); own != (id == IconFont) {
				t.Errorf("%s names %s", id, p.File)
			}
			if _, ok := files["assets/delta/textures/"+strings.TrimPrefix(p.File, "delta:")]; id == IconFont && !ok {
				t.Errorf("%s is not in the pack", p.File)
			}
			n := len([]rune(p.Chars[0]))
			for _, row := range p.Chars {
				if len([]rune(row)) != n {
					t.Fatalf("%s: rows of different lengths", p.File)
				}
				for _, c := range row {
					if c == 0 {
						continue
					}
					if fileOf[glyph{id, c}] != "" {
						t.Errorf("%s %U twice", id, c)
					}
					fileOf[glyph{id, c}] = p.File
				}
			}
		}
	}
	if want := 1 + (len(vanillaIconFiles())+gameFontSize-1)/gameFontSize; fonts != want {
		t.Errorf("%d fonts, want %d", fonts, want)
	}
	if fileOf[apple] != "minecraft:item/apple.png" || fileOf[planks] != "minecraft:block/oak_planks.png" ||
		fileOf[check] != "minecraft:gui/sprites/icon/checkmark.png" {
		t.Errorf("vanilla glyphs: %q %q %q", fileOf[apple], fileOf[planks], fileOf[check])
	}
	sheet := fileOf[sword]
	if !strings.HasPrefix(sheet, "delta:font/icons_") {
		t.Fatalf("sword's sheet: %q", sheet)
	}
	img, err := png.Decode(bytes.NewReader(files["assets/delta/textures/"+strings.TrimPrefix(sheet, "delta:")]))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 16 {
		t.Errorf("sheet is %v", img.Bounds())
	}

	// Without the option the pack has no font and nothing has a glyph.
	off, err := Convert(&Input{Packs: []*Source{src}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := off.Icon("textures/items/apple"); ok || len(off.FormIcons) != 0 || off.HasIcons {
		t.Error("icons without the option")
	}
}

// TestIconCell: how a texture is drawn into a glyph cell.
func TestIconCell(t *testing.T) {
	red := func(x, y int) color.NRGBA { return color.NRGBA{200, 0, 0, 255} }
	count := func(c *image.NRGBA) (w, h int) {
		box := image.Rectangle{}
		for y := 0; y < 16; y++ {
			for x := 0; x < 16; x++ {
				if c.NRGBAAt(x, y).A > 0 {
					box = box.Union(image.Rect(x, y, x+1, y+1))
				}
			}
		}
		return box.Dx(), box.Dy()
	}
	for _, c := range []struct {
		w, h   int
		strip  bool
		gw, gh int
	}{
		{16, 16, false, 16, 16},
		{8, 8, false, 16, 16}, // enlarged by whole steps
		{5, 5, false, 15, 15}, // 3 x
		{128, 128, false, 16, 16},
		{64, 32, false, 16, 8},
		{16, 64, true, 16, 16}, // an animation strip: the first frame
		{16, 64, false, 4, 16},
	} {
		cell := iconCellOf(paintedPNG(c.w, c.h, red), false, c.strip)
		if cell == nil {
			t.Errorf("%dx%d: no cell", c.w, c.h)
			continue
		}
		if w, h := count(cell); w != c.gw || h != c.gh {
			t.Errorf("%dx%d: drawn %dx%d, want %dx%d", c.w, c.h, w, h, c.gw, c.gh)
		}
	}
	if iconCellOf(paintedPNG(110, 10, red), false, false) != nil {
		t.Error("a 110x10 bar would be a line one pixel high: no glyph")
	}
	if c, ok := iconRune(6400); !ok || c != 0x4E00 {
		t.Errorf("glyph 6400: %U", c)
	}
	if _, ok := iconRune(6400 + 20992 + 11172); ok {
		t.Error("a glyph past the last range")
	}
}
