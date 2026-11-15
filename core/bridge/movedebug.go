package bridge

import (
	"fmt"
	"os"
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// JB_DEBUG_MOVE=1 logs every own PlayerAuthInput (tick, position, delta, the movement flags) and
// every correction in full, for movement tests.
var debugMove = os.Getenv("JB_DEBUG_MOVE") != ""

var moveFlagNames = map[int]string{
	packet.InputFlagJumpDown: "JumpDown", packet.InputFlagSprintDown: "SprintDown", packet.InputFlagJumping: "Jumping",
	packet.InputFlagSneaking: "Sneaking", packet.InputFlagUp: "Up", packet.InputFlagDown: "Down", packet.InputFlagLeft: "Left",
	packet.InputFlagRight: "Right", packet.InputFlagWantUp: "WantUp", packet.InputFlagSprinting: "Sprinting",
	packet.InputFlagStartSprinting: "StartSprint", packet.InputFlagStopSprinting: "StopSprint",
	packet.InputFlagStartJumping: "StartJump", packet.InputFlagHandledTeleport: "HandledTP",
	packet.InputFlagHorizontalCollision: "HColl", packet.InputFlagVerticalCollision: "VColl",
	packet.InputFlagJumpPressedRaw: "JumpPressed", packet.InputFlagStartFlying: "StartFly", packet.InputFlagStopFlying: "StopFly",
	packet.InputFlagWantDown: "WantDown", packet.InputFlagWantDownSlow: "WantDownSlow", packet.InputFlagSneakDown: "SneakDown", packet.InputFlagJumpReleasedRaw: "JumpReleased",
}

func (p *Player) debugInput(pk *packet.PlayerAuthInput) {
	var names []string
	for i := 0; i < packet.InputFlagCount; i++ {
		if !pk.InputData.Load(i) {
			continue
		}
		if n, ok := moveFlagNames[i]; ok {
			names = append(names, n)
		} else {
			names = append(names, fmt.Sprint(i))
		}
	}
	p.log.Info("MV input", "tick", pk.Tick, "pos", fmt.Sprintf("%.4f %.4f %.4f", pk.Position[0], pk.Position[1]-eyeHeight, pk.Position[2]),
		"delta", fmt.Sprintf("%.4f %.4f %.4f", pk.Delta[0], pk.Delta[1], pk.Delta[2]), "yaw", pk.Yaw, "mv", pk.MoveVector, "flags", strings.Join(names, ","))
}
