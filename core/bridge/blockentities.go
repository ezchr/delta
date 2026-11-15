package bridge

import (
	"math"
	"strconv"
	"strings"

	"javabridge/core/internal/blocks"
	"javabridge/core/internal/javamap"

	"github.com/df-mc/dragonfly/server/block/cube"
	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	"github.com/df-mc/dragonfly/server/java/protocol/text"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
)

// Block entities other than signs (signs.go) and chests (chests.go). Bedrock keeps in a block's
// block entity what Java keeps in two places:
//
//   - the block state (blocks.BlockEntityState): a bed's colour (color: Java dye id), a banner's
//     base colour (Base: 15 - dye id), a floor skull's rotation (Rotation: degrees), a shulker
//     box's facing (facing: 3D direction), a flower pot's plant (PlantBlock: a block state ->
//     potted_<plant>), a lectern's book (hasBook), a jukebox's record (RecordItem), a note
//     block's pitch (note), a piston's extension (State 1/2 = extending/extended). The state is
//     changed in each column (beStateFor) and redrawn on every BlockActorData.
//   - the Java block entity's data (beNBT), in the column's block entity list and as
//     block_entity_data on every BlockActorData: banner patterns (Patterns: Color + short code;
//     Type 1 = ominous), decorated pot sherds, campfire items (Item1..4), the spawner's mob
//     (EntityIdentifier -> SpawnData.entity.id), a suspicious block's item and brush side, an end
//     gateway's age, shelf items (a dense Items list). Skulls carry no profile on Bedrock (player
//     heads are Steve's), bells, conduits, beacons, end portals, lecterns, jukeboxes and chiseled
//     bookshelves only need the block entity to exist.
//
// Bells: BlockActorData with Ringing set rings the Java bell (block_event 1, the side hit).

// javaDyes are the Java dye names by dye id (DyeColor order).
var javaDyes = [16]string{"white", "orange", "magenta", "light_blue", "yellow", "lime", "pink", "gray",
	"light_gray", "cyan", "purple", "blue", "brown", "green", "red", "black"}

// directions3D are the Java Direction names by 3D data value (Bedrock's facing numbering too).
var directions3D = [6]string{"down", "up", "north", "south", "west", "east"}

// beStateFor is the Java state (client numbering) of the block idx at pos with block entity be,
// state being the state the palette gives it.
func (p *Player) beStateFor(state, idx uint32, wet bool, pos cube.Pos, be map[string]any) uint32 {
	w := p.world
	if be == nil {
		return state
	}
	if isChestName(w.pal.Name(idx)) {
		return w.chestState(state, idx, pos, be)
	}
	if !w.pal.BlockEntityState(idx) {
		return state
	}
	s, ok := w.beJavaState(w.pal.Java(idx, wet), idx, be)
	if !ok {
		return state
	}
	if v := p.ver(); v != nil && !v.Native() {
		return uint32(v.BlockState(int32(s)))
	}
	return s
}

// beJavaState is the Java 26.3 state s of palette index idx changed by its block entity be.
func (w *World) beJavaState(s, idx uint32, be map[string]any) (uint32, bool) {
	jb := blocks.JavaBlockOf(s)
	if jb == nil {
		return s, false
	}
	name := strings.TrimPrefix(w.pal.Name(idx), "minecraft:")
	switch {
	case name == "bed":
		c, ok := nbtNum(be["color"])
		if !ok || c < 0 || c > 15 {
			return s, false
		}
		return blocks.JavaSwap(s, "minecraft:"+javaDyes[c]+"_bed")
	case name == "standing_banner" || name == "wall_banner":
		c, ok := nbtNum(be["Base"])
		if !ok || c < 0 || c > 15 {
			c = 15 // white
		}
		suffix := "_banner"
		if strings.HasSuffix(jb.Name, "_wall_banner") {
			suffix = "_wall_banner"
		}
		return blocks.JavaSwap(s, "minecraft:"+javaDyes[15-c]+suffix)
	case strings.HasSuffix(name, "shulker_box"):
		f, ok := nbtNum(be["facing"])
		if !ok || f < 0 || f > 5 {
			return s, false
		}
		return jb.With(s, "facing", directions3D[f])
	case name == "flower_pot":
		return w.pottedState(s, be)
	case name == "lectern":
		return jb.With(s, "has_book", strconv.FormatBool(nbtBool(be["hasBook"])))
	case name == "jukebox":
		rec, _ := be["RecordItem"].(map[string]any)
		n, _ := rec["Name"].(string)
		return jb.With(s, "has_record", strconv.FormatBool(n != "" && n != "minecraft:air"))
	case name == "noteblock":
		n, ok := nbtNum(be["note"])
		if !ok || n < 0 || n > 24 {
			return s, false
		}
		return jb.With(s, "note", strconv.Itoa(int(n)))
	case name == "piston" || name == "sticky_piston":
		st, _ := nbtNum(be["State"])
		return jb.With(s, "extended", strconv.FormatBool(st == 1 || st == 2))
	default: // skulls and heads
		if !jb.Has("rotation") {
			return s, false // a wall skull: the facing is in the Bedrock state
		}
		r, ok := be["Rotation"].(float32)
		if !ok {
			return s, false
		}
		rot := int(math.Round(float64(r)/22.5)) % 16
		if rot < 0 {
			rot += 16
		}
		return jb.With(s, "rotation", strconv.Itoa(rot))
	}
}

// pottedState is the flower pot holding the plant of a Bedrock flower pot's PlantBlock.
func (w *World) pottedState(s uint32, be map[string]any) (uint32, bool) {
	plant, _ := be["PlantBlock"].(map[string]any)
	name, _ := plant["name"].(string)
	if name == "" || name == "minecraft:air" {
		return s, false
	}
	states, _ := plant["states"].(map[string]any)
	if states == nil {
		states = map[string]any{}
	}
	i, ok := w.pal.Lookup(name, states)
	if !ok {
		// Not this exact state (an old version's): the name's first state.
		for j, st := range w.pal.States {
			if st.Name == name {
				i, ok = uint32(j), true
				break
			}
		}
	}
	if !ok {
		return s, false
	}
	pb := blocks.JavaBlockOf(w.pal.Java(i, false))
	if pb == nil {
		return s, false
	}
	pot := blocks.JavaBlockNamed("minecraft:potted_" + strings.TrimPrefix(pb.Name, "minecraft:"))
	if pot == nil {
		return s, false
	}
	return pot.Default, true
}

// beNBT is the network NBT (0x0a and the payload) of the Java block entity of type typ made from
// the Bedrock block entity be, or nil when the Java one needs no data.
func (p *Player) beNBT(typ string, be map[string]any) []byte {
	if be == nil {
		return nil
	}
	var w wire.Writer
	w.Byte(text.TagCompound)
	switch typ {
	case "minecraft:banner":
		if !writeBannerPatterns(&w, be) {
			return nil
		}
	case "minecraft:decorated_pot":
		sherds, _ := be["sherds"].([]any)
		var names []string
		for _, sh := range sherds {
			n, _ := sh.(string)
			names = append(names, javaItemName(n, 0, "minecraft:brick"))
		}
		if len(names) == 0 || len(names) > 4 {
			return nil
		}
		text.ListStart(&w, "sherds", text.TagString, len(names))
		for _, n := range names {
			text.String(&w, n)
		}
	case "minecraft:campfire":
		var items []javaSlotItem
		for i := 0; i < 4; i++ {
			if it, ok := javaNBTItem(be["Item"+strconv.Itoa(i+1)]); ok {
				it.slot = i
				items = append(items, it)
			}
		}
		writeSlotItems(&w, "Items", items)
	case "minecraft:shelf":
		list, _ := be["Items"].([]any)
		var items []javaSlotItem
		for i, v := range list {
			if it, ok := javaNBTItem(v); ok && i < 3 {
				it.slot = i
				items = append(items, it)
			}
		}
		writeSlotItems(&w, "Items", items)
	case "minecraft:mob_spawner":
		id, _ := be["EntityIdentifier"].(string)
		t, ok := entityTypes[id]
		if !ok || t.java == "" {
			return nil
		}
		text.Key(&w, text.TagCompound, "SpawnData")
		text.Key(&w, text.TagCompound, "entity")
		text.StringTag(&w, "id", t.java)
		w.Byte(text.TagEnd)
		w.Byte(text.TagEnd)
		if d, ok := nbtNum(be["Delay"]); ok {
			text.Key(&w, text.TagShort, "Delay")
			w.Int16(int16(d))
		}
	case "minecraft:brushable_block":
		it, ok := javaNBTItem(be["item"])
		if !ok {
			return nil
		}
		text.Key(&w, text.TagCompound, "item")
		writeItemFields(&w, it)
		w.Byte(text.TagEnd)
		if d, ok := nbtNum(be["brush_direction"]); ok && d >= 0 && d <= 5 {
			text.ByteTag(&w, "hit_direction", byte(d))
		}
	case cmdBlockType: // cmdblock.go
		writeCommandBlock(&w, be)
	case "minecraft:end_gateway":
		age, ok := nbtNum(be["Age"])
		if !ok {
			return nil
		}
		text.Key(&w, text.TagLong, "Age")
		w.Int64(age)
	default:
		return nil
	}
	w.Byte(text.TagEnd)
	return w.B
}

// ominousBanner is the ominous banner's pattern list (Bedrock sends Type 1 and no patterns).
var ominousBanner = []struct{ pattern, colour string }{
	{"rhombus", "cyan"}, {"stripe_bottom", "light_gray"}, {"stripe_center", "gray"}, {"border", "light_gray"},
	{"stripe_middle", "black"}, {"half_horizontal", "light_gray"}, {"circle", "light_gray"}, {"border", "black"},
}

// writeBannerPatterns writes a banner's patterns list (false: it has none).
func writeBannerPatterns(w *wire.Writer, be map[string]any) bool {
	type layer struct{ pattern, colour string }
	var layers []layer
	if t, _ := nbtNum(be["Type"]); t == 1 {
		for _, l := range ominousBanner {
			layers = append(layers, layer{l.pattern, l.colour})
		}
	} else {
		list, _ := be["Patterns"].([]any)
		for _, v := range list {
			m, _ := v.(map[string]any)
			code, _ := m["Pattern"].(string)
			c, ok := nbtNum(m["Color"])
			name := javaBannerPatterns[code]
			if name == "" || !ok || c < 0 || c > 15 {
				continue
			}
			layers = append(layers, layer{name, javaDyes[15-c]})
		}
	}
	if len(layers) == 0 {
		return false
	}
	text.ListStart(w, "patterns", text.TagCompound, len(layers))
	for _, l := range layers {
		text.StringTag(w, "pattern", "minecraft:"+l.pattern)
		text.StringTag(w, "color", l.colour)
		w.Byte(text.TagEnd)
	}
	return true
}

// javaSlotItem is an item of a Java block entity's item list.
type javaSlotItem struct {
	slot  int
	id    string
	count int32
}

// javaNBTItem converts a Bedrock item compound (Name, Damage, Count) to a Java item.
func javaNBTItem(v any) (javaSlotItem, bool) {
	m, _ := v.(map[string]any)
	name, _ := m["Name"].(string)
	if name == "" || name == "minecraft:air" {
		return javaSlotItem{}, false
	}
	count, ok := nbtNum(m["Count"])
	if !ok || count <= 0 {
		count = 1
	}
	dmg, _ := nbtNum(m["Damage"])
	id := javaItemName(name, int16(dmg), "")
	if id == "" {
		return javaSlotItem{}, false
	}
	return javaSlotItem{id: id, count: int32(count)}, true
}

// javaItemName is the Java item name of a Bedrock item (def when it has none).
func javaItemName(name string, meta int16, def string) string {
	if !strings.Contains(name, ":") {
		name = "minecraft:" + name
	}
	if id, ok := javamap.ItemID(name, meta); ok {
		if n := jitem.Name(id); n != "" && n != "minecraft:air" {
			return n
		}
	}
	return def
}

func writeItemFields(w *wire.Writer, it javaSlotItem) {
	text.StringTag(w, "id", it.id)
	text.IntTag(w, "count", it.count)
}

// writeSlotItems writes an Items list ({Slot, id, count}), as ContainerHelper.saveAllItems.
func writeSlotItems(w *wire.Writer, name string, items []javaSlotItem) {
	text.ListStart(w, name, text.TagCompound, len(items))
	for _, it := range items {
		text.ByteTag(w, "Slot", byte(it.slot))
		writeItemFields(w, it)
		w.Byte(text.TagEnd)
	}
}

// sendBlockEntityData sends a Java block entity's data (block_entity_data).
func (p *Player) sendBlockEntityData(pos cube.Pos, typ string, data []byte) {
	id := p.ver().BuiltinID("minecraft:block_entity_type", typ)
	if id < 0 || data == nil {
		return
	}
	wr := p.pk()
	wr.Position(pos[0], pos[1], pos[2])
	wr.VarInt(id)
	wr.Raw(data)
	p.send(v777.ClientboundPlayBlockEntityData, wr)
}

// bellDirections are the Java 3D direction ids of Bedrock's bell Direction (2D: south, west,
// north, east).
var bellDirections = [4]byte{3, 4, 2, 5}

// ringBell rings the Java bell at pos (block_event 1 with the side hit).
func (p *Player) ringBell(pos cube.Pos, be map[string]any) {
	d, _ := nbtNum(be["Direction"])
	id := p.ver().BuiltinID("minecraft:block", "minecraft:bell")
	if id < 0 || d < 0 || d > 3 {
		return
	}
	wr := p.pk()
	wr.Position(pos[0], pos[1], pos[2])
	wr.Byte(1)
	wr.Byte(bellDirections[d])
	wr.VarInt(id)
	p.send(v777.ClientboundPlayBlockEvent, wr)
}

func init() {
	blockEntityHooks = append(blockEntityHooks, func(p *Player, pos cube.Pos, data map[string]any) {
		w := p.world
		if w == nil || pos.OutOfBounds(w.r) {
			return
		}
		col := w.cols[chunkOf(pos)]
		if col == nil || !col.sent {
			return
		}
		idx := col.c.Block(uint8(pos[0]&15), int16(pos[1]), uint8(pos[2]&15), 0)
		p.log.Debug("block entity", "pos", pos, "block", w.pal.Name(idx), "data", data)
		if w.pal.BlockEntityState(idx) {
			p.sendBlockUpdateRaw(pos[0], pos[1], pos[2], p.javaStateAt(col, pos))
		}
		typ := w.pal.BlockEntity(idx)
		switch {
		case typ == "minecraft:bell":
			if nbtBool(data["Ringing"]) {
				p.ringBell(pos, data)
			}
		case typ != "":
			p.sendBlockEntityData(pos, typ, p.beNBT(typ, data))
		}
	})
}
