package bridge

import (
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Blocks and items in the world (milestone 3): breaking (Java player_action -> the block actions
// of the next PlayerAuthInput), placing and using items on blocks (use_item_on -> StartItemUseOn,
// the click transaction, StopItemUseOn after the next input, as a Bedrock client sends them),
// using items in the air and releasing them (eating, bows, throwing), dropping from the hand,
// swapping hands, picking blocks, sign editing, and other players' block cracking.
//
// The Java client predicts the blocks it breaks and places and waits for block_changed_ack of
// the action's sequence: then it takes the server's block (the block updates that came while it
// waited, else the block from before). The bridge acknowledges once the server sent a block
// update for one of the action's positions, or after ackTimeout without one (a refused action:
// the client puts the old block back).

// ackTimeout is how long an action waits for the server's block update before it is acknowledged.
const ackTimeout = 600 * time.Millisecond

// actState is the player's block and item actions in progress.
type actState struct {
	ackSeq     int32
	ackPending bool
	ackAt      time.Time
	ackPos     map[protocol.BlockPos]bool

	blockActions []protocol.PlayerBlockAction
	breaking     bool
	breakPos     protocol.BlockPos
	liquidUsed   bool // a use_item_on became a click on a liquid: skip the use_item after it
	breakFace    int32
	// Survival breaking (breaking.go): the progress counted as a Bedrock client counts it; javaDone:
	// the Java client finished first and waits for it.
	progress float64
	javaDone bool
	mine     *protocol.ItemStackRequest // the MineBlock request to send with the destroy
	next     *breakJob                  // the block the Java client went on to (breaking.go)

	legacyID int32
	cracks   map[protocol.BlockPos]*crack
}

// crack is another player's breaking of a block (LevelEvent block cracking).
type crack struct {
	id       int32
	progress float64
	rate     float64 // per tick
	stage    int
}

// javaAction maps a player_action of a client protocol to the 26.2 numbering below: 26.3 (777)
// inserted CHANGE_DESTROY_DIRECTION at 1, moving the rest up by one. ok is false for actions the
// numbering has no constant for (26.3's CHANGE_DESTROY_DIRECTION, STAB).
func javaAction(protocol int32, a int32) (action int32, ok bool) {
	if protocol >= 777 {
		switch {
		case a == 1:
			return 0, false
		case a > 1:
			a--
		}
	}
	return a, a >= digStart && a <= swapHands
}

// Java player_action actions (26.2 numbering).
const (
	digStart = iota
	digAbort
	digFinish
	dropStack
	dropOne
	releaseUse
	swapHands
)

func init() {
	OnJava(v777.ServerboundPlayPlayerAction, func(p *Player, r *wireReader) error {
		raw := r.VarInt()
		x, y, z := r.Position()
		face := int32(r.Byte())
		seq := r.VarInt()
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		pos := protocol.BlockPos{int32(x), int32(y), int32(z)}
		action, ok := javaAction(p.ver().Protocol, raw)
		p.log.Debug("java player_action", "raw", raw, "action", action, "known", ok, "pos", pos, "face", face)
		if !ok {
			if raw == 1 { // CHANGE_DESTROY_DIRECTION: the same block, hit on another face
				if a := &p.win.act; a.breaking && a.breakPos == pos {
					a.breakFace = face
				}
				p.awaitAck(seq)
			}
			return nil
		}
		if action == digStart {
			p.win.closeStrayInventory()
		}
		p.playerAction(action, pos, face)
		if action <= digFinish {
			p.awaitAck(seq, pos)
		}
		return nil
	})
	OnJava(v777.ServerboundPlayUseItemOn, func(p *Player, r *wireReader) error {
		hand := r.VarInt()
		x, y, z := r.Position()
		face := r.VarInt()
		cx, cy, cz := r.Float32(), r.Float32(), r.Float32()
		r.Bool() // inside block
		r.Bool() // world border hit
		seq := r.VarInt()
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		pos := protocol.BlockPos{int32(x), int32(y), int32(z)}
		if hand != 0 || face < 0 || face > 5 {
			p.awaitAck(seq) // Bedrock has no off-hand use
			return nil
		}
		side := cube.Pos{x, y, z}.Side(cube.Face(face))
		p.win.closeStrayInventory()
		if want := liquidTarget(p.itemName(p.HeldItem().Stack.NetworkID)); want != "" {
			hit := mgl64.Vec3{float64(x) + float64(cx), float64(y) + float64(cy), float64(z) + float64(cz)}
			if lp, click, ok := p.liquidAim(want, hit.Sub(p.eyePos()).Len()); ok {
				p.useItemOn(lp, 1, click)
				p.win.act.liquidUsed = true // the use_item that follows is this click
				p.awaitAck(seq, pos, protocol.BlockPos{int32(side[0]), int32(side[1]), int32(side[2])})
				return nil
			}
		}
		p.useItemOn(pos, face, mgl32.Vec3{cx, cy, cz})
		p.awaitAck(seq, pos, protocol.BlockPos{int32(side[0]), int32(side[1]), int32(side[2])})
		return nil
	})
	OnJava(v777.ServerboundPlayUseItem, func(p *Player, r *wireReader) error {
		hand := r.VarInt()
		seq := r.VarInt()
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		if hand == 0 {
			p.win.closeStrayInventory()
			if a := &p.win.act; a.liquidUsed {
				a.liquidUsed = false
			} else if want := liquidTarget(p.itemName(p.HeldItem().Stack.NetworkID)); want == "" {
				p.useItem()
			} else if lp, click, ok := p.liquidAim(want, p.reach()); ok {
				p.useItemOn(lp, 1, click)
			} else {
				p.useItem()
			}
		}
		p.awaitAck(seq)
		return nil
	})
	OnJava(v777.ServerboundPlayPickItemFromBlock, func(p *Player, r *wireReader) error {
		x, y, z := r.Position()
		data := r.Bool()
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		p.Write(&packet.BlockPickRequest{Position: protocol.BlockPos{int32(x), int32(y), int32(z)}, AddBlockNBT: data, HotBarSlot: byte(p.win.held)})
		return nil
	})
	OnJava(v777.ServerboundPlaySignUpdate, func(p *Player, r *wireReader) error {
		x, y, z := r.Position()
		// 26.2: front (bool), then the lines; 26.3: the lines, then the side (VarInt, 1 front).
		v263 := p.ver().Protocol >= 777
		var front bool
		if !v263 {
			front = r.Bool()
		}
		var lines [4]string
		for i := range lines {
			lines[i] = r.String(384)
		}
		if v263 {
			front = r.VarInt() == 1
		}
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		p.editSign(cube.Pos{x, y, z}, front, strings.TrimRight(strings.Join(lines[:], "\n"), "\n"))
		return nil
	})
	OnBedrock(packet.IDOpenSign, func(p *Player, pk *packet.OpenSign) {
		wr := p.pk()
		wr.Position(int(pk.Position[0]), int(pk.Position[1]), int(pk.Position[2]))
		wr.Bool(pk.FrontSide)
		p.send(v777.ClientboundPlayOpenSignEditor, wr)
	})
	OnBedrock(packet.IDLevelEvent, (*Player).crackEvent)
	OnBedrock(packet.IDBlockEvent, (*Player).blockEvent)
	// A block update the server sent for a position an action waits on: acknowledge it next tick
	// (after the update went to the client).
	blockAck := func(p *Player, pos protocol.BlockPos) {
		if a := &p.win.act; a.ackPending && a.ackPos[pos] {
			a.ackAt = time.Time{}
		}
	}
	OnBedrock(packet.IDUpdateBlock, func(p *Player, pk *packet.UpdateBlock) { blockAck(p, pk.Position) })
	OnBedrock(packet.IDUpdateBlockSynced, func(p *Player, pk *packet.UpdateBlockSynced) { blockAck(p, pk.Position) })
	OnBedrock(packet.IDUpdateSubChunkBlocks, func(p *Player, pk *packet.UpdateSubChunkBlocks) {
		for _, e := range pk.Blocks {
			blockAck(p, e.BlockPos)
		}
	})
	OnTick(func(p *Player) {
		a := &p.win.act
		if a.breaking && a.javaDone && a.ackPending {
			a.ackAt = time.Now().Add(ackTimeout) // the client's finish waits for the Bedrock progress
		}
		if a.ackPending && time.Now().After(a.ackAt) {
			a.ackPending = false
			clear(a.ackPos)
			wr := p.pk()
			wr.VarInt(a.ackSeq)
			p.send(v777.ClientboundPlayBlockChangedAck, wr)
		}
		p.crackTick()
	})
	OnInput(func(p *Player, pk *packet.PlayerAuthInput, flags *protocol.InputFlags) {
		a := &p.win.act
		p.breakTick()
		if a.mine != nil {
			flags.Set(packet.InputFlagPerformItemStackRequest)
			pk.ItemStackRequest = protocol.Option(*a.mine)
			a.mine = nil
		}
		if len(a.blockActions) > 0 {
			flags.Set(packet.InputFlagPerformBlockActions)
			pk.BlockActions = protocol.Option(a.blockActions)
			a.blockActions = nil
		}
	})
}

// awaitAck makes the client wait for the server's update of the positions before the action's
// sequence is acknowledged.
func (p *Player) awaitAck(seq int32, pos ...protocol.BlockPos) {
	a := &p.win.act
	a.ackSeq = max(a.ackSeq, seq)
	if !a.ackPending {
		a.ackPending = true
		a.ackAt = time.Now().Add(ackTimeout)
	}
	if len(pos) == 0 {
		a.ackAt = time.Time{}
		return
	}
	if a.ackPos == nil {
		a.ackPos = map[protocol.BlockPos]bool{}
	}
	for _, b := range pos {
		a.ackPos[b] = true
	}
}

// playerAction handles a Java player_action.
func (p *Player) playerAction(action int32, pos protocol.BlockPos, face int32) {
	a := &p.win.act
	w := p.win
	add := func(act int32) {
		a.blockActions = append(a.blockActions, protocol.PlayerBlockAction{Action: act, BlockPos: pos, Face: face})
	}
	switch action {
	case digStart:
		if a.breaking && a.javaDone && a.breakPos != pos && p.self.mode != GameCreative {
			// The Java client finished the last block before the Bedrock progress did and starts
			// the next: a Bedrock client breaks one block at a time, so this one starts after.
			a.next = &breakJob{pos: pos, face: face}
			return
		}
		a.next = nil
		p.startBreak(pos, face)
	case digAbort:
		if a.next != nil && a.next.pos == pos {
			a.next = nil
			return
		}
		add(protocol.PlayerActionAbortBreak)
		a.breaking = false
	case digFinish:
		if a.next != nil && a.next.pos == pos {
			a.next.javaDone = true
			return
		}
		if !a.breaking || a.breakPos != pos {
			return // destroyed already (the Bedrock progress was full first) or given up
		}
		if _, known := p.breakRate(pos); !known {
			p.destroyBlock() // a block without break data: the Java client's timing
			return
		}
		a.javaDone = true // the Bedrock progress decides (breakTick)
	case dropStack, dropOne:
		k := slotKey{protocol.ContainerHotBar, byte(w.held)}
		inst := w.auth[k]
		if inst.Stack.Count == 0 {
			return
		}
		n := int(inst.Stack.Count)
		if action == dropOne {
			n = 1
		}
		p.log.Debug("java drop key", "action", action, "slot", w.held, "n", n)
		w.drop(k, n)
	case releaseUse:
		p.Write(&packet.InventoryTransaction{TransactionData: &protocol.ReleaseItemTransactionData{
			ActionType:   protocol.ReleaseItemActionRelease,
			HotBarSlot:   int32(w.held),
			HeldItem:     p.HeldItem(),
			HeadPosition: vec32(p.eyePos()),
		}})
	case swapHands:
		hk, ok := slotKey{protocol.ContainerHotBar, byte(w.held)}, slotKey{protocol.ContainerOffhand, 0}
		h, o := w.auth[hk], w.auth[ok]
		if h.Stack.Count == 0 && o.Stack.Count == 0 {
			return
		}
		// Bedrock moves items to the off hand in the inventory screen only: open it around the swap.
		wasOpen := w.invOpen || w.cur != nil
		w.openInventory()
		q := w.newRequest()
		switch {
		case h.Stack.Count == 0:
			q.move(int(o.Stack.Count), q.info(ok, o), q.info(hk, h))
		case o.Stack.Count == 0:
			q.move(int(h.Stack.Count), q.info(hk, h), q.info(ok, o))
		default:
			q.actions = append(q.actions, &protocol.SwapStackRequestAction{Source: q.info(hk, h), Destination: q.info(ok, o)})
		}
		h.StackNetworkID, o.StackNetworkID = q.id, q.id
		if h.Stack.Count == 0 {
			h = protocol.ItemInstance{}
		}
		if o.Stack.Count == 0 {
			o = protocol.ItemInstance{}
		}
		w.send(q, map[slotKey]protocol.ItemInstance{hk: o, ok: h})
		if !wasOpen {
			w.invOpen = false
			p.Write(&packet.ContainerClose{WindowID: protocol.WindowIDInventory, ContainerType: 0xff})
		}
		w.sendSlot(hk)
		w.sendSlot(ok)
	}
}

// eyePos is the player's eye position.
func (p *Player) eyePos() mgl64.Vec3 { return p.mv.pos.Add(mgl64.Vec3{0, eyeHeight, 0}) }

// interactiveBlocks are blocks a click uses instead of placing against (no block placed).
func interactiveBlock(n string) bool {
	n = strings.TrimPrefix(n, "minecraft:")
	switch n {
	case "chest", "trapped_chest", "ender_chest", "barrel", "crafting_table", "furnace", "lit_furnace", "blast_furnace",
		"lit_blast_furnace", "smoker", "lit_smoker", "anvil", "chipped_anvil", "damaged_anvil", "enchanting_table",
		"brewing_stand", "hopper", "dispenser", "dropper", "lever", "grindstone", "stonecutter_block", "loom",
		"smithing_table", "cartography_table", "beacon", "bell", "cake", "lectern", "jukebox", "noteblock", "note_block",
		"repeater", "unpowered_repeater", "powered_repeater", "unpowered_comparator", "powered_comparator", "daylight_detector",
		"daylight_detector_inverted", "crafter", "decorated_pot", "chiseled_bookshelf", "respawn_anchor", "composter",
		"cauldron", "flower_pot", "command_block", "repeating_command_block", "chain_command_block", "beehive", "bee_nest",
		"campfire", "soul_campfire", "vault", "trial_spawner":
		return true
	}
	return strings.HasSuffix(n, "_door") || strings.HasSuffix(n, "_trapdoor") || strings.HasSuffix(n, "fence_gate") ||
		strings.HasSuffix(n, "_button") || strings.HasSuffix(n, "shulker_box") || strings.HasSuffix(n, "_bed") ||
		strings.HasSuffix(n, "_sign") || strings.HasSuffix(n, "_shelf")
}

// useItemOn is a right click on a block, sent as a Bedrock client sends it: StartItemUseOn, the
// arm swing, the click transaction (with the held slot's predicted change when a block is
// placed), then StopItemUseOn after the next input.
func (p *Player) useItemOn(pos protocol.BlockPos, face int32, click mgl32.Vec3) {
	w := p.win
	slot := w.held
	hk := slotKey{protocol.ContainerHotBar, byte(slot)}
	held := w.auth[hk]
	cpos := cube.Pos{int(pos[0]), int(pos[1]), int(pos[2])}
	dst := cpos.Side(cube.Face(face))
	dp := protocol.BlockPos{int32(dst[0]), int32(dst[1]), int32(dst[2])}
	var rid uint32
	name := ""
	if p.world != nil {
		if b, ok := p.world.Block(cpos); ok {
			rid = p.rem.pal.IndexToServer(b)
			name = p.rem.pal.Name(b)
		}
	}
	er := p.rem.gd.EntityRuntimeID
	p.Write(&packet.PlayerAction{EntityRuntimeID: er, ActionType: protocol.PlayerActionStartItemUseOn, BlockPosition: pos, ResultPosition: dp, BlockFace: face})
	p.Write(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: er})
	tr := &packet.InventoryTransaction{}
	data := &protocol.UseItemTransactionData{
		ActionType:       protocol.UseItemActionClickBlock,
		TriggerType:      protocol.TriggerTypePlayerInput,
		BlockPosition:    pos,
		BlockFace:        face,
		HotBarSlot:       int32(slot),
		HeldItem:         held,
		Position:         vec32(p.eyePos()),
		ClickedPosition:  click,
		BlockRuntimeID:   rid,
		ClientPrediction: protocol.ClientPredictionSuccess,
	}
	sneaking := p.mv.keys&keyShift != 0
	if held.Stack.Count > 0 {
		// Ask for the held slot back: the server's answer corrects whatever the client predicted.
		tr.LegacyRequestID = w.nextLegacyRequest()
		tr.LegacySetItemSlots = []protocol.LegacySetItemSlot{{ContainerID: protocol.ContainerHotBar, Slots: []byte{byte(slot)}}}
	}
	if held.Stack.BlockRuntimeID != 0 && held.Stack.Count > 0 && (sneaking || !interactiveBlock(name)) && p.self.mode != GameCreative {
		after := held
		after.Stack.Count--
		if after.Stack.Count == 0 {
			after = protocol.ItemInstance{}
		}
		data.Actions = []protocol.InventoryAction{{
			SourceType:    protocol.InventoryActionSourceContainer,
			WindowID:      protocol.Option[int8](0),
			InventorySlot: uint32(slot),
			OldItem:       held,
			NewItem:       after,
		}}
		w.auth[hk] = after
	}
	tr.TransactionData = data
	p.Write(tr)
	p.AfterInput(&packet.PlayerAction{EntityRuntimeID: er, ActionType: protocol.PlayerActionStopItemUseOn, BlockPosition: pos, ResultPosition: dp, BlockFace: face})
}

// useItem is a right click in the air (eating, drinking, bows, throwing, shields...).
func (p *Player) useItem() {
	w := p.win
	held := p.HeldItem()
	if held.Stack.Count > 0 && p.cannotEat(p.itemName(held.Stack.NetworkID)) {
		// A Bedrock client does not start eating with a full hunger bar (the Java client does
		// not either, but sends use_item anyway): a server takes the use as the start of eating
		// and finishes it, so the food would be eaten. A Java client that started eating anyway
		// (its hunger bar not updated yet) would wait for the end forever: stop it. With the use
		// key held it tries again, and does not start once its bar is full too.
		p.sendUsingFlags(false)
		return
	}
	tr := &packet.InventoryTransaction{}
	if held.Stack.Count > 0 {
		tr.LegacyRequestID = w.nextLegacyRequest()
		tr.LegacySetItemSlots = []protocol.LegacySetItemSlot{{ContainerID: protocol.ContainerHotBar, Slots: []byte{byte(w.held)}}}
	}
	tr.TransactionData = &protocol.UseItemTransactionData{
		ActionType:       protocol.UseItemActionClickAir,
		TriggerType:      protocol.TriggerTypePlayerInput,
		BlockFace:        -1,
		HotBarSlot:       int32(w.held),
		HeldItem:         held,
		Position:         vec32(p.eyePos()),
		ClientPrediction: protocol.ClientPredictionSuccess,
	}
	p.Write(tr)
}

// nextLegacyRequest is the next legacy request id: even, from -4 down, as the client numbers them.
func (w *Windows) nextLegacyRequest() int32 {
	a := &w.act
	if a.legacyID >= -2 || a.legacyID < -30000 {
		a.legacyID = -2
	}
	a.legacyID -= 2
	return a.legacyID
}

// editSign sends a sign's new text.
func (p *Player) editSign(pos cube.Pos, front bool, txt string) {
	var data map[string]any
	if p.world != nil {
		if col := p.world.cols[chunkOf(pos)]; col != nil {
			if d, ok := col.be[pos]; ok {
				data = copyNBT(d)
			}
		}
	}
	if data == nil {
		data = map[string]any{"id": "Sign", "x": int32(pos[0]), "y": int32(pos[1]), "z": int32(pos[2])}
	}
	side := "BackText"
	if front {
		side = "FrontText"
	}
	st, _ := data[side].(map[string]any)
	st = copyNBT(st)
	st["Text"] = txt
	data[side] = st
	p.Write(&packet.BlockActorData{Position: protocol.BlockPos{int32(pos[0]), int32(pos[1]), int32(pos[2])}, NBTData: data})
}

// blockEvent shows chest and shulker box lids opening and closing (block_event 1: viewers).
func (p *Player) blockEvent(pk *packet.BlockEvent) {
	if pk.EventType != 1 || p.world == nil {
		return
	}
	b, ok := p.world.Block(cube.Pos{int(pk.Position[0]), int(pk.Position[1]), int(pk.Position[2])})
	if !ok {
		return
	}
	name := p.rem.pal.Name(b)
	switch {
	case name == "minecraft:chest", name == "minecraft:trapped_chest", name == "minecraft:ender_chest":
	case name == "minecraft:undyed_shulker_box":
		name = "minecraft:shulker_box"
	case strings.HasSuffix(name, "shulker_box"):
	default:
		return
	}
	id := v777.BuiltinID("minecraft:block", name)
	if id < 0 {
		return
	}
	wr := p.pk()
	wr.Position(int(pk.Position[0]), int(pk.Position[1]), int(pk.Position[2]))
	wr.Byte(1)
	wr.Byte(byte(min(max(pk.EventData, 0), 255)))
	wr.VarInt(id)
	p.send(v777.ClientboundPlayBlockEvent, wr)
}

// copyNBT copies a compound one level deep (nil: a new one).
func copyNBT(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// crackEvent shows other players' block cracking (the player's own the Java client draws itself).
func (p *Player) crackEvent(pk *packet.LevelEvent) {
	switch pk.EventType {
	case packet.LevelEventStartBlockCracking, packet.LevelEventUpdateBlockCracking, packet.LevelEventStopBlockCracking:
	default:
		return
	}
	pos := protocol.BlockPos{int32(mfloor(float64(pk.Position[0]))), int32(mfloor(float64(pk.Position[1]))), int32(mfloor(float64(pk.Position[2])))}
	a := &p.win.act
	if a.breaking && a.breakPos == pos {
		return // our own
	}
	if a.cracks == nil {
		a.cracks = map[protocol.BlockPos]*crack{}
	}
	c := a.cracks[pos]
	if pk.EventType == packet.LevelEventStopBlockCracking {
		if c != nil {
			p.sendCrack(c.id, pos, -1)
			delete(a.cracks, pos)
		}
		return
	}
	if c == nil {
		c = &crack{id: -(0x10000 + int32(len(a.cracks))), stage: -1}
		for _, o := range a.cracks {
			if o.id == c.id {
				c.id--
			}
		}
		a.cracks[pos] = c
	}
	c.rate = float64(pk.EventData) / 65535
}

func (p *Player) crackTick() {
	a := &p.win.act
	for pos, c := range a.cracks {
		c.progress += c.rate
		st := min(9, int(c.progress*10))
		if c.progress > 1.5 {
			p.sendCrack(c.id, pos, -1)
			delete(a.cracks, pos)
			continue
		}
		if st != c.stage {
			c.stage = st
			p.sendCrack(c.id, pos, st)
		}
	}
}

// sendCrack sends block_destruction (stage -1: removed).
func (p *Player) sendCrack(id int32, pos protocol.BlockPos, stage int) {
	wr := p.pk()
	wr.VarInt(id)
	wr.Position(int(pos[0]), int(pos[1]), int(pos[2]))
	if stage < 0 {
		stage = 10
	}
	wr.Byte(byte(stage))
	p.send(v777.ClientboundPlayBlockDestruction, wr)
}
