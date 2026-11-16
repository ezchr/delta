package bridge

import (
	"testing"

	"github.com/ezchr/go-mcjava/text"
	"github.com/ezchr/go-mcjava/wire"
)

// TestClickPayload: a Java client sends custom_click_action's payload length-prefixed.
func TestClickPayload(t *testing.T) {
	pl := &wire.Writer{}
	pl.Byte(text.TagCompound)
	text.IntTag(pl, "f", 3)
	text.IntTag(pl, "b", -1)
	text.StringTag(pl, "e0", "Steve")
	pl.Byte(text.TagEnd)
	w := &wire.Writer{}
	w.VarInt(int32(len(pl.B)))
	w.Raw(pl.B)
	m, err := readClickPayload(w.B)
	if err != nil {
		t.Fatal(err)
	}
	if m["f"] != int32(3) || m["b"] != int32(-1) || m["e0"] != "Steve" {
		t.Fatalf("got %v", m)
	}
	// No payload: length 1, TagEnd.
	if m, err := readClickPayload([]byte{1, text.TagEnd}); err != nil || len(m) != 0 {
		t.Fatalf("empty payload: %v %v", m, err)
	}
}
