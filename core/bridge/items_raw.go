package bridge

import (
	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
)

// Components the bridge writes itself (Stack.Raw) reach a client of the newest version as they
// are. For an older one the item package leaves out every raw component whose wire form it
// does not know, since it could not tell whether that version reads it the same way; a book's
// content is one of those, so a 26.2 client got written books without their text.
//
// sameWireRaw are the raw components known to be written the same in every version the bridge
// speaks; encodeStack puts them back into an older version's stack.
var sameWireRaw = map[int32]string{
	jitem.CompWrittenBookContent:  "minecraft:written_book_content",
	jitem.CompWritableBookContent: "minecraft:writable_book_content",
}

// encodeStack writes a Java stack for a client of version v.
func encodeStack(w *wire.Writer, js *jitem.Stack, v *version.Version) {
	pr := jitem.For(v)
	if pr == nil || len(js.Raw) == 0 || js.Count <= 0 {
		js.EncodeFor(w, pr)
		return
	}
	type extra struct {
		id   int32
		data []byte
	}
	var extras []extra
	for i := range js.Raw {
		if name, ok := sameWireRaw[js.Raw[i].Type]; ok {
			if id := v.BuiltinID("minecraft:data_component_type", name); id >= 0 {
				extras = append(extras, extra{id, js.Raw[i].Data})
			}
		}
	}
	if len(extras) == 0 {
		js.EncodeFor(w, pr)
		return
	}
	// The stack is count, item, the numbers of components set and removed, the ones set, the
	// ones removed. Written a second time with none removed, it ends where the ones set end,
	// which is where the extra ones go.
	var full, set wire.Writer
	js.EncodeFor(&full, pr)
	removed := js.Removed
	js.Removed = jitem.Set{}
	js.EncodeFor(&set, pr)
	js.Removed = removed
	fr, sr := wire.NewReader(full.B), wire.NewReader(set.B)
	count, id, nSet, nRemoved := fr.VarInt(), fr.VarInt(), fr.VarInt(), fr.VarInt()
	sr.VarInt()
	sr.VarInt()
	sr.VarInt()
	sr.VarInt()
	body := sr.Rest()
	if fr.Err != nil || sr.Err != nil || count <= 0 || fr.Len() < len(body) {
		w.Raw(full.B) // an item this version does not have (written as empty)
		return
	}
	rest := fr.Rest()
	w.VarInt(count)
	w.VarInt(id)
	w.VarInt(nSet + int32(len(extras)))
	w.VarInt(nRemoved)
	w.Raw(body)
	for _, e := range extras {
		w.VarInt(e.id)
		w.Raw(e.data)
	}
	w.Raw(rest[len(body):])
}
