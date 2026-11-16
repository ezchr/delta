package bridge

import (
	"testing"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestModelFx: a hurt custom mob's displays get their item again (coloured, then plain), its
// hurt sound is heard, and its name shows on a text display while the player looks at it.
func TestModelFx(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	p.rem.jpack = animTestPack()
	p.mv.yaw = 180 // looking away from where the mob will be
	name := protocol.EntityMetadata{protocol.EntityDataKeyName: "Walker Lv. 3\nPress Use"}
	p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: 10, EntityUniqueID: -10, Position: mgl32.Vec3{0, 0, 4}, EntityMetadata: name})
	me := p.packs.ents[10]
	p.animTick()
	if me == nil || len(me.anim.parts) != 4 {
		t.Fatal("walker not animated")
	}
	take()
	count := func(out []outPacket, id int32) (n int) {
		for _, o := range out {
			if o.id == id {
				n++
			}
		}
		return n
	}
	items := func(out []outPacket) (n int) {
		for _, o := range out {
			if o.id == v777.ClientboundPlaySetEntityData {
				r := wire.NewReader(o.w.B)
				r.VarInt()
				if r.Byte() == displayItemIdx {
					n++
				}
			}
		}
		return n
	}
	// Hurt: every part gets its item with the colour, in a bundle, and the plain one later.
	p.dispatchBedrock(&packet.ActorEvent{EntityRuntimeID: 10, EventType: packet.ActorEventHurt})
	out := take()
	if items(out) != 4 || count(out, v777.ClientboundPlayBundleDelimiter) != 2 || me.tintUntil == 0 || count(out, v777.ClientboundPlaySound) != 1 {
		t.Fatalf("hurt: %d item updates, tint until %d", items(out), me.tintUntil)
	}
	for i := 0; i < hurtTicks+1; i++ {
		p.modelFxTick()
	}
	if out = take(); items(out) != 4 || me.tintUntil != 0 {
		t.Fatalf("after the flash: %d item updates, tint until %d", items(out), me.tintUntil)
	}
	// A hurt level sound for it adds nothing (the event was heard); for a custom actor that is
	// no model it plays the generic sound.
	p.dispatchBedrock(&packet.LevelSoundEvent{SoundType: "hurt", EntityType: "t:walker", Position: mgl32.Vec3{0, 0, 4}, EntityUniqueID: -10})
	if out = take(); count(out, v777.ClientboundPlaySound) != 0 {
		t.Fatalf("a second hurt sound")
	}
	p.dispatchBedrock(&packet.LevelSoundEvent{SoundType: "hurt", EntityType: "t:other", Position: mgl32.Vec3{0, 0, 4}, EntityUniqueID: -99})
	if out = take(); count(out, v777.ClientboundPlaySound) != 1 {
		t.Fatalf("hurt sound: %d sound packets", count(out, v777.ClientboundPlaySound))
	}
	// The player looks south at it (yaw 0): its name appears on a display riding the hitbox;
	// looking away removes it.
	p.mv.yaw, p.mv.pitch = 0, 0
	p.modelFxTick()
	p.modelFxTick()
	out = take()
	if me.nameTag == 0 || count(out, v777.ClientboundPlayAddEntity) != 1 || count(out, v777.ClientboundPlaySetPassengers) != 1 {
		t.Fatalf("looking at it: name display %d, %d added", me.nameTag, count(out, v777.ClientboundPlayAddEntity))
	}
	p.mv.yaw = 180
	p.modelFxTick()
	p.modelFxTick()
	if out = take(); me.nameTag != 0 || count(out, v777.ClientboundPlayRemoveEntities) != 1 {
		t.Fatalf("looking away: name display %d", me.nameTag)
	}
	// Too far to show a name.
	p.mv.yaw = 0
	p.mv.pos[2] = -20
	p.modelFxTick()
	p.modelFxTick()
	if me.nameTag != 0 {
		t.Fatal("a name from 24 blocks")
	}
}

// TestModelBoxScale: the hitbox of a custom mob is its box at its scale, as the server has it,
// and it follows a change of the scale; the name display starts on top of that box.
func TestModelBoxScale(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	p.rem.jpack = animTestPack()
	p.mv.yaw = 180
	meta := protocol.EntityMetadata{protocol.EntityDataKeyName: "Owl", protocol.EntityDataKeyScale: float32(0.65),
		protocol.EntityDataKeyWidth: float32(0.9), protocol.EntityDataKeyHeight: float32(3)}
	p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: 10, EntityUniqueID: -10, Position: mgl32.Vec3{0, 0, 4}, EntityMetadata: meta})
	me := p.packs.ents[10]
	if me == nil || me.hitbox == 0 {
		t.Fatal("no hitbox")
	}
	box := func(out []outPacket) (w, h float32) {
		for _, o := range out {
			if o.id != v777.ClientboundPlaySetEntityData {
				continue
			}
			r := wire.NewReader(o.w.B)
			if r.VarInt() != me.hitbox || r.Byte() != interactionWidth {
				continue
			}
			r.VarInt()
			w = r.Float32()
			r.Byte()
			r.VarInt()
			h = r.Float32()
		}
		return w, h
	}
	near := func(a, b float32) bool { return a-b < 1e-4 && b-a < 1e-4 }
	if w, h := box(take()); !near(w, 0.9*0.65) || !near(h, 3*0.65) {
		t.Fatalf("hitbox %v x %v, want 0.585 x 1.95", w, h)
	}
	p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 10, EntityMetadata: protocol.EntityMetadata{protocol.EntityDataKeyScale: float32(1)}})
	if w, h := box(take()); !near(w, 0.9) || !near(h, 3) {
		t.Fatalf("hitbox after the scale changed %v x %v, want 0.9 x 3", w, h)
	}
	// Looked at: the name display is added on top of the box.
	p.mv.yaw, p.mv.pitch = 0, 0
	p.modelFxTick()
	p.modelFxTick()
	for _, o := range take() {
		if o.id == v777.ClientboundPlayAddEntity {
			r := wire.NewReader(o.w.B)
			if r.VarInt() != me.nameTag {
				continue
			}
			r.UUID()
			r.VarInt()
			r.Float64()
			if y := r.Float64(); y != 3 {
				t.Fatalf("name display added at y %v, want 3", y)
			}
			return
		}
	}
	t.Fatal("no name display")
}
