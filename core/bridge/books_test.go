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

// TestBookForOlderClient: a 26.2 client gets the book's content too (the item package leaves raw
// components out for it), between the components set and the ones removed.
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
	// 26.2: the item package's stack has one component, ours two, the second the book's.
	var lib, old wire.Writer
	js.EncodeFor(&lib, jitem.For(version.V776))
	encodeStack(&old, &js, version.V776)
	if js.Removed != removed {
		t.Fatal("the stack's removed components were changed")
	}
	lr, r := wire.NewReader(lib.B), wire.NewReader(old.B)
	lr.VarInt()
	lr.VarInt()
	if n, nr := lr.VarInt(), lr.VarInt(); n != 1 || nr != 1 {
		t.Fatalf("the item package wrote %d components, %d removed", n, nr)
	}
	libRest := lr.Rest()
	c := r.VarInt()
	r.VarInt()
	if n, nr := r.VarInt(), r.VarInt(); c != 1 || n != 2 || nr != 1 {
		t.Fatalf("count %d, %d components, %d removed", c, n, nr)
	}
	var book wire.Writer
	book.VarInt(version.V776.BuiltinID("minecraft:data_component_type", "minecraft:written_book_content"))
	book.Raw(js.Raw[0].Data)
	rest := r.Rest()
	// What the item package wrote, with the book before its last byte (the removed component).
	wantRest := append(append(append([]byte(nil), libRest[:len(libRest)-1]...), book.B...), libRest[len(libRest)-1])
	if !bytes.Equal(rest, wantRest) {
		t.Fatalf("stack for 26.2:\n%x\nwant\n%x", rest, wantRest)
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
