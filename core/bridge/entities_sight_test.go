package bridge

import (
	"testing"

	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/ezchr/go-mcjava/version"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestAnimSight: a mob behind a wall takes no animation slot, one in the open does; a hole in
// the wall, glass and a mob that sticks out over it count as seen; an animated mob that goes
// out of sight keeps its slot for a few tests.
func TestAnimSight(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	p.rem.jpack = animTestPack()
	p.world = newWorld(p.rem.pal, 0)
	for cx := int32(-1); cx <= 1; cx++ {
		for cz := int32(-1); cz <= 1; cz++ {
			p.world.cols[ChunkPos{cx, cz}] = &column{c: chunk.New(p.world.reg, p.world.r)}
		}
	}
	idx := func(name string) uint32 { return p.rem.pal.ServerToIndex(uint32(rid(t, p, name))) }
	stone, glass, air := idx("minecraft:stone"), idx("minecraft:glass"), idx("minecraft:air")
	set := func(x, y, z int, b uint32) {
		p.world.cols[ChunkPos{int32(x >> 4), int32(z >> 4)}].c.SetBlock(uint8(x&15), int16(y), uint8(z&15), 0, b)
	}
	// The player stands at 0.5 0 0.5 (eyes at 1.62) and looks east at a wall at x = 4, 9 wide
	// and 6 high.
	p.mv.pos = mgl64.Vec3{0.5, 0, 0.5}
	wall := func(b uint32) {
		for z := -4; z <= 4; z++ {
			for y := 0; y < 6; y++ {
				set(4, y, z, b)
			}
		}
	}
	wall(stone)
	animNear, animFar = 1, 0
	defer func() { animNear, animFar = 6, 10 }()
	tick := func(n int) {
		for i := 0; i < n; i++ {
			p.animTick()
		}
	}
	add := func(rid uint64, x, z float32) *modelEntity {
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: rid, EntityUniqueID: -int64(rid), Position: mgl32.Vec3{x, 0, z}})
		me := p.packs.ents[rid]
		if me == nil || me.anim == nil {
			t.Fatalf("walker %d not shown", rid)
		}
		return me
	}
	// One slot: the hidden mob is nearer, the one in the open gets it.
	hidden := add(10, 7.5, 0.5)
	open := add(11, -12.5, 0.5)
	tick(1)
	if hidden.anim.parts != nil || open.anim.parts == nil {
		t.Fatalf("behind the wall animated: %v, in the open: %v", hidden.anim.parts != nil, open.anim.parts != nil)
	}
	if p.sightLine(p.ents.shown[10], hidden) || !p.sightLine(p.ents.shown[11], open) {
		t.Fatal("sight lines")
	}
	// Glass instead of stone: seen, and being nearer it takes the slot at its next test.
	wall(glass)
	tick(animSightTicks + animRankTicks)
	if hidden.anim.parts == nil || open.anim.parts != nil {
		t.Fatalf("behind glass animated: %v, the other: %v", hidden.anim.parts != nil, open.anim.parts != nil)
	}
	// The wall again: it keeps the slot until it failed animSightMisses tests, then loses it.
	wall(stone)
	tick(animSightTicks + animRankTicks)
	if hidden.anim.parts == nil {
		t.Fatal("lost its slot at the first test that did not see it")
	}
	tick(animSightTicks * animSightMisses)
	if hidden.anim.parts != nil || open.anim.parts == nil {
		t.Fatalf("after %d failed tests: behind the wall animated: %v, in the open: %v", animSightMisses, hidden.anim.parts != nil, open.anim.parts != nil)
	}
	// A hole at eye level, and a mob taller than the wall, are seen.
	set(4, 1, 0, air)
	if !p.sightLine(p.ents.shown[10], hidden) {
		t.Fatal("not seen through a hole in the wall")
	}
	set(4, 1, 0, stone)
	hidden.h = 12
	if !p.sightLine(p.ents.shown[10], hidden) {
		t.Fatal("a mob twice as tall as the wall is not seen")
	}
	hidden.h = 1.8
	// Half behind the wall's end: its side is seen.
	p.dispatchBedrock(&packet.MoveActorAbsolute{EntityRuntimeID: 10, Position: mgl32.Vec3{5.5, 0, 6.2}, Flags: packet.MoveFlagOnGround})
	hidden.w = 1.5
	if !p.sightLine(p.ents.shown[10], hidden) {
		t.Fatal("a mob half behind the wall's end is not seen")
	}
	take()
}
