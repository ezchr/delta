package bridge

import (
	"fmt"
	"strings"
	"testing"

	"javabridge/core/internal/blocks"

	"github.com/df-mc/dragonfly/server/block/cube"
	jchunk "github.com/df-mc/dragonfly/server/java/protocol/chunk"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/version"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// vineIdx is the palette index of a Bedrock vine block with its *_age property at age.
func vineIdx(t *testing.T, pal *blocks.Palette, name string, age int) uint32 {
	t.Helper()
	for i, s := range pal.States {
		if s.Name != name {
			continue
		}
		for k, v := range s.Properties {
			if strings.HasSuffix(k, "_age") && fmt.Sprint(v) == fmt.Sprint(age) {
				return uint32(i)
			}
		}
	}
	t.Fatalf("no %s age %d", name, age)
	return 0
}

// blockIdx is the palette index of a Bedrock block in its default state.
func blockIdx(t *testing.T, pal *blocks.Palette, name string) uint32 {
	t.Helper()
	props, ok := blocks.Registry{P: pal}.DefaultProperties(name)
	if !ok {
		t.Fatalf("no %s", name)
	}
	i, ok := pal.Lookup(name, props)
	if !ok {
		t.Fatalf("no %s %v", name, props)
	}
	return i
}

// javaDesc describes a Java 26.3 state as "name age=.. berries=.." (the properties vines have).
func javaDesc(state uint32) string {
	b := blocks.JavaBlockOf(state)
	if b == nil {
		return fmt.Sprintf("?%d", state)
	}
	s := b.Name
	if v, ok := b.Get(state, "age"); ok {
		s += " age=" + v
	}
	if v, ok := b.Get(state, "berries"); ok {
		s += " berries=" + v
	}
	return s
}

// vineWorld is a player in the overworld with one empty stored column at 0 0.
func vineWorld(t *testing.T) (*Player, *column, func() []outPacket) {
	t.Helper()
	p, take := soundPlayer(t, version.Newest)
	p.world = newWorld(p.rem.pal, 0)
	p.javaDim = javaDimension(0)
	col := &column{c: chunk.New(p.world.reg, p.world.r)}
	p.world.cols[ChunkPos{0, 0}] = col
	return p, col, take
}

// TestVineColumn: vines in a sent column are Java bodies where the same vine follows in the
// growth direction (across section borders) and tips elsewhere (also at the column's ends).
func TestVineColumn(t *testing.T) {
	p, col, take := vineWorld(t)
	pal := p.rem.pal
	weep := func(a int) uint32 { return vineIdx(t, pal, "minecraft:weeping_vines", a) }
	twist := func(a int) uint32 { return vineIdx(t, pal, "minecraft:twisting_vines", a) }
	kelp := func(a int) uint32 { return vineIdx(t, pal, "minecraft:kelp", a) }
	cave := func(a int) uint32 { return vineIdx(t, pal, "minecraft:cave_vines", a) }
	caveBody := func(a int) uint32 { return vineIdx(t, pal, "minecraft:cave_vines_body_with_berries", a) }
	caveHead := func(a int) uint32 { return vineIdx(t, pal, "minecraft:cave_vines_head_with_berries", a) }
	set := map[cube.Pos]uint32{
		// Weeping vines hanging across the section border at y 16.
		{1, 17, 1}: weep(3), {1, 16, 1}: weep(4), {1, 15, 1}: weep(5),
		// Kelp growing across the border at y 32, in water.
		{2, 31, 2}: kelp(1), {2, 32, 2}: kelp(2), {2, 33, 2}: kelp(9),
		// Twisting vines.
		{3, 47, 3}: twist(0), {3, 48, 3}: twist(6),
		// Cave vines with and without berries count as one vine.
		{4, 63, 4}: cave(0), {4, 62, 4}: caveBody(0), {4, 61, 4}: cave(2), {4, 60, 4}: caveBody(7),
		{4, 70, 4}: caveHead(0), {4, 69, 4}: caveHead(11),
		// Different vines do not join.
		{5, 80, 5}: weep(1), {5, 79, 5}: twist(1),
		// The ends of the column.
		{6, -64, 6}: weep(2), {6, -63, 6}: weep(0), {6, 318, 6}: kelp(0), {6, 319, 6}: kelp(4),
	}
	want := map[cube.Pos]string{
		{1, 17, 1}: "minecraft:weeping_vines_plant", {1, 16, 1}: "minecraft:weeping_vines_plant", {1, 15, 1}: "minecraft:weeping_vines age=5",
		{2, 31, 2}: "minecraft:kelp_plant", {2, 32, 2}: "minecraft:kelp_plant", {2, 33, 2}: "minecraft:kelp age=9",
		{3, 47, 3}: "minecraft:twisting_vines_plant", {3, 48, 3}: "minecraft:twisting_vines age=6",
		{4, 63, 4}: "minecraft:cave_vines_plant berries=false", {4, 62, 4}: "minecraft:cave_vines_plant berries=true",
		{4, 61, 4}: "minecraft:cave_vines_plant berries=false", {4, 60, 4}: "minecraft:cave_vines age=7 berries=true",
		{4, 70, 4}: "minecraft:cave_vines_plant berries=true", {4, 69, 4}: "minecraft:cave_vines age=11 berries=true",
		{5, 80, 5}: "minecraft:weeping_vines age=1", {5, 79, 5}: "minecraft:twisting_vines age=1",
		{6, -64, 6}: "minecraft:weeping_vines age=2", {6, -63, 6}: "minecraft:weeping_vines_plant",
		{6, 318, 6}: "minecraft:kelp_plant", {6, 319, 6}: "minecraft:kelp age=4",
	}
	for pos, idx := range set {
		col.c.SetBlock(uint8(pos[0]), int16(pos[1]), uint8(pos[2]), 0, idx)
	}
	water, ok := pal.Lookup("minecraft:water", map[string]any{"liquid_depth": int32(0)})
	if !ok {
		t.Fatal("no water")
	}
	for y := 31; y <= 33; y++ {
		col.c.SetBlock(2, int16(y), 2, 1, water)
	}
	if err := p.sendColumn(ChunkPos{0, 0}, col); err != nil {
		t.Fatal(err)
	}
	v := p.ver()
	var got *jchunk.Column
	for _, o := range take() {
		if o.id == v777.ClientboundPlayLevelChunkWithLight {
			got = &jchunk.Column{}
			if err := jchunk.NewDecoder(v.BlockStates, v.Biomes).Decode(wire.NewReader(o.w.B), got); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got == nil {
		t.Fatal("no level_chunk_with_light")
	}
	for pos, w := range want {
		y := pos[1] - dimMinY(p.javaDim)
		state := got.Sections[y>>4].Blocks[jchunk.BlockIndex(pos[0], y&15, pos[2])]
		if g := javaDesc(state); g != w {
			t.Errorf("%v (%s): %s, want %s", pos, pal.Name(set[pos]), g, w)
		}
		// The block update path (javaStateAt) agrees with the chunk.
		if s := p.javaStateAt(col, pos); s != state {
			t.Errorf("%v: javaStateAt %s, chunk %s", pos, javaDesc(s), javaDesc(state))
		}
	}
}

// sentBlocks reads block_update and section_blocks_update packets: the last state per position.
func sentBlocks(out []outPacket) map[cube.Pos]uint32 {
	m := map[cube.Pos]uint32{}
	for _, o := range out {
		r := wire.NewReader(o.w.B)
		switch o.id {
		case v777.ClientboundPlayBlockUpdate:
			x, y, z := r.Position()
			m[cube.Pos{x, y, z}] = uint32(r.VarInt())
		case v777.ClientboundPlaySectionBlocksUpdate:
			k := r.Int64()
			sx, sz, sy := int(k>>42), int(k<<22>>42), int(k<<44>>44)
			n := int(r.VarInt())
			for i := 0; i < n; i++ {
				e := uint64(r.VarLong())
				m[cube.Pos{sx<<4 | int(e>>8&15), sy<<4 | int(e&15), sz<<4 | int(e>>4&15)}] = uint32(e >> 12)
			}
		}
	}
	return m
}

// TestVineUpdates: placing or breaking a vine block redraws the neighbour whose form depends on
// it, and only that one.
func TestVineUpdates(t *testing.T) {
	p, col, take := vineWorld(t)
	pal := p.rem.pal
	weep := vineIdx(t, pal, "minecraft:weeping_vines", 0)
	kelp := vineIdx(t, pal, "minecraft:kelp", 3)
	stone := blockIdx(t, pal, "minecraft:stone")
	for y := 15; y <= 17; y++ {
		col.c.SetBlock(1, int16(y), 1, 0, weep)
	}
	col.c.SetBlock(2, 40, 2, 0, kelp)
	col.c.SetBlock(3, 50, 3, 0, weep)
	col.sent = true
	take()
	change := func(x, y, z int, idx uint32) map[cube.Pos]string {
		p.updateBlocks([]blockChange{{pos: protocol.BlockPos{int32(x), int32(y), int32(z)}, rid: pal.IndexToServer(idx)}})
		out := map[cube.Pos]string{}
		for pos, s := range sentBlocks(take()) {
			out[pos] = javaDesc(s)
		}
		return out
	}
	check := func(what string, got, want map[cube.Pos]string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: sent %v, want %v", what, got, want)
			return
		}
		for pos, w := range want {
			if got[pos] != w {
				t.Errorf("%s: %v sent as %q, want %q", what, pos, got[pos], w)
			}
		}
	}
	// Breaking the lowest weeping vine makes the one above it the tip.
	check("break weeping tip", change(1, 15, 1, pal.Air), map[cube.Pos]string{
		{1, 15, 1}: "minecraft:air", {1, 16, 1}: "minecraft:weeping_vines age=0",
	})
	// A vine growing down turns the old tip into a body.
	check("grow weeping", change(1, 15, 1, weep), map[cube.Pos]string{
		{1, 15, 1}: "minecraft:weeping_vines age=0", {1, 16, 1}: "minecraft:weeping_vines_plant",
	})
	// Kelp growing up turns the old tip into a body.
	check("grow kelp", change(2, 41, 2, kelp), map[cube.Pos]string{
		{2, 41, 2}: "minecraft:kelp age=3", {2, 40, 2}: "minecraft:kelp_plant",
	})
	// A block above weeping vines does not change them: only the block is sent.
	check("stone above weeping", change(3, 51, 3, stone), map[cube.Pos]string{
		{3, 51, 3}: "minecraft:stone",
	})
}
