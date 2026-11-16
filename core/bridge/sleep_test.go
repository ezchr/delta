package bridge

import (
	"math"
	"testing"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Sleeping (sleep.go): what BDS sends when a player goes to bed and wakes (positions as BDS
// 1.26.60 sent them on a flat world, bed head block at 2975 -60 3003), what the Java client gets
// and what BDS gets back.

var sleepFlag = int64(1) << (protocol.EntityDataFlagSleeping - 64)

// sleepData is a set_entity_data's pose (-1: not in it) and sleeping position (hasBed: in it;
// bed nil: none).
type sleepData struct {
	id     int32
	pose   int32
	bed    *protocol.BlockPos
	hasBed bool
}

func readSleepData(t *testing.T, w *wire.Writer) sleepData {
	t.Helper()
	r := wire.NewReader(w.B)
	d := sleepData{id: r.VarInt(), pose: -1}
	for r.Err == nil {
		idx := r.Byte()
		if idx == 0xff {
			break
		}
		ser := r.VarInt()
		switch {
		case idx == 6 && ser == serPose:
			d.pose = r.VarInt()
		case idx == 14 && ser == serOptBlockPos:
			d.hasBed = true
			if r.Bool() {
				x, y, z := r.Position()
				d.bed = &protocol.BlockPos{int32(x), int32(y), int32(z)}
			}
		case ser == serByte || ser == serBool:
			r.Byte()
		case ser == serInt || ser == serPose:
			r.VarInt()
		case ser == serFloat:
			r.Float32()
		default:
			return d // another entry: not decoded here
		}
	}
	if r.Err != nil {
		t.Fatalf("set_entity_data: %v", r.Err)
	}
	return d
}

// selfSleepData finds the set_entity_data of the player itself in out.
func selfSleepData(t *testing.T, out []outPacket) (sleepData, bool) {
	t.Helper()
	for _, o := range out {
		if o.id == v777.ClientboundPlaySetEntityData {
			if d := readSleepData(t, o.w); d.id == SelfEntityID {
				return d, true
			}
		}
	}
	return sleepData{}, false
}

// positions are the player_position packets in out: x, y, z and the relative flags.
func positions(out []outPacket) [][4]float64 {
	var ps [][4]float64
	for _, o := range out {
		if o.id != v777.ClientboundPlayPlayerPosition {
			continue
		}
		r := wire.NewReader(o.w.B)
		r.VarInt()
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		r.Float64()
		r.Float64()
		r.Float64()
		r.Float32()
		r.Float32()
		ps = append(ps, [4]float64{x, y, z, float64(r.Int32())})
	}
	return ps
}

// actions are the PlayerActions BDS got.
func actions(p *Player) []int32 {
	var as []int32
	for _, pk := range p.rem.testOut {
		if a, ok := pk.(*packet.PlayerAction); ok {
			as = append(as, a.ActionType)
		}
	}
	return as
}

func near(a, b mgl64.Vec3) bool { return a.Sub(b).Len() < 1e-4 }

func TestSleepSelf(t *testing.T) {
	for _, v := range []*version.Version{version.Newest, version.ByProtocol(776)} {
		p, take := soundPlayer(t, v)
		p.rem.log = cfg.Log
		p.world = newWorld(p.rem.pal, 0)
		p.javaDim = javaDimension(0)
		p.javaLoaded = true
		p.self.mode = GameSurvival
		p.mv.reset(mgl64.Vec3{2975.5, -60, 3000.5}, 0, 0)
		self := p.rem.gd.EntityRuntimeID
		bed := protocol.BlockPos{2975, -60, 3003}

		// The server puts the player in bed: the flag, and the bed (only when it changed: the
		// spawn bed).
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: self, EntityMetadata: protocol.EntityMetadata{
			protocol.EntityDataKeyBedPosition: bed, protocol.EntityDataKeyFlagsTwo: sleepFlag}})
		d, ok := selfSleepData(t, take())
		if !ok || d.pose != poseSleeping || d.bed == nil || *d.bed != bed {
			t.Fatalf("%d asleep: own entity data %+v (sent %v), want pose %d and bed %v", v.Protocol, d, ok, poseSleeping, bed)
		}
		if as := actions(p); len(as) != 1 || as[0] != protocol.PlayerActionStartSleeping {
			t.Fatalf("%d asleep: player actions %v, want StartSleeping", v.Protocol, as)
		}
		p.rem.testOut = nil

		// The server's correction to the bed (eyes 0.90625 above the bed block) does not move the
		// Java client, which lies in its bed by itself.
		sleepEyes := mgl32.Vec3{2975.5, -59.09375, 3003.5}
		p.dispatchBedrock(&packet.CorrectPlayerMovePrediction{Position: sleepEyes, OnGround: true, Tick: p.mv.tick})
		if ps := positions(take()); len(ps) != 0 {
			t.Fatalf("%d asleep: the client was moved %v", v.Protocol, ps)
		}

		// The Java client lies on the bed (it falls from 0.125 above the bed's top onto it), keys
		// held: the server gets its own position and no movement.
		for _, y := range []float64{-59.3125, -59.391, -59.4375, -59.4375} {
			tick(t, p, keyForward|keyJump, mgl64.Vec3{2975.5, y, 3003.5}, 0, 0, y == -59.4375)
		}
		n := 0
		for _, pk := range p.rem.testOut {
			in, ok := pk.(*packet.PlayerAuthInput)
			if !ok {
				continue
			}
			n++
			if !near(vec64(in.Position), vec64(sleepEyes)) || in.Delta != (mgl32.Vec3{}) || in.MoveVector != (mgl32.Vec2{}) ||
				in.InputData.Load(packet.InputFlagUp) || in.InputData.Load(packet.InputFlagJumping) || in.InputData.Load(packet.InputFlagStartJumping) {
				t.Fatalf("%d asleep: input at %v delta %v move %v up %v jump %v, want %v and no movement", v.Protocol,
					in.Position, in.Delta, in.MoveVector, in.InputData.Load(packet.InputFlagUp), in.InputData.Load(packet.InputFlagJumping), sleepEyes)
			}
		}
		if n != 4 {
			t.Fatalf("%d asleep: %d inputs for 4 ticks", v.Protocol, n)
		}
		p.rem.testOut = nil

		// Leave Bed: player_command STOP_SLEEPING.
		javaIn(t, p, v777.ServerboundPlayPlayerCommand, func(w *wire.Writer) {
			w.VarInt(SelfEntityID)
			w.VarInt(0)
			w.VarInt(0)
		})
		if as := actions(p); len(as) != 1 || as[0] != protocol.PlayerActionStopSleeping {
			t.Fatalf("%d leave bed: player actions %v, want StopSleeping", v.Protocol, as)
		}

		// The server wakes the player: the client stands up (animate WAKE_UP) and loses its bed,
		// then goes where the server put it (eyes 1.72 above the block next to the bed).
		p.dispatchBedrock(&packet.Animate{ActionType: packet.AnimateActionStopSleep, EntityRuntimeID: self})
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: self, EntityMetadata: protocol.EntityMetadata{
			protocol.EntityDataKeyFlagsTwo: int64(0)}})
		out := take()
		var wakes []int32
		for _, o := range out {
			if o.id == v777.ClientboundPlayAnimate {
				r := wire.NewReader(o.w.B)
				if r.VarInt() == SelfEntityID {
					wakes = append(wakes, int32(r.Byte()))
				}
			}
		}
		want := int32(2) // 26.2's WAKE_UP
		if v.Native() {
			want = 0
		}
		if len(wakes) != 1 || wakes[0] != want {
			t.Fatalf("%d awake: own animates %v, want one WAKE_UP %d", v.Protocol, wakes, want)
		}
		d, ok = selfSleepData(t, out)
		if !ok || d.pose != poseStanding || !d.hasBed || d.bed != nil {
			t.Fatalf("%d awake: own entity data %+v (sent %v), want standing without a bed", v.Protocol, d, ok)
		}
		// A correction still naming the bed (to an input sent before the wake-up) is not the new
		// place.
		p.dispatchBedrock(&packet.CorrectPlayerMovePrediction{Position: sleepEyes, OnGround: true, Tick: p.mv.tick})
		if ps := positions(take()); len(ps) != 0 {
			t.Fatalf("%d awake: moved back to the bed %v", v.Protocol, ps)
		}
		p.dispatchBedrock(&packet.CorrectPlayerMovePrediction{Position: mgl32.Vec3{2974.5, -58.28, 3003.5}, OnGround: true, Tick: p.mv.tick})
		ps := positions(take())
		if len(ps) != 1 || ps[0][3] != 0 || !near(mgl64.Vec3{ps[0][0], ps[0][1], ps[0][2]}, mgl64.Vec3{2974.5, -59.9, 3003.5}) {
			t.Fatalf("%d awake: positions %v, want one absolute to 2974.5 -59.9 3003.5", v.Protocol, ps)
		}
		if p.self.sleeping || p.mv.waking {
			t.Fatalf("%d awake: sleeping %v waking %v", v.Protocol, p.self.sleeping, p.mv.waking)
		}
	}
}

func TestSleepOtherPlayer(t *testing.T) {
	for _, v := range []*version.Version{version.Newest, version.ByProtocol(776)} {
		p, take := soundPlayer(t, v)
		bed := protocol.BlockPos{2975, -60, 3003}
		java := func(rid uint64) mgl64.Vec3 { return p.ents.shown[rid].pos }

		// Someone already asleep comes into view (AddPlayer: the feet, 0.2 below the server's
		// position of a sleeper): on the bed's top.
		p.dispatchBedrock(&packet.AddPlayer{UUID: uuid.New(), Username: "Sleeper", EntityRuntimeID: 200,
			AbilityData: protocol.AbilityData{EntityUniqueID: -200}, Position: mgl32.Vec3{2975.5, -59.29375, 3003.5},
			EntityMetadata: protocol.EntityMetadata{protocol.EntityDataKeyFlagsTwo: sleepFlag, protocol.EntityDataKeyBedPosition: bed}})
		if e, ok := p.ents.shown[200]; !ok || !e.spawned {
			t.Fatalf("%d: sleeper not shown", v.Protocol)
		}
		if got := java(200); math.Abs(got[1]-(-59.4375)) > 1e-4 {
			t.Fatalf("%d: sleeper added at %v, want y -59.4375", v.Protocol, got)
		}
		var data sleepData
		for _, o := range take() {
			if o.id == v777.ClientboundPlaySetEntityData {
				if d := readSleepData(t, o.w); d.id == p.ents.shown[200].id && d.pose >= 0 {
					data = d
				}
			}
		}
		if data.pose != poseSleeping {
			t.Fatalf("%d: sleeper's pose %d, want %d", v.Protocol, data.pose, poseSleeping)
		}

		// Another player walks up and lies down (the server's position first, then the flag)
		// and gets up again (the flag first, then the position).
		p.dispatchBedrock(&packet.AddPlayer{UUID: uuid.New(), Username: "Walker", EntityRuntimeID: 201,
			AbilityData: protocol.AbilityData{EntityUniqueID: -201}, Position: mgl32.Vec3{2975.5, -60, 3000.5},
			EntityMetadata: protocol.EntityMetadata{}})
		p.dispatchBedrock(&packet.MovePlayer{EntityRuntimeID: 201, Position: mgl32.Vec3{2975.5, -59.09375, 3003.5}, Mode: packet.MoveModeNormal})
		take()
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 201, EntityMetadata: protocol.EntityMetadata{
			protocol.EntityDataKeyFlagsTwo: sleepFlag, protocol.EntityDataKeyBedPosition: bed}})
		if got := java(201); math.Abs(got[1]-(-59.4375)) > 1e-4 {
			t.Fatalf("%d: player lying down at %v, want y -59.4375", v.Protocol, got)
		}
		synced := false
		for _, o := range take() {
			synced = synced || o.id == v777.ClientboundPlayEntityPositionSync
		}
		if !synced {
			t.Fatalf("%d: the lying player's position was not sent again", v.Protocol)
		}
		p.dispatchBedrock(&packet.MovePlayer{EntityRuntimeID: 201, Position: mgl32.Vec3{2975.5, -59.09375, 3003.5}, Mode: packet.MoveModeNormal})
		if got := java(201); math.Abs(got[1]-(-59.4375)) > 1e-4 {
			t.Fatalf("%d: lying player moved to %v, want y -59.4375", v.Protocol, got)
		}
		p.dispatchBedrock(&packet.Animate{ActionType: packet.AnimateActionStopSleep, EntityRuntimeID: 201})
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 201, EntityMetadata: protocol.EntityMetadata{
			protocol.EntityDataKeyFlagsTwo: int64(0)}})
		p.dispatchBedrock(&packet.MovePlayer{EntityRuntimeID: 201, Position: mgl32.Vec3{2974.5, -58.28, 3003.5}, Mode: packet.MoveModeNormal})
		if got := java(201); math.Abs(got[1]-(-59.9)) > 1e-4 {
			t.Fatalf("%d: player up at %v, want y -59.9", v.Protocol, got)
		}
		wake := false
		for _, o := range take() {
			if o.id == v777.ClientboundPlayAnimate {
				r := wire.NewReader(o.w.B)
				wake = wake || r.VarInt() == p.ents.shown[201].id
			}
		}
		if !wake {
			t.Fatalf("%d: no WAKE_UP for the player getting up", v.Protocol)
		}
	}
}
