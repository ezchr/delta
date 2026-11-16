package bridge

import (
	"math"
	"strings"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Items used on a liquid: a Bedrock client aims them at the water (or lava) surface and sends a
// click on the liquid block itself. The Java client aims through liquids (its use_item_on names
// the block under the water, if any, and the item then sends use_item into the air), so the
// bridge finds the liquid along the look itself.

// liquidTarget says what a held item aims at: "" nothing special, "source" a liquid source (empty
// bucket), "water" any water (boats, lily pads, frog spawn).
func liquidTarget(item string) string {
	n := strings.TrimPrefix(item, "minecraft:")
	switch {
	case n == "bucket":
		return "source"
	case n == "waterlily", n == "frog_spawn", strings.HasSuffix(n, "_boat"), strings.HasSuffix(n, "_raft"):
		return "water"
	}
	return ""
}

// liquidAim finds the liquid block the player looks at for an item aiming at want, closer than
// limit (the distance of the block the Java client clicked, or the reach): the block, and where
// on its top the look meets it.
func (p *Player) liquidAim(want string, limit float64) (protocol.BlockPos, mgl32.Vec3, bool) {
	w := p.world
	if w == nil || want == "" {
		return protocol.BlockPos{}, mgl32.Vec3{}, false
	}
	eye := p.eyePos()
	yaw, pitch := float64(p.mv.yaw)*math.Pi/180, float64(p.mv.pitch)*math.Pi/180
	dir := mgl64.Vec3{-math.Sin(yaw) * math.Cos(pitch), -math.Sin(pitch), math.Cos(yaw) * math.Cos(pitch)}
	for d := 0.0; d <= limit; d += 0.05 {
		at := eye.Add(dir.Mul(d))
		pos := cube.PosFromVec3(at)
		if pos.OutOfBounds(w.r) {
			break
		}
		b, ok := w.Block(pos)
		if !ok || w.pal.IsAir(b) {
			continue
		}
		name := w.pal.Name(b)
		water := w.pal.IsWater(b)
		lava := name == "minecraft:lava" || name == "minecraft:flowing_lava"
		if !water && !lava {
			return protocol.BlockPos{}, mgl32.Vec3{}, false // a block before any liquid
		}
		if want == "water" && !water {
			return protocol.BlockPos{}, mgl32.Vec3{}, false
		}
		if want == "source" {
			if depth, _ := w.pal.States[b].Properties["liquid_depth"].(int32); depth != 0 {
				continue // flowing liquid: a bucket goes on to a source behind it
			}
		}
		f := at.Sub(pos.Vec3())
		return protocol.BlockPos{int32(pos[0]), int32(pos[1]), int32(pos[2])}, mgl32.Vec3{float32(f[0]), 1, float32(f[2])}, true
	}
	return protocol.BlockPos{}, mgl32.Vec3{}, false
}

// reach is the block reach used for aiming at liquids.
func (p *Player) reach() float64 {
	if p.self.mode == GameCreative {
		return bedrockCreativeRange
	}
	return bedrockBlockRange
}
