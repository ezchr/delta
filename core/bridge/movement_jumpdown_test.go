package bridge

import (
	"testing"

	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// A held jump key says JumpDown only for a player that may not fly: BDS toggles flight itself on
// a double JumpDown, on top of the Java client's own toggle.
func TestJumpDownOnlyWithoutFlight(t *testing.T) {
	for _, c := range []struct {
		name     string
		mode     int32
		mayFly   bool
		jumpDown bool
	}{
		{"survival", GameSurvival, false, true},
		{"survival that may fly", GameSurvival, true, false},
		{"creative", GameCreative, false, false},
	} {
		p, take := movePlayer(t)
		p.self.mode, p.self.mayFly = c.mode, c.mayFly
		tick(t, p, keyJump, mgl64.Vec3{0.5, 64, 0.5}, 0, 0, true)
		in := take()
		if len(in) != 1 {
			t.Fatalf("%s: %d inputs, want 1", c.name, len(in))
		}
		f := in[0].InputData
		if got := f.Load(packet.InputFlagJumpDown); got != c.jumpDown {
			t.Errorf("%s: JumpDown %v, want %v", c.name, got, c.jumpDown)
		}
		if !f.Load(packet.InputFlagJumping) || !f.Load(packet.InputFlagWantUp) {
			t.Errorf("%s: jump key without Jumping and WantUp", c.name)
		}
	}
}
