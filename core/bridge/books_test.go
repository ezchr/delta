package bridge

import (
	"bytes"
	"testing"

	"github.com/ezchr/go-mcjava/version"

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

// TestBookForOlderClient: a 26.2 client gets the book's content too, once: the item package
// writes a raw component in that client's form (go-mcjava after v0.1.2).
func TestBookForOlderClient(t *testing.T) {
	var js jitem.Stack
	js.Reset()
	js.ID, js.Count = mustJavaItem("minecraft:written_book"), 1
	js.CustomName = jitem.Text{Text: "README"}
	js.Add(jitem.CompCustomName)
	js.Remove(jitem.CompRepairCost)
	bookContent(&js, map[string]any{"pages": []any{map[string]any{"text": "page one"}}, "title": "README", "author": "Someone"})
	removed := js.Removed

	// The newest version: as the item package writes it.
	var want, got wire.Writer
	js.EncodeFor(&want, nil)
	encodeStack(&got, &js, version.Newest)
	if !bytes.Equal(want.B, got.B) {
		t.Fatal("the newest version's stack was changed")
	}
	// 26.2: two components, the name and the book's content (the same bytes in both versions,
	// under 26.2's id), and the one removed.
	var old wire.Writer
	encodeStack(&old, &js, version.V776)
	if js.Removed != removed {
		t.Fatal("the stack's removed components were changed")
	}
	r := wire.NewReader(old.B)
	c := r.VarInt()
	r.VarInt()
	if n, nr := r.VarInt(), r.VarInt(); c != 1 || n != 2 || nr != 1 {
		t.Fatalf("count %d, %d components, %d removed", c, n, nr)
	}
	var book wire.Writer
	book.VarInt(version.V776.BuiltinID("minecraft:data_component_type", "minecraft:written_book_content"))
	book.Raw(js.Raw[0].Data)
	if rest := r.Rest(); bytes.Count(rest, book.B) != 1 {
		t.Fatalf("stack for 26.2 has the book's content %d times:\n%x", bytes.Count(rest, book.B), rest)
	}
	// And it reads back as the stack it was.
	var back jitem.Stack
	br := wire.NewReader(old.B)
	back.DecodeFor(br, jitem.For(version.V776))
	if br.Err != nil || br.Len() != 0 || len(back.Raw) != 1 || back.Raw[0].Type != jitem.CompWrittenBookContent || !bytes.Equal(back.Raw[0].Data, js.Raw[0].Data) {
		t.Fatalf("26.2 stack read back: %v, raw %+v", br.Err, back.Raw)
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
