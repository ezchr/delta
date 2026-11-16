package bridge

import (
	"bytes"
	"testing"

	"delta/core/internal/blocks"

	"github.com/sandertv/gophertunnel/minecraft/nbt"
)

// TestBlockEntityStates: Java states made from Bedrock block entities (vanilla 1.26.60 palette).
func TestBlockEntityStates(t *testing.T) {
	pal, err := blocks.NewPalette(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	w := &World{pal: pal}
	cases := []struct {
		name  string
		props map[string]any
		be    map[string]any
		want  string            // Java block
		check map[string]string // Java properties
	}{
		{"minecraft:bed", map[string]any{"direction": int32(0), "head_piece_bit": uint8(1), "occupied_bit": uint8(0)},
			map[string]any{"color": uint8(11)}, "minecraft:blue_bed", map[string]string{"part": "head"}},
		{"minecraft:bed", map[string]any{"direction": int32(0), "head_piece_bit": uint8(0), "occupied_bit": uint8(0)},
			map[string]any{"color": uint8(0)}, "minecraft:white_bed", map[string]string{"part": "foot"}},
		{"minecraft:standing_banner", map[string]any{"ground_sign_direction": int32(4)},
			map[string]any{"Base": int32(1)}, "minecraft:red_banner", map[string]string{"rotation": "4"}},
		{"minecraft:wall_banner", map[string]any{"facing_direction": int32(3)},
			map[string]any{"Base": int32(4)}, "minecraft:blue_wall_banner", nil},
		{"minecraft:skeleton_skull", map[string]any{"facing_direction": int32(1)},
			map[string]any{"Rotation": float32(-90)}, "minecraft:skeleton_skull", map[string]string{"rotation": "12"}},
		{"minecraft:zombie_head", map[string]any{"facing_direction": int32(2)},
			map[string]any{"Rotation": float32(0)}, "minecraft:zombie_wall_head", map[string]string{"facing": "north"}},
		{"minecraft:red_shulker_box", map[string]any{},
			map[string]any{"facing": uint8(0)}, "minecraft:red_shulker_box", map[string]string{"facing": "down"}},
		{"minecraft:flower_pot", map[string]any{"update_bit": uint8(0)},
			map[string]any{"PlantBlock": map[string]any{"name": "minecraft:poppy", "states": map[string]any{}}}, "minecraft:potted_poppy", nil},
		{"minecraft:flower_pot", map[string]any{"update_bit": uint8(0)},
			map[string]any{"PlantBlock": map[string]any{"name": "minecraft:oak_sapling", "states": map[string]any{"age_bit": uint8(0)}}}, "minecraft:potted_oak_sapling", nil},
		{"minecraft:lectern", map[string]any{"minecraft:cardinal_direction": "north", "powered_bit": uint8(0)},
			map[string]any{"hasBook": uint8(1)}, "minecraft:lectern", map[string]string{"has_book": "true"}},
		{"minecraft:jukebox", map[string]any{},
			map[string]any{"RecordItem": map[string]any{"Name": "minecraft:music_disc_cat", "Count": uint8(1)}}, "minecraft:jukebox", map[string]string{"has_record": "true"}},
		{"minecraft:noteblock", map[string]any{},
			map[string]any{"note": uint8(7)}, "minecraft:note_block", map[string]string{"note": "7"}},
	}
	for _, c := range cases {
		idx, ok := pal.Lookup(c.name, c.props)
		if !ok {
			t.Errorf("%s %v: not in the palette", c.name, c.props)
			continue
		}
		if !pal.BlockEntityState(idx) {
			t.Errorf("%s: not marked as taking its state from the block entity", c.name)
		}
		s, _ := w.beJavaState(pal.Java(idx, false), idx, c.be)
		jb := blocks.JavaBlockOf(s)
		if jb == nil || jb.Name != c.want {
			t.Errorf("%s %v: Java %v, want %s", c.name, c.be, jb, c.want)
			continue
		}
		for k, v := range c.check {
			if got, _ := jb.Get(s, k); got != v {
				t.Errorf("%s %v: %s=%s, want %s", c.name, c.be, k, got, v)
			}
		}
	}
}

// decodeJavaNBT decodes network NBT (no root name) to a map.
func decodeJavaNBT(t *testing.T, b []byte) map[string]any {
	t.Helper()
	if len(b) == 0 || b[0] != 10 {
		t.Fatalf("not a compound: %x", b)
	}
	named := append([]byte{10, 0, 0}, b[1:]...)
	var m map[string]any
	if err := nbt.NewDecoderWithEncoding(bytes.NewReader(named), nbt.BigEndian).Decode(&m); err != nil {
		t.Fatalf("decode %x: %v", b, err)
	}
	return m
}

func TestBlockEntityNBT(t *testing.T) {
	p := &Player{}
	banner := decodeJavaNBT(t, p.beNBT("minecraft:banner", map[string]any{"Base": int32(15),
		"Patterns": []any{map[string]any{"Color": int32(1), "Pattern": "bo"}, map[string]any{"Color": int32(15), "Pattern": "cre"}}}))
	pats, _ := banner["patterns"].([]any)
	if len(pats) != 2 {
		t.Fatalf("banner: %v", banner)
	}
	if l := pats[0].(map[string]any); l["pattern"] != "minecraft:border" || l["color"] != "red" {
		t.Fatalf("banner layer 0: %v", l)
	}
	if l := pats[1].(map[string]any); l["pattern"] != "minecraft:creeper" || l["color"] != "white" {
		t.Fatalf("banner layer 1: %v", l)
	}
	if om := decodeJavaNBT(t, p.beNBT("minecraft:banner", map[string]any{"Type": int32(1)})); len(om["patterns"].([]any)) != 8 {
		t.Fatalf("ominous banner: %v", om)
	}
	if p.beNBT("minecraft:banner", map[string]any{"Base": int32(0)}) != nil {
		t.Fatal("plain banner has data")
	}
	camp := decodeJavaNBT(t, p.beNBT("minecraft:campfire", map[string]any{
		"Item2": map[string]any{"Name": "minecraft:beef", "Count": uint8(1), "Damage": int16(0)}}))
	items, _ := camp["Items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "minecraft:beef" || items[0].(map[string]any)["Slot"] != uint8(1) {
		t.Fatalf("campfire: %v", camp)
	}
	pot := decodeJavaNBT(t, p.beNBT("minecraft:decorated_pot", map[string]any{
		"sherds": []any{"minecraft:brick", "minecraft:angler_pottery_sherd", "minecraft:brick", "minecraft:skull_pottery_sherd"}}))
	if sh, _ := pot["sherds"].([]any); len(sh) != 4 || sh[1] != "minecraft:angler_pottery_sherd" {
		t.Fatalf("pot: %v", pot)
	}
	sp := decodeJavaNBT(t, p.beNBT("minecraft:mob_spawner", map[string]any{"EntityIdentifier": "minecraft:zombie", "Delay": int16(20)}))
	if e := sp["SpawnData"].(map[string]any)["entity"].(map[string]any); e["id"] != "minecraft:zombie" {
		t.Fatalf("spawner: %v", sp)
	}
	gw := decodeJavaNBT(t, p.beNBT("minecraft:end_gateway", map[string]any{"Age": int32(300)}))
	if gw["Age"] != int64(300) {
		t.Fatalf("gateway: %v", gw)
	}
	br := decodeJavaNBT(t, p.beNBT("minecraft:brushable_block", map[string]any{
		"item": map[string]any{"Name": "minecraft:emerald", "Count": uint8(1)}, "brush_direction": uint8(1)}))
	if br["item"].(map[string]any)["id"] != "minecraft:emerald" || br["hit_direction"] != uint8(1) {
		t.Fatalf("brushable: %v", br)
	}
	sh := decodeJavaNBT(t, p.beNBT("minecraft:shelf", map[string]any{"Items": []any{
		map[string]any{"Name": "", "Count": uint8(0)}, map[string]any{"Name": "minecraft:apple", "Count": uint8(3)}}}))
	if it := sh["Items"].([]any); len(it) != 1 || it[0].(map[string]any)["count"] != int32(3) {
		t.Fatalf("shelf: %v", sh)
	}
}
