package bridge

import (
	"fmt"
	"strings"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

// Books. Bedrock keeps a book's text in the item's NBT: pages (a list of {text}), and for a
// signed book title, author and generation. Java has it in a component: written_book_content
// (title, author, generation, pages as text components, resolved) or writable_book_content
// (pages as plain strings) for a book and quill.
//
// A Bedrock client opens a book by itself. A Java client opens a book and quill by itself too,
// but for a written book it waits for the server's open_book, so a use of a written book in
// the main hand is answered with that and not passed on (the server has nothing to do for it).
//
// Not done: writing from Java. A page a Java player edits or a book they sign (edit_book) is
// not sent to the server (Bedrock's BookEdit), so the server keeps the old text.

// Limits of Java's book codecs.
const (
	bookMaxPages = 100
	bookMaxTitle = 32
	bookMaxPage  = 1024 // a book and quill's page, as a string
)

// bookContent gives a Java item the book component for a Bedrock book's NBT.
func bookContent(js *jitem.Stack, nbt map[string]any) {
	pages, ok := nbt["pages"].([]any)
	if !ok {
		if raw, has := nbt["pages"]; has && cfg.Log != nil {
			cfg.Log.Debug("book pages of an unexpected type", "type", fmt.Sprintf("%T", raw))
		}
		return
	}
	if len(pages) > bookMaxPages {
		pages = pages[:bookMaxPages]
	}
	texts := make([]string, 0, len(pages))
	for _, pg := range pages {
		m, _ := pg.(map[string]any)
		t, _ := m["text"].(string)
		texts = append(texts, bookText(t))
	}
	w := &wire.Writer{}
	title, written := nbt["title"].(string)
	if !written {
		w.VarInt(int32(len(texts)))
		for _, t := range texts {
			w.String(clipRunes(t, bookMaxPage))
			w.Bool(false) // no filtered text
		}
		js.Raw = append(js.Raw, jitem.RawComponent{Type: jitem.CompWritableBookContent, Data: w.B})
		return
	}
	author, _ := nbt["author"].(string)
	gen := int32(0)
	switch v := nbt["generation"].(type) {
	case int32:
		gen = v
	case uint8:
		gen = int32(v)
	}
	w.String(clipRunes(bookText(title), bookMaxTitle))
	w.Bool(false)
	w.String(bookText(author))
	w.VarInt(max(0, min(3, gen)))
	w.VarInt(int32(len(texts)))
	for _, t := range texts {
		c := text.Legacy(t, text.Bedrock)
		c.Write(w)
		w.Bool(false)
	}
	w.Bool(true) // resolved: the pages are final text
	js.Raw = append(js.Raw, jitem.RawComponent{Type: jitem.CompWrittenBookContent, Data: w.B})
}

// bookText is a book's text without the private-use characters Bedrock packs draw as glyphs
// of their own font (U+E000 to U+F8FF): the converted pack has no font for them, and Java
// would draw a box for each.
func bookText(s string) string {
	if !strings.ContainsFunc(s, privateUse) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if privateUse(r) {
			return -1
		}
		return r
	}, s)
}

func privateUse(r rune) bool { return r >= 0xE000 && r <= 0xF8FF }

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// openHeldBook opens the written book in the player's main hand on the Java client. It
// reports whether there was one.
func (p *Player) openHeldBook() bool {
	if strings.TrimPrefix(p.itemName(p.HeldItem().Stack.NetworkID), "minecraft:") != "written_book" {
		return false
	}
	w := p.pk()
	w.VarInt(0) // the main hand
	p.send(v777.ClientboundPlayOpenBook, w)
	return true
}
