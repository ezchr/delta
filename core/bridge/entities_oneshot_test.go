package bridge

import (
	"testing"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestAnimOneShots: an animation the server plays (AnimateEntity) moves the mob's parts once and
// is over; attack and hurt events reach the scripts' inputs; a dead mob keeps its displays.
func TestAnimOneShots(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	p.rem.jpack = animTestPack()
	tick := func(n int) {
		for i := 0; i < n; i++ {
			p.animTick()
		}
	}
	p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: 10, EntityUniqueID: -10, Position: mgl32.Vec3{3, 0, 3}})
	me := p.packs.ents[10]
	tick(1 + animNearTicks*2)
	take()
	legs := func() (n int) {
		for _, o := range take() {
			if o.id != v777.ClientboundPlaySetEntityData {
				continue
			}
			d := readDisplayData(t, o)
			if d.id == me.anim.parts[2].id || d.id == me.anim.parts[3].id {
				n++
			}
		}
		return n
	}
	if legs() != 0 {
		t.Fatal("legs move while idle")
	}
	// The server plays the walk on the standing mob (and on an actor the client does not have).
	p.dispatchBedrock(&packet.AnimateEntity{Animation: "animation.t.walk", BlendOutTime: 0.1, EntityRuntimeIDs: []uint64{10, 999}})
	tick(animNearTicks * 3)
	if n := legs(); n < 4 || me.anim.st.Overlaid() != "animation.t.walk" || me.anim.st.Playing() != "move=idle" {
		t.Fatalf("%d leg updates, playing %q + %q", n, me.anim.st.Playing(), me.anim.st.Overlaid())
	}
	tick(20) // the walk is a second long
	take()
	tick(animNearTicks * 2)
	if n := legs(); n != 0 || me.anim.st.Overlaid() != "" {
		t.Fatalf("after the animation: %d leg updates, still playing %q", n, me.anim.st.Overlaid())
	}
	p.dispatchBedrock(&packet.AnimateEntity{Animation: "animation.t.nothing", EntityRuntimeIDs: []uint64{10}})
	// An attack swing and a hit.
	p.dispatchBedrock(&packet.ActorEvent{EntityRuntimeID: 10, EventType: packet.ActorEventStartAttacking})
	p.dispatchBedrock(&packet.ActorEvent{EntityRuntimeID: 10, EventType: packet.ActorEventHurt})
	tick(animNearTicks)
	if in := me.anim.st.In; in.AttackTime <= 0 || in.AttackTime > 1 || in.HurtTime <= 0 || in.HurtTime > hurtTicks {
		t.Fatalf("attack time %v, hurt time %v after the events", in.AttackTime, in.HurtTime)
	}
	tick(hurtTicks + animNearTicks)
	if in := me.anim.st.In; in.AttackTime != 0 || in.HurtTime != 0 {
		t.Fatalf("attack time %v, hurt time %v a while later", in.AttackTime, in.HurtTime)
	}
	// Dead: still animated (a faint animation is an animation).
	p.ents.shown[10].dead = true
	tick(animRankTicks * 2)
	if me.anim.parts == nil {
		t.Fatal("a dead mob lost its displays")
	}
}
