package bridge

import (
	"encoding/json"
	"math"
	"testing"

	"delta/core/internal/anim"
	"delta/core/internal/javapack"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// animTestPack is a converted pack with one animated entity type, t:walker: a body, a head that
// nods while idle and two legs that swing while it walks.
func animTestPack() *javaPack {
	lib := anim.NewLibrary()
	lib.AddFile("animations/w.json", []byte(`{"animations": {
		"animation.t.idle": {"loop": true, "bones": {"head": {"rotation": ["math.sin(q.anim_time*90)*10", 0, 0]}}},
		"animation.t.walk": {"loop": true, "animation_length": 1, "bones": {
			"leg_left": {"rotation": {"0.0": [30, 0, 0], "0.5": [-30, 0, 0], "1.0": [30, 0, 0]}},
			"leg_right": {"rotation": {"0.0": [-30, 0, 0], "0.5": [30, 0, 0], "1.0": [-30, 0, 0]}}}}}}`))
	lib.AddFile("animation_controllers/w.json", []byte(`{"animation_controllers": {"controller.animation.t.move": {"initial_state": "idle", "states": {
		"idle": {"animations": ["idle"], "transitions": [{"walk": "q.ground_speed > 0.5"}]},
		"walk": {"animations": ["walk"], "transitions": [{"idle": "q.ground_speed <= 0.5"}]}}}}}`))
	src := &anim.Source{
		Bones: []anim.BoneDef{
			{Name: "body", Pivot: [3]float64{0, 8, 0}},
			{Name: "head", Parent: "body", Pivot: [3]float64{0, 16, 0}},
			{Name: "leg_left", Parent: "body", Pivot: [3]float64{2, 8, 0}},
			{Name: "leg_right", Parent: "body", Pivot: [3]float64{-2, 8, 0}},
		},
		Animations: map[string]string{"move": "controller.animation.t.move", "idle": "animation.t.idle", "walk": "animation.t.walk"},
		Animate:    []json.RawMessage{json.RawMessage(`"move"`)},
		Scale:      "q.property('t:size') > 0 ? q.property('t:size') : 1",
	}
	parts := []anim.Part{{Bone: -1, Model: "delta:entity/t/walker/p0", K: 1}, {Bone: 1, Model: "delta:entity/t/walker/p1", K: 1},
		{Bone: 2, Model: "delta:entity/t/walker/p2", K: 0.5}, {Bone: 3, Model: "delta:entity/t/walker/p3", K: 0.5}}
	res := &javapack.Result{Entities: map[string]javapack.Entity{
		"t:walker": {Model: "delta:entity/t/walker", Scale: 1, Anim: anim.NewModel(parts, src, lib)},
		"t:statue": {Model: "delta:entity/t/statue", Scale: 1},
	}}
	return &javaPack{packBuild: &packBuild{res: res, sha: "x"}}
}

// displayData is one set_entity_data packet of a display, by index.
type displayData struct {
	id    int32
	vals  map[byte][]float32 // ints as one value, vectors as three, quaternions as four
	item  bool
	other bool
}

func readDisplayData(t *testing.T, o outPacket) displayData {
	t.Helper()
	r := wire.NewReader(o.w.B)
	d := displayData{id: r.VarInt(), vals: map[byte][]float32{}}
	for r.Err == nil {
		idx := r.Byte()
		if idx == 0xff {
			break
		}
		switch ser := r.VarInt(); ser {
		case serInt:
			d.vals[idx] = []float32{float32(r.VarInt())}
		case serFloat:
			d.vals[idx] = []float32{r.Float32()}
		case serBool:
			r.Bool()
		case serVector3Data:
			d.vals[idx] = []float32{r.Float32(), r.Float32(), r.Float32()}
		case serQuaternion:
			d.vals[idx] = []float32{r.Float32(), r.Float32(), r.Float32(), r.Float32()}
		case serItemStack:
			d.item = true
			return d // the item is the last value the bridge writes before the end
		default:
			d.other = true // not a display's transformation (the mob's own entity data)
			return d
		}
	}
	if r.Err != nil {
		t.Fatalf("entity data of %d: %v", d.id, r.Err)
	}
	return d
}

func TestAnimatedMob(t *testing.T) {
	for _, v := range []*version.Version{version.Newest, version.ByProtocol(776)} {
		p, take := soundPlayer(t, v)
		p.rem.jpack = animTestPack()
		tick := func(n int) {
			for i := 0; i < n; i++ {
				p.animTick()
			}
		}
		count := func(out []outPacket, id int32) (n int) {
			for _, o := range out {
				if o.id == id {
					n++
				}
			}
			return n
		}

		// A walker next to the player: its own display, a hitbox, and four part displays that
		// ride it.
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: 10, EntityUniqueID: -10, Position: mgl32.Vec3{3, 0, 3}, Yaw: 90})
		e := p.ents.shown[10]
		me := p.packs.ents[10]
		if e == nil || me == nil || me.anim == nil {
			t.Fatalf("%d: walker not shown as a model", v.Protocol)
		}
		tick(1)
		out := take()
		if n := count(out, v777.ClientboundPlayAddEntity); n != 2+4 {
			t.Fatalf("%d: %d entities added, want the mob, its hitbox and 4 parts", v.Protocol, n)
		}
		if len(me.anim.parts) != 4 || len(e.parts) != 4 || me.anim.period != animNearTicks {
			t.Fatalf("%d: parts %d, period %d", v.Protocol, len(me.anim.parts), me.anim.period)
		}
		partIDs := map[int32]int{}
		for i, pt := range me.anim.parts {
			partIDs[pt.id] = i
		}
		var passengers []int32
		ownItemGone := false
		spawned := map[int32]displayData{}
		for _, o := range out {
			switch o.id {
			case v777.ClientboundPlaySetPassengers:
				r := wire.NewReader(o.w.B)
				if r.VarInt() == e.id {
					passengers = nil
					for n := r.VarInt(); n > 0; n-- {
						passengers = append(passengers, r.VarInt())
					}
				}
			case v777.ClientboundPlaySetEntityData:
				d := readDisplayData(t, o)
				if _, ok := partIDs[d.id]; ok {
					spawned[d.id] = d
				} else if d.id == e.id && d.item {
					ownItemGone = true
				}
			}
		}
		if len(passengers) != 5 {
			t.Fatalf("%d: passengers %v, want the hitbox and 4 parts", v.Protocol, passengers)
		}
		if !ownItemGone {
			t.Fatalf("%d: the mob's own display was not sent its item again", v.Protocol)
		}
		for id, i := range partIDs {
			d, ok := spawned[id]
			if !ok || !d.item || d.vals[displayInterpDuration][0] != animNearTicks || d.vals[displayPosInterp][0] != animNearTicks ||
				len(d.vals[displayTranslation]) != 3 || len(d.vals[displayLeftRotation]) != 4 || len(d.vals[displayScaleIdx]) != 3 {
				t.Fatalf("%d: part %d spawn data %+v", v.Protocol, i, d)
			}
			// A model shrunk by half is shown at twice the size.
			if want := float32(1 / me.anim.model.Parts[i].K); d.vals[displayScaleIdx][0] != want {
				t.Fatalf("%d: part %d scale %v, want %v", v.Protocol, i, d.vals[displayScaleIdx], want)
			}
		}

		// Idle: only the head moves, every animNearTicks ticks, with the interpolation started.
		tick(animNearTicks * 4)
		out = take()
		updates := map[int32]int{}
		for _, o := range out {
			if o.id != v777.ClientboundPlaySetEntityData {
				continue
			}
			d := readDisplayData(t, o)
			if _, ok := partIDs[d.id]; !ok {
				continue
			}
			updates[d.id]++
			if d := d.vals[displayInterpDelay]; len(d) != 1 || d[0] != 0 {
				t.Fatalf("%d: an update without the interpolation start", v.Protocol)
			}
			q := d.vals[displayLeftRotation]
			if n := q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3]; math.Abs(float64(n)-1) > 1e-3 {
				t.Fatalf("%d: rotation %v", v.Protocol, q)
			}
			if _, ok := d.vals[displayScaleIdx]; ok {
				t.Fatalf("%d: the scale was sent again though it did not change", v.Protocol)
			}
		}
		head := me.anim.parts[1].id
		if len(updates) != 1 || updates[head] != 4 {
			t.Fatalf("%d: idle updates %v, want 4 of the head (%d)", v.Protocol, updates, head)
		}
		if n := count(out, v777.ClientboundPlayBundleDelimiter); n != 8 {
			t.Fatalf("%d: %d bundle delimiters around 4 updates", v.Protocol, n)
		}
		if got := me.anim.st.Playing(); got != "move=idle" {
			t.Fatalf("%d: playing %q", v.Protocol, got)
		}

		// It walks and turns: the controller changes state, the legs move, every part turns.
		for i := 1; i <= 12; i++ {
			p.dispatchBedrock(&packet.MoveActorAbsolute{EntityRuntimeID: 10, Position: mgl32.Vec3{3 + float32(i)*0.2, 0, 3},
				Rotation: mgl32.Vec3{20, 45, 45}, Flags: packet.MoveFlagOnGround})
			tick(1)
		}
		if got := me.anim.st.Playing(); got != "move=walk" {
			t.Fatalf("%d: playing %q while walking (speed %v)", v.Protocol, got, me.anim.st.In.GroundSpeed)
		}
		if me.anim.st.In.HeadPitch != 20 || e.pitch != 0 {
			t.Fatalf("%d: head pitch %v for the animation, %v on the display", v.Protocol, me.anim.st.In.HeadPitch, e.pitch)
		}
		out = take()
		updates = map[int32]int{}
		rots := map[int32]int{}
		for _, o := range out {
			switch o.id {
			case v777.ClientboundPlaySetEntityData:
				updates[readDisplayData(t, o).id]++
			case v777.ClientboundPlayMoveEntityRot:
				if id := wire.NewReader(o.w.B).VarInt(); id != e.id {
					rots[id]++
				}
			}
		}
		for _, i := range []int{2, 3} {
			if updates[me.anim.parts[i].id] < 3 {
				t.Fatalf("%d: leg updates %v while walking", v.Protocol, updates)
			}
		}
		for _, pt := range me.anim.parts {
			if rots[pt.id] != 1 {
				t.Fatalf("%d: part rotations %v, want one each for the turn", v.Protocol, rots)
			}
		}
		if _, ok := updates[me.anim.parts[0].id]; ok {
			t.Fatalf("%d: the part on the model's root was sent a pose again", v.Protocol)
		}

		// A property scales it (scripts.scale): every part follows.
		e.props = map[string]any{"t:size": float32(2)}
		tick(animNearTicks)
		scaled := 0
		for _, o := range take() {
			if o.id == v777.ClientboundPlaySetEntityData {
				d := readDisplayData(t, o)
				if _, ok := partIDs[d.id]; ok && len(d.vals[displayScaleIdx]) == 3 {
					scaled++
				}
			}
		}
		if scaled != 4 || me.script != 2 {
			t.Fatalf("%d: %d parts rescaled, script scale %v", v.Protocol, scaled, me.script)
		}

		// Twenty more walkers at growing distances and one far away: the nearest animNear are
		// stepped fast, the next animFar slowly, the rest are the static model.
		for i := 0; i < 20; i++ {
			p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: uint64(300 + i), EntityUniqueID: int64(-300 - i),
				Position: mgl32.Vec3{0, 0, 8 + float32(i)}})
		}
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: 200, EntityUniqueID: -200, Position: mgl32.Vec3{0, 0, 42}})
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:statue", EntityRuntimeID: 201, EntityUniqueID: -201, Position: mgl32.Vec3{1, 0, 1}})
		tick(animRankTicks)
		near, far, static := 0, 0, 0
		for rid, m := range p.packs.ents {
			switch {
			case m.anim == nil:
				if rid != 201 {
					t.Fatalf("%d: walker %d has no animation state", v.Protocol, rid)
				}
			case m.anim.parts == nil:
				static++
				if rid >= 300 && rid < 300+animNear+animFar-1 {
					t.Fatalf("%d: near walker %d is static", v.Protocol, rid)
				}
			case m.anim.period == animNearTicks:
				near++
			case m.anim.period == animFarTicks:
				far++
			}
		}
		if near != animNear || far != animFar || static != 22-animNear-animFar {
			t.Fatalf("%d: %d near, %d far, %d static", v.Protocol, near, far, static)
		}
		if p.packs.ents[200].anim.parts != nil {
			t.Fatalf("%d: a mob %v blocks away is animated", v.Protocol, 42)
		}
		take()

		// The player walks away: the mobs become static (their parts are removed) and the first
		// one comes back when the player returns.
		p.mv.pos[0] = 500
		tick(animRankTicks)
		out = take()
		for rid, m := range p.packs.ents {
			if m.anim != nil && m.anim.parts != nil {
				t.Fatalf("%d: mob %d still animated from 500 blocks", v.Protocol, rid)
			}
		}
		if n := count(out, v777.ClientboundPlayRemoveEntities); n != animNear+animFar {
			t.Fatalf("%d: %d removals, want %d", v.Protocol, n, animNear+animFar)
		}
		if len(e.parts) != 0 {
			t.Fatalf("%d: the mob still lists parts %v", v.Protocol, e.parts)
		}
		p.mv.pos[0] = 0
		tick(animRankTicks)
		if me.anim.parts == nil {
			t.Fatalf("%d: the mob is not animated again", v.Protocol)
		}
		take()

		// Removed: its parts go with it.
		ids := map[int32]bool{e.id: true, me.hitbox: true}
		for _, pt := range me.anim.parts {
			ids[pt.id] = true
		}
		p.dispatchBedrock(&packet.RemoveActor{EntityUniqueID: -10})
		for _, o := range take() {
			if o.id == v777.ClientboundPlayRemoveEntities {
				r := wire.NewReader(o.w.B)
				for n := r.VarInt(); n > 0; n-- {
					delete(ids, r.VarInt())
				}
			}
		}
		if len(ids) != 0 || p.packs.ents[10] != nil {
			t.Fatalf("%d: not removed: %v", v.Protocol, ids)
		}
		tick(animRankTicks * 2) // nothing left of it to step
	}
}

func TestAnimationsOff(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	p.rem.jpack = animTestPack()
	animOff = true
	defer func() { animOff = false }()
	p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: 10, EntityUniqueID: -10, Position: mgl32.Vec3{3, 0, 3},
		EntityMetadata: protocol.EntityMetadata{protocol.EntityDataKeyScale: float32(1)}})
	for i := 0; i < 40; i++ {
		p.animTick()
	}
	n := 0
	for _, o := range take() {
		if o.id == v777.ClientboundPlayAddEntity {
			n++
		}
	}
	if n != 2 || p.packs.ents[10].anim.parts != nil {
		t.Fatalf("%d entities added with animations off", n)
	}
}
