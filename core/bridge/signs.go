package bridge

import (
	"strings"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

// Sign text: Bedrock keeps it in the sign's block entity (FrontText/BackText: Text with lines
// split by \n, SignTextColor ARGB, IgnoreLighting for glowing text; IsWaxed), Java draws it from
// the block_entity_data of the sign (front_text/back_text: four messages, a dye colour name,
// has_glowing_text; is_waxed). Sent with each column and on every BlockActorData of a sign.

// signColours are the Bedrock sign text colours of the dyes (RGB) by Java dye name.
var signColours = []struct {
	name string
	rgb  uint32
}{
	{"black", 0x000000}, {"black", 0x1D1D21}, {"white", 0xF0F0F0}, {"orange", 0xF9801D},
	{"magenta", 0xC74EBD}, {"light_blue", 0x3AB3DA}, {"yellow", 0xFED83D}, {"lime", 0x80C71F},
	{"pink", 0xF38BAA}, {"gray", 0x474F52}, {"light_gray", 0x9D9D97}, {"cyan", 0x169C9C},
	{"purple", 0x8932B8}, {"blue", 0x3C44AA}, {"brown", 0x835432}, {"green", 0x5E7C16},
	{"red", 0xB02E26},
}

// signColour is the Java dye name nearest to a Bedrock ARGB text colour.
func signColour(argb int64) string {
	c := uint32(argb) & 0xFFFFFF
	best, bestD := "black", int64(-1)
	for _, sc := range signColours {
		dr := int64(c>>16&0xFF) - int64(sc.rgb>>16&0xFF)
		dg := int64(c>>8&0xFF) - int64(sc.rgb>>8&0xFF)
		db := int64(c&0xFF) - int64(sc.rgb&0xFF)
		if d := dr*dr + dg*dg + db*db; bestD < 0 || d < bestD {
			best, bestD = sc.name, d
		}
	}
	return best
}

func nbtInt64(v any) (int64, bool) {
	switch v := v.(type) {
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint32:
		return int64(v), true
	}
	return 0, false
}

func nbtBool(v any) bool {
	switch v := v.(type) {
	case uint8:
		return v != 0
	case int8:
		return v != 0
	case bool:
		return v
	}
	return false
}

// writeSignSide writes one side's compound entries (front_text / back_text).
func writeSignSide(w *wire.Writer, name string, side map[string]any) {
	text.Key(w, text.TagCompound, name)
	txt, _ := side["Text"].(string)
	lines := strings.SplitN(txt, "\n", 4)
	text.ListStart(w, "messages", text.TagCompound, 4)
	for i := 0; i < 4; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		c := text.Legacy(line, text.Bedrock)
		b := c.Bytes()
		if len(b) > 0 && b[0] == text.TagCompound {
			w.Raw(b[1:])
		} else {
			text.StringTag(w, "text", c.Text)
			w.Byte(text.TagEnd)
		}
	}
	colour := "black"
	if v, ok := nbtInt64(side["SignTextColor"]); ok {
		colour = signColour(v)
	}
	text.StringTag(w, "color", colour)
	text.BoolTag(w, "has_glowing_text", nbtBool(side["IgnoreLighting"]))
	w.Byte(text.TagEnd)
}

// sendSignText sends the text of the sign at pos (block entity data be) if the block there is a
// sign on Java.
func (p *Player) sendSignText(col *column, pos cube.Pos, be map[string]any) {
	w := p.world
	if w == nil || be == nil || pos.OutOfBounds(w.r) {
		return
	}
	idx := col.c.Block(uint8(pos[0]&15), int16(pos[1]), uint8(pos[2]&15), 0)
	typ := w.pal.BlockEntity(idx)
	if typ != "minecraft:sign" && typ != "minecraft:hanging_sign" {
		return
	}
	id := p.ver().BuiltinID("minecraft:block_entity_type", typ)
	if id < 0 {
		return
	}
	front, _ := be["FrontText"].(map[string]any)
	back, _ := be["BackText"].(map[string]any)
	if front == nil {
		// Old format: one side, Text and SignTextColor at the top level.
		front = be
	}
	wr := p.pk()
	wr.Position(pos[0], pos[1], pos[2])
	wr.VarInt(id)
	wr.Byte(text.TagCompound)
	writeSignSide(wr, "front_text", front)
	if back == nil {
		back = map[string]any{}
	}
	writeSignSide(wr, "back_text", back)
	text.BoolTag(wr, "is_waxed", nbtBool(be["IsWaxed"]))
	wr.Byte(text.TagEnd)
	p.send(v777.ClientboundPlayBlockEntityData, wr)
}

func init() {
	chunkSentHooks = append(chunkSentHooks, func(p *Player, _ ChunkPos, col *column) {
		for pos, be := range col.be {
			p.sendSignText(col, pos, be)
		}
	})
	blockEntityHooks = append(blockEntityHooks, func(p *Player, pos cube.Pos, data map[string]any) {
		if p.world == nil {
			return
		}
		if col := p.world.cols[chunkOf(pos)]; col != nil && col.sent {
			p.sendSignText(col, pos, data)
		}
	})
}
