package bridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/java/protocol/text"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Command blocks (2026-11-10).
//
// Bedrock → Java view. The three Bedrock command blocks are Java's (the block states, facing and
// conditional, come from the palette's state table) and are listed as minecraft:command_block
// block entities (internal/blocks), so a Java client has a CommandBlockEntity to open. Their data
// (beNBT → writeCommandBlock) goes in each column and as block_entity_data on BlockActorData:
// Command, CustomName (plain text), SuccessCount, TrackOutput, LastOutput (the Bedrock key with
// LastOutputParams translated, only while tracking, as Java loads it), powered, conditionMet and
// auto (Bedrock's auto; its LP* fields stay 0 whatever the mode).
//
// Opening. A 26.3 client opens the edit screen itself (CommandBlock.useWithoutItem) when it is in
// creative with op level ≥ 2 (Player.canUseGameMasterBlocks), unless it sneaks holding an item;
// the screen starts with its controls off and fills in (updateGui) only when block_entity_data
// for that block comes while it is open, which a vanilla server sends on the use
// (ServerPlayer.openCommandBlock). On Bedrock the editor must pass Player::canUseOperatorBlocks:
// the operator commands ability (UpdateAbilities; BDS sets instabuild in no layer, it follows the
// creative game type) in creative; BDS answers the click of such a player with ContainerOpen type
// CommandBlock. So on the Java use_item_on of a command block that opened the screen: a player
// the Bedrock server lets edit gets the block's data at once; anyone else gets container_close,
// which shuts the screen (LocalPlayer.clientSideCloseContainer): self.go gives every player op
// level 2 so the client takes target selectors, which alone would let non-operators open it. The
// click still goes to the server as a Bedrock client sends it (interact.go); the server's
// ContainerOpen is answered with the data again and a ContainerClose right away (the Java screen
// is no container and tells the server nothing when it closes; BDS takes CommandBlockUpdate
// without an open window).
//
// Java → Bedrock edit. set_command_block (pos, command, mode SEQUENCE/AUTO/REDSTONE, flags
// 1 track output, 2 conditional, 4 automatic) → CommandBlockUpdate as the Bedrock UI sends it:
// Block, the position, mode impulse/repeating/chain, NeedsRedstone = not automatic, Conditional,
// the command, the shown LastOutput (empty when not tracking), the block's Name, TickDelay and
// ExecuteOnFirstTick kept from its data. BDS checks canUseOperatorBlocks and answers an accepted
// edit with BlockActorData (and an UpdateBlock for a new mode/condition); the bridge then says
// "Command set: ..." as a Java server does (or the "disabled" text while the game rule
// commandblocksenabled is off). No answer within 2 s: refused; the cached data goes back to the
// client (its screen changed TrackOutput/LastOutput locally) and, for a player the server does not
// let edit, "advMode.notAllowed".
//
// Command block minecarts are not translated (no entity data, no set_command_minecart).

// cmdBlockState is the player's command block state (p.mu).
type cmdBlockState struct {
	rem *Remote // the connection the state is for
	// opCommands: the operator commands ability (with creative: Bedrock canUseOperatorBlocks).
	opCommands bool
	// enabled: the game rule commandblocksenabled.
	enabled bool
	pending map[cube.Pos]cmdBlockEdit
	// shown, shownAt: the block whose data went to the client on its use, and when.
	shown   cube.Pos
	shownAt time.Time
}

// cmdBlockEdit is an edit sent to the server and not answered yet.
type cmdBlockEdit struct {
	command string
	at      time.Time
}

const cmdBlockType = "minecraft:command_block"

// cmdBlock is the player's command block state for the current connection.
func (p *Player) cmdBlock() *cmdBlockState {
	s := &p.cmdb
	if s.rem != p.rem {
		*s = cmdBlockState{rem: p.rem, enabled: true, pending: map[cube.Pos]cmdBlockEdit{}}
		if p.rem != nil {
			s.opCommands = p.rem.gd.PlayerPermissions >= 2 // until UpdateAbilities
			s.setRules(p.rem.gd.GameRules)
		}
	}
	return s
}

// mayEditCommandBlocks reports whether the server lets the player edit command blocks
// (canUseOperatorBlocks: operator commands, creative).
func (p *Player) mayEditCommandBlocks() bool {
	return p.cmdBlock().opCommands && p.self.mode == GameCreative
}

func (s *cmdBlockState) setRules(rules []protocol.GameRule) {
	for _, r := range rules {
		if strings.EqualFold(r.Name, "commandblocksenabled") {
			if v, ok := r.Value.(bool); ok {
				s.enabled = v
			}
		}
	}
}

// abilityValue is an ability's value from the layers as LayeredAbilities reads it: the base
// layer's, overridden by any other layer that sets it.
func abilityValue(layers []protocol.AbilityLayer, bit uint32) bool {
	v := false
	for _, l := range layers {
		if l.Type == protocol.AbilityLayerTypeBase {
			v = l.Values&bit != 0
		}
	}
	for _, l := range layers {
		if l.Type != protocol.AbilityLayerTypeBase && l.Type != protocol.AbilityLayerTypeCustomCache && l.Abilities&bit != 0 {
			v = l.Values&bit != 0
		}
	}
	return v
}

// commandBlockAt is the cached Bedrock data of the command block at pos (ok: there is one).
func (p *Player) commandBlockAt(pos cube.Pos) (map[string]any, bool) {
	w := p.world
	if w == nil || pos.OutOfBounds(w.r) {
		return nil, false
	}
	idx, ok := w.Block(pos)
	if !ok || w.pal.BlockEntity(idx) != cmdBlockType {
		return nil, false
	}
	var be map[string]any
	if col := w.cols[chunkOf(pos)]; col != nil {
		be = col.be[pos]
	}
	if be == nil {
		be = map[string]any{}
	}
	return be, true
}

// writeCommandBlock writes the fields of a Java command block entity (BaseCommandBlock.save and
// CommandBlockEntity.saveAdditional) from Bedrock's.
func writeCommandBlock(w *wire.Writer, be map[string]any) {
	cmd, _ := be["Command"].(string)
	text.StringTag(w, "Command", cmd)
	n, _ := nbtNum(be["SuccessCount"])
	text.IntTag(w, "SuccessCount", int32(n))
	if name, _ := be["CustomName"].(string); name != "" {
		text.StringTag(w, "CustomName", name)
	}
	track := true
	if _, ok := be["TrackOutput"]; ok {
		track = nbtBool(be["TrackOutput"])
	}
	text.BoolTag(w, "TrackOutput", track)
	if out := commandBlockOutput(be); track && out != "" {
		text.StringTag(w, "LastOutput", out)
	}
	text.BoolTag(w, "powered", nbtBool(be["powered"]))
	text.BoolTag(w, "conditionMet", nbtBool(be["conditionMet"]))
	text.BoolTag(w, "auto", commandBlockAuto(be))
}

// commandBlockAuto is Java's auto ("Always Active") of a Bedrock command block.
func commandBlockAuto(be map[string]any) bool {
	return nbtBool(be["auto"])
}

// commandBlockOutput is a Bedrock command block's last output in English.
func commandBlockOutput(be map[string]any) string {
	out, _ := be["LastOutput"].(string)
	if out == "" {
		return ""
	}
	var params []string
	list, _ := be["LastOutputParams"].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok {
			params = append(params, s)
		}
	}
	return translate(out, params)
}

// sendCommandBlock sends the command block's data at pos as block_entity_data.
func (p *Player) sendCommandBlock(pos cube.Pos, be map[string]any) {
	p.sendBlockEntityData(pos, cmdBlockType, p.beNBT(cmdBlockType, be))
}

// javaOpensCommandBlock reports whether the Java client opened the command block screen on its
// use_item_on (MultiPlayerGameMode.performUseItemOn + CommandBlock.useWithoutItem).
func (p *Player) javaOpensCommandBlock() bool {
	if p.self.mode != GameCreative {
		return false
	}
	if lvl := p.self.opLevel; lvl != 0 && lvl < 2 {
		return false
	}
	if p.mv.keys&keyShift != 0 { // isSecondaryUseActive
		holding := p.HeldItem().Stack.Count > 0 || p.win.auth[slotKey{protocol.ContainerOffhand, 0}].Stack.Count > 0
		if holding {
			return false
		}
	}
	return true
}

// cmdBlockModes maps the Java mode (CommandBlockEntity.Mode: SEQUENCE, AUTO, REDSTONE) to Bedrock's.
var cmdBlockModes = [3]uint32{packet.CommandBlockChain, packet.CommandBlockRepeating, packet.CommandBlockImpulse}

// commandBlockUpdate is the CommandBlockUpdate a Bedrock client sends for the Java edit.
func commandBlockUpdate(pos cube.Pos, be map[string]any, command string, mode int32, flags byte) *packet.CommandBlockUpdate {
	track := flags&1 != 0
	name, _ := be["CustomName"].(string)
	out := ""
	if track {
		out, _ = be["LastOutput"].(string)
	}
	delay, _ := nbtNum(be["TickDelay"])
	return &packet.CommandBlockUpdate{
		Block:              true,
		Position:           protocol.BlockPos{int32(pos[0]), int32(pos[1]), int32(pos[2])},
		Mode:               cmdBlockModes[mode],
		NeedsRedstone:      flags&4 == 0,
		Conditional:        flags&2 != 0,
		Command:            command,
		LastOutput:         out,
		Name:               name,
		ShouldTrackOutput:  track,
		TickDelay:          uint32(max(delay, 0)),
		ExecuteOnFirstTick: nbtBool(be["ExecuteOnFirstTick"]),
	}
}

func init() {
	OnBedrock(packet.IDUpdateAbilities, func(p *Player, pk *packet.UpdateAbilities) {
		if p.rem == nil || pk.AbilityData.EntityUniqueID != p.rem.gd.EntityUniqueID {
			return
		}
		s := p.cmdBlock()
		l := pk.AbilityData.Layers
		s.opCommands = abilityValue(l, protocol.AbilityOperatorCommands)
		p.log.Debug("command block permission", "opCommands", s.opCommands, "perm", pk.AbilityData.PlayerPermissions,
			"cmdPerm", pk.AbilityData.CommandPermissions, "layers", fmt.Sprintf("%+v", l))
	})
	OnBedrockStop(packet.IDContainerOpen, func(p *Player, pk *packet.ContainerOpen) bool {
		if pk.ContainerType != protocol.ContainerTypeCommandBlock {
			return false
		}
		pos := cube.Pos{int(pk.ContainerPosition[0]), int(pk.ContainerPosition[1]), int(pk.ContainerPosition[2])}
		s := p.cmdBlock()
		if be, ok := p.commandBlockAt(pos); ok && pk.ContainerEntityUniqueID == -1 && (s.shown != pos || time.Since(s.shownAt) > time.Second) {
			p.sendCommandBlock(pos, be) // the client's open was not seen as one (or long ago)
		}
		p.Write(&packet.ContainerClose{WindowID: pk.WindowID, ContainerType: pk.ContainerType})
		p.log.Debug("command block window", "pos", pos, "window", pk.WindowID, "entity", pk.ContainerEntityUniqueID)
		return true
	})
	OnBedrock(packet.IDGameRulesChanged, func(p *Player, pk *packet.GameRulesChanged) {
		p.cmdBlock().setRules(pk.GameRules)
	})
	OnJava(v777.ServerboundPlayUseItemOn, func(p *Player, r *wireReader) error {
		hand := r.VarInt()
		x, y, z := r.Position()
		if r.Err != nil || p.rem == nil || p.inWaiting || hand != 0 {
			return r.Err
		}
		pos := cube.Pos{x, y, z}
		be, ok := p.commandBlockAt(pos)
		if !ok || !p.javaOpensCommandBlock() {
			return nil
		}
		if p.mayEditCommandBlocks() {
			p.sendCommandBlock(pos, be)
			s := p.cmdBlock()
			s.shown, s.shownAt = pos, time.Now()
			return nil
		}
		if p.win.cur == nil {
			wr := p.pk()
			wr.VarInt(0)
			p.send(v777.ClientboundPlayContainerClose, wr)
		}
		return nil
	})
	OnJava(v777.ServerboundPlaySetCommandBlock, func(p *Player, r *wireReader) error {
		x, y, z := r.Position()
		command := r.String(32767)
		mode := r.VarInt()
		flags := r.Byte()
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		pos := cube.Pos{x, y, z}
		be, ok := p.commandBlockAt(pos)
		if !ok || mode < 0 || mode > 2 {
			return nil
		}
		p.Write(commandBlockUpdate(pos, be, command, mode, flags))
		p.cmdBlock().pending[pos] = cmdBlockEdit{command: command, at: time.Now()}
		p.log.Debug("command block edit", "pos", pos, "command", command, "mode", mode, "flags", flags)
		return nil
	})
	blockEntityHooks = append(blockEntityHooks, func(p *Player, pos cube.Pos, data map[string]any) {
		s := p.cmdBlock()
		e, ok := s.pending[pos]
		if !ok {
			return
		}
		delete(s.pending, pos)
		if cmd, _ := data["Command"].(string); cmd == e.command && cmd != "" {
			key := "advMode.setCommand.success"
			if !s.enabled {
				key = "advMode.setCommand.disabled"
			}
			c := text.Translatable(key, text.Plain(cmd))
			p.sendSystem(&c, false)
		}
	})
	OnTick(func(p *Player) {
		s := &p.cmdb
		if s.rem != p.rem || len(s.pending) == 0 {
			return
		}
		for pos, e := range s.pending {
			if time.Since(e.at) < 2*time.Second {
				continue
			}
			delete(s.pending, pos)
			if be, ok := p.commandBlockAt(pos); ok {
				p.sendCommandBlock(pos, be)
			}
			if !p.mayEditCommandBlocks() {
				c := text.Translatable("advMode.notAllowed")
				p.sendSystem(&c, false)
			}
			p.log.Debug("command block edit refused", "pos", pos)
		}
	})
}
