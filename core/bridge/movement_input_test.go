package bridge

import (
	"math"
	"testing"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/version"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Movement: Java movement packets in, the PlayerAuthInput BDS gets out (one per client tick,
// with the position at eye height, the tick's delta, the look and the input flags).

// movePlayer is a player on a fake connection whose Bedrock packets the test takes.
func movePlayer(t *testing.T) (*Player, func() []*packet.PlayerAuthInput) {
	t.Helper()
	p, _ := soundPlayer(t, version.Newest)
	p.rem.log = cfg.Log
	p.world = newWorld(p.rem.pal, 0)
	p.javaDim = javaDimension(0)
	p.javaLoaded = true
	p.self.mode = GameSurvival
	p.mv.reset(mgl64.Vec3{0.5, 64, 0.5}, 0, 0)
	take := func() []*packet.PlayerAuthInput {
		var out []*packet.PlayerAuthInput
		for _, pk := range p.rem.testOut {
			if in, ok := pk.(*packet.PlayerAuthInput); ok {
				out = append(out, in)
			}
		}
		p.rem.testOut = nil
		return out
	}
	return p, take
}

// javaIn dispatches one serverbound Java packet built by f.
func javaIn(t *testing.T, p *Player, id int32, f func(w *wire.Writer)) {
	t.Helper()
	var w wire.Writer
	if f != nil {
		f(&w)
	}
	if err := p.dispatchJava(id, w.B); err != nil {
		t.Fatalf("java packet %#x: %v", id, err)
	}
}

// tick is one Java client tick: input keys, position (on ground or not), look, tick end.
func tick(t *testing.T, p *Player, keys byte, pos mgl64.Vec3, yaw, pitch float32, onGround bool) {
	t.Helper()
	javaIn(t, p, v777.ServerboundPlayPlayerInput, func(w *wire.Writer) { w.Byte(keys) })
	var flags byte
	if onGround {
		flags = 1
	}
	javaIn(t, p, v777.ServerboundPlayMovePlayerPosRot, func(w *wire.Writer) {
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		w.Float32(yaw)
		w.Float32(pitch)
		w.Byte(flags)
	})
	javaIn(t, p, v777.ServerboundPlayClientTickEnd, nil)
}

func TestMovementInput(t *testing.T) {
	p, take := movePlayer(t)

	// Standing still.
	tick(t, p, 0, mgl64.Vec3{0.5, 64, 0.5}, 0, 0, true)
	in := take()
	if len(in) != 1 {
		t.Fatalf("%d PlayerAuthInputs for one client tick, want 1", len(in))
	}
	first := in[0]
	if got := first.Position; math.Abs(float64(got[1])-(64+eyeHeight)) > 1e-4 || got[0] != 0.5 || got[2] != 0.5 {
		t.Errorf("position %v, want the feet + %.2f", got, eyeHeight)
	}
	if !first.InputData.Load(packet.InputFlagVerticalCollision) {
		t.Error("on ground without VerticalCollision")
	}

	// Walking forward (south, yaw 0): Up, a move vector, the tick's delta, one tick later.
	tick(t, p, keyForward, mgl64.Vec3{0.5, 64, 0.6}, 0, 10, true)
	in = take()
	walk := in[0]
	if walk.Tick != first.Tick+1 {
		t.Errorf("tick %d after %d", walk.Tick, first.Tick)
	}
	if !walk.InputData.Load(packet.InputFlagUp) || walk.MoveVector[1] != 1 {
		t.Errorf("walking: Up %v move %v", walk.InputData.Load(packet.InputFlagUp), walk.MoveVector)
	}
	if d := walk.Delta; math.Abs(float64(d[2])-0.1) > 1e-5 || d[0] != 0 || d[1] != 0 {
		t.Errorf("delta %v, want 0 0 0.1", d)
	}
	if walk.Yaw != 0 || walk.Pitch != 10 || walk.HeadYaw != 0 {
		t.Errorf("look %v/%v head %v", walk.Yaw, walk.Pitch, walk.HeadYaw)
	}

	// Sprint (player_command 1) and jump: the take-off tick has StartJumping.
	javaIn(t, p, v777.ServerboundPlayPlayerCommand, func(w *wire.Writer) { w.VarInt(SelfEntityID); w.VarInt(1); w.VarInt(0) })
	tick(t, p, keyForward|keyJump, mgl64.Vec3{0.5, 64.42, 0.9}, 0, 10, false)
	jump := take()[0]
	for _, f := range []int{packet.InputFlagSprinting, packet.InputFlagStartSprinting, packet.InputFlagJumping, packet.InputFlagStartJumping, packet.InputFlagJumpPressedRaw} {
		if !jump.InputData.Load(f) {
			t.Errorf("sprint jump: flag %d not set", f)
		}
	}
	if jump.InputData.Load(packet.InputFlagVerticalCollision) {
		t.Error("in the air with VerticalCollision")
	}
	tick(t, p, keyForward|keyJump, mgl64.Vec3{0.5, 64.75, 1.2}, 0, 10, false)
	if air := take()[0]; air.InputData.Load(packet.InputFlagStartJumping) || air.InputData.Load(packet.InputFlagStartSprinting) {
		t.Error("StartJumping/StartSprinting repeated in the air")
	}

	// Sneaking: the move vector is scaled like Bedrock's (x0.3), sneak flags on the first tick.
	javaIn(t, p, v777.ServerboundPlayPlayerCommand, func(w *wire.Writer) { w.VarInt(SelfEntityID); w.VarInt(2); w.VarInt(0) })
	tick(t, p, keyForward|keyShift, mgl64.Vec3{0.5, 64, 1.25}, 0, 10, true)
	sneak := take()[0]
	if !sneak.InputData.Load(packet.InputFlagSneaking) || !sneak.InputData.Load(packet.InputFlagStartSneaking) || !sneak.InputData.Load(packet.InputFlagStopSprinting) {
		t.Errorf("sneak flags: sneaking %v start %v stop sprint %v", sneak.InputData.Load(packet.InputFlagSneaking),
			sneak.InputData.Load(packet.InputFlagStartSneaking), sneak.InputData.Load(packet.InputFlagStopSprinting))
	}
	if math.Abs(float64(sneak.MoveVector[1])-0.3) > 1e-6 {
		t.Errorf("sneak move vector %v, want 0.3 forward", sneak.MoveVector)
	}
	tick(t, p, 0, mgl64.Vec3{0.5, 64, 1.25}, 0, 10, true)
	if s := take()[0]; !s.InputData.Load(packet.InputFlagStopSneaking) || s.InputData.Load(packet.InputFlagSneaking) {
		t.Error("releasing sneak: no StopSneaking")
	}
}

// TestSprintEndsWhenCrouching: Java 26 sprints on while crouching, BDS ends the sprint, so the
// bridge ends the Java client's sprint the tick it crouches (a tick after the shift key).
func TestSprintEndsWhenCrouching(t *testing.T) {
	p, take := movePlayer(t)
	javaIn(t, p, v777.ServerboundPlayPlayerCommand, func(w *wire.Writer) { w.VarInt(SelfEntityID); w.VarInt(1); w.VarInt(0) })
	tick(t, p, keyForward, mgl64.Vec3{0.5, 64, 0.8}, 0, 0, true)
	tick(t, p, keyForward|keyShift, mgl64.Vec3{0.5, 64, 1.1}, 0, 0, true)
	if !p.mv.sprinting {
		t.Fatal("sprint ended on the shift key tick (the client crouches a tick later)")
	}
	tick(t, p, keyForward|keyShift, mgl64.Vec3{0.5, 64, 1.2}, 0, 0, true)
	in := take()
	if p.mv.sprinting || !in[len(in)-1].InputData.Load(packet.InputFlagStopSprinting) {
		t.Errorf("crouching: sprinting %v, StopSprinting %v", p.mv.sprinting, in[len(in)-1].InputData.Load(packet.InputFlagStopSprinting))
	}
}

// TestSprintStopOnLanding: BDS ignores a sprint stop in an input that starts in the air, so a
// Java client that stops sprinting in the air (it ran into a block side) is reported sprinting
// until an input that starts on the ground, which carries the stop.
func TestSprintStopOnLanding(t *testing.T) {
	p, take := movePlayer(t)
	javaIn(t, p, v777.ServerboundPlayPlayerCommand, func(w *wire.Writer) { w.VarInt(SelfEntityID); w.VarInt(1); w.VarInt(0) })
	tick(t, p, keyForward|keyJump, mgl64.Vec3{0.5, 64.42, 0.8}, 0, 0, false)
	p.mv.srvSprint = true // the server's attribute: sprinting
	take()
	javaIn(t, p, v777.ServerboundPlayPlayerCommand, func(w *wire.Writer) { w.VarInt(SelfEntityID); w.VarInt(2); w.VarInt(0) })
	for i := range 3 {
		tick(t, p, keyForward|keyJump, mgl64.Vec3{0.5, 64.6, 0.8}, 0, 0, false)
		in := take()[0]
		if !in.InputData.Load(packet.InputFlagSprinting) || in.InputData.Load(packet.InputFlagStopSprinting) {
			t.Errorf("in the air %d after the sprint stop: Sprinting %v StopSprinting %v", i, in.InputData.Load(packet.InputFlagSprinting),
				in.InputData.Load(packet.InputFlagStopSprinting))
		}
	}
	tick(t, p, keyForward|keyJump, mgl64.Vec3{0.5, 64, 0.8}, 0, 0, true)
	if in := take()[0]; !in.InputData.Load(packet.InputFlagSprinting) {
		t.Error("landing input (starts in the air): sprint stop already told")
	}
	tick(t, p, keyForward|keyJump, mgl64.Vec3{0.5, 64.42, 0.8}, 0, 0, false)
	if in := take()[0]; !in.InputData.Load(packet.InputFlagStopSprinting) || in.InputData.Load(packet.InputFlagSprinting) {
		t.Error("the input after the landing (the hop): no StopSprinting")
	}
	tick(t, p, keyForward|keyJump, mgl64.Vec3{0.5, 64.7, 0.8}, 0, 0, false)
	if in := take()[0]; in.InputData.Load(packet.InputFlagStopSprinting) || in.InputData.Load(packet.InputFlagSprinting) {
		t.Error("after the stop: sprint flags again")
	}
}

// TestTeleportWaitsForChunks: a server teleport to where the Java client has no chunks yet is
// given to it only once the columns there were in a chunk batch it acknowledged; until then the
// server gets standing-still inputs at the target.
func TestTeleportWaitsForChunks(t *testing.T) {
	p, take := movePlayer(t)
	to := mgl64.Vec3{1001.5, 60, 1000.5}
	p.teleportTo(to, 0, 0)
	if p.mv.held == nil || len(p.mv.pending) != 0 {
		t.Fatalf("teleport into missing chunks: held %v pending %d", p.mv.held, len(p.mv.pending))
	}
	// The client keeps moving where it was, holding forward: not reported.
	tick(t, p, keyForward, mgl64.Vec3{0.5, 63.9, 0.7}, 0, 0, false)
	in := take()[0]
	if got := vec64(in.Position).Sub(mgl64.Vec3{0, eyeHeight, 0}); got.Sub(to).Len() > 1e-4 || in.MoveVector != [2]float32{} {
		t.Errorf("held: input at %v move %v, want standing at %v", got, in.MoveVector, to)
	}
	if !in.InputData.Load(packet.InputFlagHandledTeleport) {
		t.Error("held: the first input does not confirm the teleport")
	}
	p.releaseTeleport()
	if p.mv.held == nil {
		t.Fatal("released without the chunks")
	}
	// The columns under the player arrive in a batch the client acknowledges.
	p.batchSeq, p.batchAcked = 1, 1
	p.world.cols[ChunkPos{1001 >> 4, 1000 >> 4}] = &column{sent: true, batch: 1}
	p.releaseTeleport()
	if p.mv.held != nil || len(p.mv.pending) != 1 || !p.mv.pending[0].absolute {
		t.Fatalf("after the chunks: held %v pending %v", p.mv.held, p.mv.pending)
	}
}
