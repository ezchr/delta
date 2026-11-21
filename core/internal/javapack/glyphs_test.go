package javapack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
)

// TestGlyphSheets: a private use glyph sheet becomes a bitmap provider of Java's default font with
// a character for each cell that draws something; other pages are left to Java.
func TestGlyphSheets(t *testing.T) {
	sheet := func(cells ...image.Point) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, 512, 512)) // 32 pixel cells
		for _, c := range cells {
			img.Set(c.X*32+5, c.Y*32+7, color.NRGBA{R: 255, A: 255})
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		return buf.Bytes()
	}
	src := &Source{files: map[string][]byte{
		"font/glyph_e1.png": sheet(image.Pt(0, 0), image.Pt(3, 2)),
		"font/glyph_00.png": sheet(image.Pt(1, 1)), // the letters: Java's own
		"font/glyph_e2.png": sheet(),               // nothing drawn
	}}
	res, err := Convert(&Input{Packs: []*Source{src}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Glyphs != 2 {
		t.Fatalf("glyphs = %d, want 2", res.Stats.Glyphs)
	}
	files := readZip(t, res.Zip)
	var font struct {
		Providers []struct {
			Type, File     string
			Height, Ascent int
			Chars          []string
		}
	}
	if err := json.Unmarshal(files["assets/minecraft/font/default.json"], &font); err != nil {
		t.Fatal(err)
	}
	if len(font.Providers) != 1 {
		t.Fatalf("providers = %+v, want the E1 sheet only", font.Providers)
	}
	p := font.Providers[0]
	if p.Type != "bitmap" || p.File != Namespace+":font/glyph_e1.png" || p.Height != glyphHeight || p.Ascent != glyphAscent {
		t.Fatalf("provider = %+v", p)
	}
	if _, ok := files["assets/"+Namespace+"/textures/font/glyph_e1.png"]; !ok {
		t.Fatal("the sheet is not in the pack")
	}
	if len(p.Chars) != 16 {
		t.Fatalf("rows = %d", len(p.Chars))
	}
	for row, s := range p.Chars {
		r := []rune(s)
		if len(r) != 16 {
			t.Fatalf("row %d has %d characters", row, len(r))
		}
		for col, c := range r {
			want := rune(0)
			if (row == 0 && col == 0) || (row == 2 && col == 3) {
				want = rune(0xE100 | row<<4 | col)
			}
			if c != want {
				t.Fatalf("row %d col %d = %U, want %U", row, col, c, want)
			}
		}
	}
}

func TestGlyphPage(t *testing.T) {
	for name, want := range map[string]int{"font/glyph_E0.png": 0xE0, "font/glyph_f8.png": 0xF8,
		"font/glyph_00.png": -1, "font/glyph_F9.png": -1, "font/glyph_E1.tga": -1, "font/default8.png": -1} {
		got, ok := glyphPage(name)
		if !ok {
			got = -1
		}
		if got != want {
			t.Errorf("%s: %X, want %X", name, got, want)
		}
	}
}

// readZip is the files of a zip by name.
func readZip(t *testing.T, z []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = data
	}
	return out
}
