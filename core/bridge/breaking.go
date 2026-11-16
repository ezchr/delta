package bridge

import (
	"math"
	"strings"

	"delta/core/internal/blocks"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Survival block breaking the way a Bedrock client does it. Servers with server authoritative
// block breaking (BDS) tick the breaking progress themselves on every PlayerAuthInput and check
// the client's PredictDestroyBlock against it, so the bridge counts the same progress (hardness,
// tool, Efficiency, Haste/Conduit Power, Mining Fatigue, water, ground: Dragonfly's BreakDuration,
// which follows Bedrock) and sends the block actions a Bedrock client sends: StartBreak, then
// CrackBreak every tick with the face, then PredictDestroyBlock and StopBreak in the tick the
// progress is full (with the held tool's MineBlock request), whatever the Java client's own timing.
// A Java client that finishes first waits for it (its block_changed_ack is held back).

// Bedrock effect ids the break speed depends on.
const (
	effectHaste         = 3
	effectMiningFatigue = 4
	effectConduitPower  = 26
)

// toolOf is a held item as a tool: its kind (blocks.ToolPickaxe..., -1: not a tool), harvest
// level and mining speed.
func toolOf(name string) (kind, level int, speed float64) {
	n := strings.TrimPrefix(name, "minecraft:")
	if n == "shears" {
		return blocks.ToolShears, 1, 1.5
	}
	kind = -1
	for k, s := range []string{"_pickaxe", "_axe", "_shovel", "_hoe", "_sword"} {
		if strings.HasSuffix(n, s) {
			kind = k
			n = strings.TrimSuffix(n, s)
		}
	}
	if kind < 0 {
		return -1, 0, 1
	}
	switch n {
	case "wooden":
		return kind, 1, 2
	case "golden":
		return kind, 1, 12
	case "stone":
		return kind, 2, 4
	case "copper":
		return kind, 2, 5
	case "iron":
		return kind, 3, 6
	case "diamond":
		return kind, 4, 8
	case "netherite":
		return kind, 4, 9
	}
	return -1, 0, 1
}

// shearsSpeed is the mining speed of shears on a block they are effective on.
func shearsSpeed(block string) float64 {
	n := strings.TrimPrefix(block, "minecraft:")
	switch {
	case n == "web" || n == "cobweb" || strings.HasSuffix(n, "leaves"):
		return 15
	case strings.HasSuffix(n, "wool"):
		return 5
	case n == "vine" || n == "glow_lichen":
		return 2
	}
	return 1.5
}

// enchantLevel is the level of Bedrock enchantment id on a stack (0: none).
func enchantLevel(st protocol.ItemStack, id int) int {
	for _, e := range stackEnchants(st) {
		if e.id == id {
			return e.lvl
		}
	}
	return 0
}

// effectLevel is the level (amplifier + 1) of one of the player's own effects (0: none).
func (p *Player) effectLevel(id int32) int {
	if e, ok := p.ents.effects[id]; ok {
		return int(e.amplifier) + 1
	}
	return 0
}

// breakRate is the breaking progress per tick on a block (1 or more: it breaks at once, 0: never),
// and false if the block's data is not known (custom blocks: the Java client's timing is used).
func (p *Player) breakRate(pos protocol.BlockPos) (float64, bool) {
	if p.world == nil {
		return 0, false
	}
	b, ok := p.world.Block(cube.Pos{int(pos[0]), int(pos[1]), int(pos[2])})
	if !ok {
		return 0, false
	}
	name := p.rem.pal.Name(b)
	info, ok := blocks.Break(name)
	if !ok {
		return 0, false
	}
	switch {
	case info.Hardness < 0:
		return 0, true
	case info.Hardness == 0:
		return 1, true
	}
	held := p.HeldItem().Stack
	kind, level, speed := toolOf(p.stackName(held))
	harvest := info.Hand || (kind >= 0 && info.MinTier[kind] > 0 && level >= int(info.MinTier[kind]))
	s := 1.0
	if kind >= 0 && info.Effective&(1<<kind) != 0 {
		switch kind {
		case blocks.ToolSword:
			s = 1.5
			if n := strings.TrimPrefix(name, "minecraft:"); n == "web" || n == "cobweb" {
				s = 15
			}
		case blocks.ToolShears:
			s = shearsSpeed(name)
		default:
			s = speed
		}
		if !harvest {
			s = 1 // the right kind of tool of too low a tier is no faster on Bedrock
		} else if e := enchantLevel(held, 15); e > 0 { // efficiency
			s += float64(e*e + 1)
		}
	}
	haste := max(p.effectLevel(effectHaste), p.effectLevel(effectConduitPower))
	fatigue := p.effectLevel(effectMiningFatigue)
	if haste > 0 {
		s *= 0.2*float64(haste) + 1
	}
	if fatigue > 0 {
		s *= math.Pow(0.3, float64(fatigue))
	}
	if p.underwater() && enchantLevel(p.win.auth[slotKey{protocol.ContainerArmor, 0}].Stack, 8) == 0 { // aqua affinity
		s /= 5
	}
	if !p.mv.onGround && !p.self.flying {
		s /= 5
	}
	d := s / info.Hardness
	if harvest {
		d /= 30
	} else {
		d /= 100
	}
	if haste > 0 {
		d *= math.Pow(1.2, float64(haste))
	}
	if fatigue > 0 {
		d *= math.Pow(0.7, float64(fatigue))
	}
	return d, true
}

// underwater reports whether the player's eyes are in water.
func (p *Player) underwater() bool {
	if p.world == nil {
		return false
	}
	e := p.eyePos()
	b, ok := p.world.Block(cube.Pos{int(math.Floor(e[0])), int(math.Floor(e[1])), int(math.Floor(e[2]))})
	return ok && strings.HasSuffix(p.rem.pal.Name(b), "water")
}

// mineRequest is the MineBlock item stack request a Bedrock client sends with the destroy of a
// block when it holds something that wears (nil otherwise).
func (w *Windows) mineRequest() *protocol.ItemStackRequest {
	p := w.p
	k := slotKey{protocol.ContainerHotBar, byte(w.held)}
	inst := w.auth[k]
	maxD := p.maxDamage(inst.Stack)
	if maxD <= 0 || !filled(inst.Stack) {
		return nil
	}
	q := w.newRequest()
	dmg := damageOf(inst.Stack) + 1
	q.actions = append(q.actions, &protocol.MineBlockStackRequestAction{HotbarSlot: int32(w.held), PredictedDurability: int32(max(0, maxD-dmg)), StackNetworkID: inst.StackNetworkID})
	after := withDamage(inst.Stack, dmg)
	w.track(q, map[slotKey]protocol.ItemInstance{k: {StackNetworkID: q.id, Stack: after}})
	return &protocol.ItemStackRequest{RequestID: q.id, Actions: q.actions}
}

// breakTick counts one tick of the survival breaking in progress (from the PlayerAuthInput hook,
// before the block actions go out): CrackBreak while it lasts, the destroy once it is full.
func (p *Player) breakTick() {
	a := &p.win.act
	if n := a.next; n != nil && !a.breaking {
		// The block the Java client went on to, in the input after the last one's destroy (BDS
		// ticks the block started last in an input: a start next to a destroy loses the destroy).
		a.next = nil
		p.startBreak(n.pos, n.face)
		a.javaDone = a.breaking && n.javaDone
	}
	if !a.breaking || p.self.mode == GameCreative {
		return
	}
	if p.world != nil {
		if b, ok := p.world.Block(cube.Pos{int(a.breakPos[0]), int(a.breakPos[1]), int(a.breakPos[2])}); ok && p.rem.pal.Name(b) == "minecraft:air" {
			a.breaking, a.javaDone = false, false // gone already (the server broke it, or something else did)
			return
		}
	}
	rate, known := p.breakRate(a.breakPos)
	if known {
		a.progress += rate
		if rate > 0 && a.progress >= 1 {
			p.destroyBlock()
			return
		}
	}
	if n := len(a.blockActions); n == 0 || a.blockActions[n-1].Action != protocol.PlayerActionStartBreak {
		a.blockActions = append(a.blockActions, protocol.PlayerBlockAction{Action: protocol.PlayerActionCrackBreak, BlockPos: a.breakPos, Face: a.breakFace})
	}
}

// destroyBlock ends the breaking in progress: PredictDestroyBlock and StopBreak, with the held
// tool's MineBlock request in the same input.
func (p *Player) destroyBlock() {
	a := &p.win.act
	a.blockActions = append(a.blockActions,
		protocol.PlayerBlockAction{Action: protocol.PlayerActionPredictDestroyBlock, BlockPos: a.breakPos, Face: a.breakFace},
		protocol.PlayerBlockAction{Action: protocol.PlayerActionStopBreak, BlockPos: a.breakPos, Face: a.breakFace})
	if p.self.mode != GameCreative {
		a.mine = p.win.mineRequest()
	}
	p.log.Debug("predict destroy", "pos", a.breakPos, "progress", a.progress, "javaDone", a.javaDone, "mine", a.mine != nil)
	a.breaking, a.javaDone = false, false // a queued next block starts with the next input
}

// breakJob is a block the Java client started breaking while the last one was still being broken
// on the Bedrock side.
type breakJob struct {
	pos      protocol.BlockPos
	face     int32
	javaDone bool
}

// startBreak starts breaking a block: StartBreak in the next input (creative: destroyed with it).
func (p *Player) startBreak(pos protocol.BlockPos, face int32) {
	a := &p.win.act
	a.blockActions = append(a.blockActions, protocol.PlayerBlockAction{Action: protocol.PlayerActionStartBreak, BlockPos: pos, Face: face})
	a.breaking, a.breakPos, a.breakFace, a.progress, a.javaDone = true, pos, face, 0, false
	if p.self.mode == GameCreative {
		a.blockActions = append(a.blockActions, protocol.PlayerBlockAction{Action: protocol.PlayerActionPredictDestroyBlock, BlockPos: pos, Face: face})
		a.breaking = false
		return
	}
	if rate, ok := p.breakRate(pos); ok && rate >= 1 {
		p.destroyBlock() // breaks in the tick it starts (no hardness, or fast enough)
	}
}
