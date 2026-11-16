package bridge

import (
	"strings"

	"delta/core/internal/javamap"

	"github.com/df-mc/dragonfly/server/block/cube"
	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Item frames: blocks on Bedrock (minecraft:frame / glow_frame, air for the Java client), entities
// on Java. Each frame block entity of a column the client has becomes an item_frame entity with
// the frame's item and rotation; punching it starts breaking the block (Bedrock takes the item
// out), right clicking it uses the held item on the block.

// frameRuntimeBit marks the made-up runtime ids of frame entities (real ones are small).
const frameRuntimeBit = 1 << 63

// frameRuntimeID is the made-up runtime id of the frame at pos.
func frameRuntimeID(pos cube.Pos) uint64 {
	return frameRuntimeBit | uint64(uint32(pos[0])&0x3ffffff)<<38 | uint64(uint32(pos[2])&0x3ffffff)<<12 | uint64(uint32(pos[1])&0xfff)
}

func init() {
	chunkSentHooks = append(chunkSentHooks, func(p *Player, pos ChunkPos, col *column) {
		for bp, data := range col.be {
			if isFrameNBT(data) {
				p.showFrame(bp, data)
			}
		}
	})
	blockEntityHooks = append(blockEntityHooks, func(p *Player, pos cube.Pos, data map[string]any) {
		if !isFrameNBT(data) || p.world == nil {
			return
		}
		if col := p.world.cols[chunkOf(pos)]; col != nil && col.sent {
			p.showFrame(pos, data)
		}
	})
	chunkForgetHooks = append(chunkForgetHooks, func(p *Player, pos ChunkPos) {
		for bp := range p.ents.frames {
			if chunkOf(bp) == pos {
				p.removeFrame(bp)
			}
		}
	})
	OnBedrock(packet.IDUpdateBlock, func(p *Player, pk *packet.UpdateBlock) {
		pos := cube.Pos{int(pk.Position[0]), int(pk.Position[1]), int(pk.Position[2])}
		if _, ok := p.ents.frames[pos]; !ok || pk.Layer != 0 {
			return
		}
		if !strings.HasSuffix(p.rem.pal.Name(p.rem.pal.ServerToIndex(pk.NewBlockRuntimeID)), "frame") {
			p.removeFrame(pos)
		}
	})
	OnInput(func(p *Player, pk *packet.PlayerAuthInput, flags *protocol.InputFlags) {
		if p.ents.framePunch == nil {
			return
		}
		pos, face := *p.ents.framePunch, p.ents.framePunchFace
		p.ents.framePunch = nil
		bp := protocol.BlockPos{int32(pos[0]), int32(pos[1]), int32(pos[2])}
		actions, _ := pk.BlockActions.Value() // other hooks may have put theirs in
		pk.BlockActions = protocol.Option(append(actions,
			protocol.PlayerBlockAction{Action: protocol.PlayerActionStartBreak, BlockPos: bp, Face: face},
			protocol.PlayerBlockAction{Action: protocol.PlayerActionAbortBreak, BlockPos: bp, Face: face}))
		flags.Set(packet.InputFlagPerformBlockActions)
	})
}

func isFrameNBT(data map[string]any) bool {
	id, _ := data["id"].(string)
	return id == "ItemFrame" || id == "GlowItemFrame"
}

// frameFacing is the facing_direction of the frame block at pos (the way it faces out: the same
// numbers as Java's 3D directions), 1 (up) if unknown.
func (p *Player) frameFacing(pos cube.Pos) int32 {
	if p.world == nil || p.rem == nil {
		return 1
	}
	i, ok := p.world.Block(pos)
	if !ok || int(i) >= len(p.rem.pal.States) {
		return 1
	}
	if f, ok := p.rem.pal.States[i].Properties["facing_direction"].(int32); ok && f >= 0 && f < 6 {
		return f
	}
	return 1
}

// showFrame shows (or updates) the frame at pos.
func (p *Player) showFrame(pos cube.Pos, data map[string]any) {
	java := "minecraft:item_frame"
	if data["id"] == "GlowItemFrame" {
		java = "minecraft:glow_item_frame"
	}
	dir := p.frameFacing(pos)
	if rid, ok := p.ents.frames[pos]; ok {
		if e := p.ents.shown[rid]; e != nil && e.java == java && e.data == dir {
			e.frameNBT = data
			p.sendEntityData(e, false)
			return
		}
		p.removeFrame(pos)
	}
	rid := frameRuntimeID(pos)
	e := &entity{rid: rid, uniq: int64(rid), bedrock: "minecraft:frame", t: entityType{java: java}, java: java,
		uuid: entityUUID(rid), meta: protocol.EntityMetadata{}, data: dir, frameNBT: data}
	e.pos = vec64(mgl32.Vec3{float32(pos[0]) + 0.5, float32(pos[1]) + 0.5, float32(pos[2]) + 0.5})
	if !p.trackEntity(e) {
		return
	}
	if p.ents.frames == nil {
		p.ents.frames = map[cube.Pos]uint64{}
	}
	p.ents.frames[pos] = rid
	p.showEntity(e)
}

// removeFrame removes the frame entity at pos.
func (p *Player) removeFrame(pos cube.Pos) {
	rid, ok := p.ents.frames[pos]
	if !ok {
		return
	}
	delete(p.ents.frames, pos)
	p.removeEntity(rid, false)
}

// frameBlock returns the block of a frame entity, floored properly for negative coordinates.
func frameBlock(e *entity) cube.Pos {
	return cube.Pos{int(mfloor(e.pos[0])), int(mfloor(e.pos[1])), int(mfloor(e.pos[2]))}
}

// useFrame is a Java click on a frame entity: punching starts breaking the block (the server
// takes the item out), using clicks the block with the held item.
func (p *Player) useFrame(e *entity, attack bool) {
	pos := frameBlock(e)
	if attack {
		p.ents.framePunch, p.ents.framePunchFace = &pos, e.data
		return
	}
	var slot int32
	var held protocol.ItemInstance
	if entityHeldItem != nil {
		slot, held = entityHeldItem(p)
	}
	rid := uint32(0)
	if i, ok := p.world.Block(pos); ok {
		rid = p.rem.pal.IndexToServer(i)
	}
	p.Write(&packet.InventoryTransaction{TransactionData: &protocol.UseItemTransactionData{
		ActionType:       protocol.UseItemActionClickBlock,
		TriggerType:      protocol.TriggerTypePlayerInput,
		BlockPosition:    protocol.BlockPos{int32(pos[0]), int32(pos[1]), int32(pos[2])},
		BlockFace:        e.data,
		HotBarSlot:       slot,
		HeldItem:         held,
		Position:         vec32(p.mv.pos.Add(vec64(mgl32.Vec3{0, eyeHeight, 0}))),
		ClickedPosition:  mgl32.Vec3{0.5, 0.5, 0.5},
		BlockRuntimeID:   rid,
		ClientPrediction: protocol.ClientPredictionSuccess,
	}})
}

// frameItem converts the item of a frame's NBT ("Item": Name, Count, Damage).
func frameItem(data map[string]any, js *jitem.Stack) bool {
	js.Reset()
	it, ok := data["Item"].(map[string]any)
	if !ok {
		return false
	}
	name, _ := it["Name"].(string)
	if name == "" || name == "minecraft:air" {
		return false
	}
	var damage int16
	switch v := it["Damage"].(type) {
	case int16:
		damage = v
	case int32:
		damage = int16(v)
	}
	id, _ := javamap.ItemID(name, damage)
	js.ID, js.Count = id, 1
	if tag, ok := it["tag"].(map[string]any); ok {
		if _, ok := tag["ench"]; ok {
			js.EnchantmentGlintOverride = true
			js.Add(jitem.CompEnchantmentGlintOverride)
		}
	}
	return true
}

// frameRotation is a frame's item rotation, 0-7 (Bedrock saves degrees).
func frameRotation(data map[string]any) int32 {
	switch v := data["ItemRotation"].(type) {
	case float32:
		return int32(v/45+0.5) & 7
	case uint8:
		return int32(v) & 7
	case int32:
		return v & 7
	}
	return 0
}
