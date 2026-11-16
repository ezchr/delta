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
	if items(out) != 4 || count(out, v777.ClientboundPlayBundleDelimiter) != 2 || me.tintUntil == 0 {
		t.Fatalf("hurt: %d item updates, tint until %d", items(out), me.tintUntil)
	}
	for i := 0; i < hurtTicks+1; i++ {
		p.modelFxTick()
	}
	if out = take(); items(out) != 4 || me.tintUntil != 0 {
		t.Fatalf("after the flash: %d item updates, tint until %d", items(out), me.tintUntil)
	}
	// Its hurt sound: a custom mob's own sound is not in the Java pack, the generic one plays.
	p.dispatchBedrock(&packet.LevelSoundEvent{SoundType: "hurt", EntityType: "t:walker", Position: mgl32.Vec3{0, 0, 4}, EntityUniqueID: -10})
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
