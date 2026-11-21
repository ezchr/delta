package javapack

import (
	"bytes"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"sort"
	"strconv"
	"strings"
)

// Glyph sheets: a Bedrock pack draws characters of its own from font/glyph_XX.png, a 16 x 16 grid
// of the characters U+XX00 to U+XXFF. Servers put pictures in the private use pages (E0 to F8):
// ranks before names, icons in chat and in scoreboards, the pictures of a HUD. Java has no glyph
// for those characters and draws its missing-glyph box. The converted pack gives the Java default
// font a bitmap provider for each such sheet, so the same characters draw the same pictures in
// chat, titles, name tags, signs, scoreboards and dialogs.
//
// Bedrock draws a cell of a sheet the height of a line of text whatever the sheet's resolution, so
// the providers are 8 pixels high with the letters' ascent of 7 (a cell of 32 pixels is drawn at a
// quarter). Java takes a glyph's width from its rightmost pixel, as Bedrock does. Empty cells are
// left out (character 0), so the sheet takes no character it draws nothing for. Only the private
// use pages are taken: a sheet of the letters' pages would redraw Java's own text.
//
// The providers are in font files of their own under minecraft:default (and minecraft:uniform,
// the default font of a client that forces Unicode): the client joins the
// providers of every pack's default.json, so the game's own stay. A provider that fails to load
// drops the whole font, so only sheets that decode, with a size the grid divides, are listed.

// glyphHeight and glyphAscent are how big a cell of a glyph sheet is drawn: a line of text.
const (
	glyphHeight = 8
	glyphAscent = 7
)

// glyphPages are the pages of glyph sheets taken: the private use area.
const (
	glyphFirstPage = 0xE0
	glyphLastPage  = 0xF8
)

// glyphSheets adds the packs' private use glyph sheets to Java's default font.
func (b *builder) glyphSheets() {
	type provider struct {
		Type   string   `json:"type"`
		File   string   `json:"file"`
		Height int      `json:"height"`
		Ascent int      `json:"ascent"`
		Chars  []string `json:"chars"`
	}
	// The first pack that has a page wins (packs are in priority order).
	pages := map[int][]byte{}
	for _, s := range b.in.Packs {
		for _, n := range s.Files("font/") {
			page, ok := glyphPage(n)
			if !ok || pages[page] != nil {
				continue
			}
			if data, _ := s.File(n); len(data) > 0 {
				pages[page] = data
			}
		}
	}
	order := make([]int, 0, len(pages))
	for p := range pages {
		order = append(order, p)
	}
	sort.Ints(order)
	var providers []provider
	for _, page := range order {
		img, err := png.Decode(bytes.NewReader(pages[page]))
		if err != nil {
			b.res.Stats.miss("glyph sheet glyph_%X: %v", page, err)
			continue
		}
		bounds := img.Bounds()
		w, h := bounds.Dx(), bounds.Dy()
		if w < 16 || h < 16 || w%16 != 0 || h%16 != 0 {
			b.res.Stats.miss("glyph sheet glyph_%X: %dx%d is not a 16 x 16 grid", page, w, h)
			continue
		}
		sheet := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(sheet, sheet.Bounds(), img, bounds.Min, draw.Src)
		cw, ch := w/16, h/16
		chars := make([]string, 16)
		glyphs := 0
		for row := range 16 {
			r := make([]rune, 16)
			for col := range 16 {
				if cellOpaque(sheet, col*cw, row*ch, cw, ch) {
					r[col] = rune(page<<8 | row<<4 | col)
					glyphs++
				}
			}
			chars[row] = string(r)
		}
		if glyphs == 0 {
			continue
		}
		var buf bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, sheet); err != nil {
			continue
		}
		name := "font/glyph_" + strings.ToLower(strconv.FormatInt(int64(page), 16))
		b.files["assets/"+Namespace+"/textures/"+name+".png"] = buf.Bytes()
		providers = append(providers, provider{Type: "bitmap", File: Namespace + ":" + name + ".png",
			Height: glyphHeight, Ascent: glyphAscent, Chars: chars})
		b.res.Stats.Glyphs += glyphs
	}
	if len(providers) == 0 {
		return
	}
	data, _ := json.Marshal(map[string]any{"providers": providers})
	b.files["assets/minecraft/font/default.json"] = data
	// The uniform font is the default one of a client with "Force Unicode Font" on.
	b.files["assets/minecraft/font/uniform.json"] = data
}

// glyphPage is the page of a glyph sheet's path (font/glyph_E1.png -> 0xE1), ok only for the
// private use pages.
func glyphPage(name string) (int, bool) {
	base, ok := strings.CutPrefix(strings.ToLower(name), "font/glyph_")
	if !ok {
		return 0, false
	}
	hex, ok := strings.CutSuffix(base, ".png")
	if !ok || len(hex) != 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(hex, 16, 32)
	if err != nil || n < glyphFirstPage || n > glyphLastPage {
		return 0, false
	}
	return int(n), true
}

// cellOpaque reports whether the cell at x, y of size w x h has a pixel that is not transparent.
func cellOpaque(img *image.NRGBA, x, y, w, h int) bool {
	for yy := y; yy < y+h; yy++ {
		off := img.PixOffset(x, yy)
		for i := 0; i < w; i++ {
			if img.Pix[off+i*4+3] != 0 {
				return true
			}
		}
	}
	return false
}
