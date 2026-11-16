package bridge

import (
	"testing"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/wire"
)

// TestBookContent: a Bedrock book's NBT becomes Java's book component.
func TestBookContent(t *testing.T) {
	pages := []any{map[string]any{"text": "§lHello world"}, map[string]any{"text": "page two"}}
	// A signed book: title (32 characters at most), author, generation, pages as components.
	var js jitem.Stack
	js.Reset()
	bookContent(&js, map[string]any{"pages": pages, "title": "A title that is longer than thirty-two characters", "author": "Someone", "generation": int32(1)})
	if len(js.Raw) != 1 || js.Raw[0].Type != jitem.CompWrittenBookContent {
		t.Fatalf("written book: %+v", js.Raw)
	}
	r := wire.NewReader(js.Raw[0].Data)
	title := r.String(64)
	if r.Bool() || len([]rune(title)) != bookMaxTitle || r.String(64) != "Someone" || r.VarInt() != 1 || r.VarInt() != 2 || r.Err != nil {
		t.Fatalf("written book header: title %q, err %v", title, r.Err)
	}
	if rest := string(r.Rest()); len(rest) == 0 || containsPrivateUse(rest) || rest[len(rest)-1] != 1 {
		t.Fatalf("written book pages: %q", rest)
	}
	// A book and quill: pages as strings, the pack's private glyphs dropped.
	js.Reset()
	bookContent(&js, map[string]any{"pages": pages})
	if len(js.Raw) != 1 || js.Raw[0].Type != jitem.CompWritableBookContent {
		t.Fatalf("book and quill: %+v", js.Raw)
	}
	r = wire.NewReader(js.Raw[0].Data)
	if n := r.VarInt(); n != 2 {
		t.Fatalf("%d pages", n)
	}
	if p1 := r.String(2048); p1 != "§lHello world" || r.Bool() || r.String(2048) != "page two" || r.Bool() || r.Len() != 0 {
		t.Fatalf("book and quill page %q", p1)
	}
	// No pages: no component.
	js.Reset()
	bookContent(&js, map[string]any{"display": map[string]any{}})
	if len(js.Raw) != 0 {
		t.Fatal("a component for an item that is no book")
	}
}

func containsPrivateUse(s string) bool {
	for _, r := range s {
		if privateUse(r) {
			return true
		}
	}
	return false
}
