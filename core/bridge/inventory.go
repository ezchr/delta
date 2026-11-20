package bridge

import (
	"fmt"
	"sort"
	"strings"

	jitem "github.com/ezchr/go-mcjava/item"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The player's inventories (milestone 3). Bedrock servers are server authoritative: the bridge
// keeps the server's slots as it sent them (with their stack network ids) in w.auth and shows
// them on the Java client. A Java click is applied by the Java client itself, which tells the
// bridge what each changed slot and the cursor hold now (container_click's hashed stacks): the
// bridge compares that with the server's slots and asks the server for the same change as one
// item stack request (swaps, moves (take/place), drops, the crafts of the crafting grid, creative
// items), predicting the result. A refused request puts the server's slots back on the client.
//
// Slots are Bedrock (container, slot) keys: hotbar 0-8, inventory 9-35, armour 0-3, off hand 0,
// and the UI inventory (window 124) whose slots are the cursor (0), the crafting grids (28-31,
// 32-40), the station inputs and the created output (50).

// slotKey is one slot of a Bedrock container.
type slotKey struct {
	c byte // protocol.ContainerXxx
	s byte
}

// cVirtual marks a Java slot without a Bedrock slot: a crafting or station result.
const cVirtual = 0xfe

var (
	cursorKey  = slotKey{protocol.ContainerCursor, 0}
	createdKey = slotKey{protocol.ContainerCreatedOutput, 50}
	resultKey  = slotKey{cVirtual, 0}
)

// uiContainers are the containers of the UI inventory's slots: slot -> container (0xff: none).
var uiContainers = func() [54]byte {
	var c [54]byte
	for i := range c {
		c[i] = 0xff
	}
	c[0] = protocol.ContainerCursor
	c[1] = protocol.ContainerAnvilInput
	c[2] = protocol.ContainerAnvilMaterial
	c[3] = protocol.ContainerStonecutterInput
	c[4] = protocol.ContainerTradeIngredientOne
	c[5] = protocol.ContainerTradeIngredientTwo
	c[6] = protocol.ContainerTradeTwoIngredientOne
	c[7] = protocol.ContainerTradeTwoIngredientTwo
	c[9] = protocol.ContainerLoomInput
	c[10] = protocol.ContainerLoomDye
	c[11] = protocol.ContainerLoomMaterial
	c[12] = protocol.ContainerCartographyInput
	c[13] = protocol.ContainerCartographyAdditional
	c[14] = protocol.ContainerEnchantingInput
	c[15] = protocol.ContainerEnchantingMaterial
	c[16] = protocol.ContainerGrindstoneInput
	c[17] = protocol.ContainerGrindstoneAdditional
	c[27] = protocol.ContainerBeaconPayment
	for i := 28; i <= 40; i++ {
		c[i] = protocol.ContainerCraftingInput
	}
	c[50] = protocol.ContainerCreatedOutput
	c[51] = protocol.ContainerSmithingTableInput
	c[52] = protocol.ContainerSmithingTableMaterial
	c[53] = protocol.ContainerSmithingTableTemplate
	return c
}()

func uiKey(slot int) slotKey { return slotKey{uiContainers[slot], byte(slot)} }

// isUI reports whether k is a UI inventory slot (cursor, grids, station inputs).
func isUI(k slotKey) bool {
	return int(k.s) < len(uiContainers) && uiContainers[k.s] == k.c && k.c != 0xff
}

// stationInput reports whether k is an input slot of a crafting grid or a station.
func stationInput(k slotKey) bool {
	return isUI(k) && k.c != protocol.ContainerCursor && k.c != protocol.ContainerCreatedOutput
}

// playerSlot is the Bedrock slot of a Java player inventory index (set_player_inventory: 0-8
// hotbar, 9-35 inventory, 36-39 boots..helmet, 40 off hand).
func playerSlot(i int) (slotKey, bool) {
	switch {
	case i < 0:
	case i < 9:
		return slotKey{protocol.ContainerHotBar, byte(i)}, true
	case i < 36:
		return slotKey{protocol.ContainerInventory, byte(i)}, true
	case i < 40:
		return slotKey{protocol.ContainerArmor, byte(39 - i)}, true
	case i == 40:
		return slotKey{protocol.ContainerOffhand, 0}, true
	}
	return slotKey{}, false
}

// playerIndex is the inverse of playerSlot (-1: not a player inventory slot).
func playerIndex(k slotKey) int {
	switch k.c {
	case protocol.ContainerHotBar:
		return int(k.s)
	case protocol.ContainerInventory:
		return int(k.s)
	case protocol.ContainerArmor:
		return 39 - int(k.s)
	case protocol.ContainerOffhand:
		return 40
	}
	return -1
}

// inventoryLayout is the Java player inventory window (id 0): result, 2x2 grid, armour,
// inventory, hotbar, off hand.
var inventoryLayout = func() []slotKey {
	l := []slotKey{resultKey, uiKey(28), uiKey(29), uiKey(30), uiKey(31)}
	for i := 0; i < 4; i++ {
		l = append(l, slotKey{protocol.ContainerArmor, byte(i)})
	}
	for i := 9; i < 36; i++ {
		l = append(l, slotKey{protocol.ContainerInventory, byte(i)})
	}
	for i := 0; i < 9; i++ {
		l = append(l, slotKey{protocol.ContainerHotBar, byte(i)})
	}
	return append(l, slotKey{protocol.ContainerOffhand, 0})
}()

// layout is the Bedrock slot of every slot of the Java window open now.
func (w *Windows) layout() []slotKey {
	if w.open == 0 || w.cur == nil || !w.cur.shown {
		return inventoryLayout
	}
	if w.cur.layout == nil {
		l := append([]slotKey(nil), w.cur.slots...)
		for i := 9; i < 36; i++ {
			l = append(l, slotKey{protocol.ContainerInventory, byte(i)})
		}
		for i := 0; i < 9; i++ {
			l = append(l, slotKey{protocol.ContainerHotBar, byte(i)})
		}
		w.cur.layout = l
	}
	return w.cur.layout
}

// javaSlot is the Java slot of k in the open window (-1: not shown there).
func (w *Windows) javaSlot(k slotKey) int {
	for i, l := range w.layout() {
		if l == k {
			return i
		}
	}
	return -1
}

// windowKey is the Bedrock slot of an InventoryContent/InventorySlot index of a window id.
func (w *Windows) windowKey(window uint32, i int, name protocol.FullContainerName) (slotKey, bool) {
	switch window {
	case protocol.WindowIDInventory:
		if i < 9 {
			return slotKey{protocol.ContainerHotBar, byte(i)}, true
		}
		if i < 36 {
			return slotKey{protocol.ContainerInventory, byte(i)}, true
		}
	case protocol.WindowIDOffHand:
		if i == 0 {
			return slotKey{protocol.ContainerOffhand, 0}, true
		}
	case protocol.WindowIDArmour:
		if i < 4 {
			return slotKey{protocol.ContainerArmor, byte(i)}, true
		}
	case protocol.WindowIDUI:
		if i >= 0 && i < len(uiContainers) && uiContainers[i] != 0xff {
			return uiKey(i), true
		}
	}
	if c := w.cur; c != nil && window == uint32(c.bid) {
		if k, ok := c.contentKey(i, name); ok {
			return k, true
		}
	}
	return slotKey{}, false
}

// pendingReq is an item stack request waiting for its answer.
type pendingReq struct {
	id   int32
	snap map[slotKey]protocol.ItemInstance // the slots before it
	pred map[slotKey]protocol.ItemInstance // what it should leave
}

func init() {
	OnBedrock(packet.IDInventoryContent, func(p *Player, pk *packet.InventoryContent) {
		w := p.win
		if pk.WindowID != protocol.WindowIDUI {
			p.log.Debug("inventory content", "window", pk.WindowID, "n", len(pk.Content), "container", pk.Container.ContainerID)
		}
		if c := w.cur; c != nil && pk.WindowID == uint32(c.bid) {
			if c.station() {
				return // a station: its inputs are UI slots
			}
			c.setContent(len(pk.Content), pk.Container)
		}
		for i, it := range pk.Content {
			if k, ok := w.windowKey(pk.WindowID, i, pk.Container); ok {
				w.auth[k] = it
			}
		}
		if c := w.cur; c != nil && pk.WindowID == uint32(c.bid) && !c.shown {
			w.showWindow()
			return
		}
		if pk.WindowID == protocol.WindowIDUI {
			w.updateResult()
		}
		w.dirty = true
	})
	OnBedrock(packet.IDInventorySlot, func(p *Player, pk *packet.InventorySlot) {
		w := p.win
		name, _ := pk.Container.Value()
		k, ok := w.windowKey(pk.WindowID, int(pk.Slot), name)
		p.log.Debug("inventory slot", "window", pk.WindowID, "slot", pk.Slot, "container", name.ContainerID, "item", p.describeStack(pk.NewItem.Stack), "id", pk.NewItem.StackNetworkID, "known", ok)
		if !ok {
			return
		}
		w.auth[k] = pk.NewItem
		w.sendSlot(k)
		if stationInput(k) {
			w.updateResult()
		}
	})
	// BDS tells the client about an item it picked up with an InventoryTransaction whose container
	// actions carry the slot's new item (just before TakeItemActor). Without this the Java client
	// only saw the pickup at BDS's next full InventoryContent, seconds later.
	OnBedrock(packet.IDInventoryTransaction, func(p *Player, pk *packet.InventoryTransaction) {
		w := p.win
		for _, a := range pk.Actions {
			win, ok := a.WindowID.Value()
			if a.SourceType != protocol.InventoryActionSourceContainer || !ok {
				continue
			}
			k, ok := w.windowKey(uint32(int32(win)), int(a.InventorySlot), protocol.FullContainerName{})
			if !ok {
				continue
			}
			w.auth[k] = a.NewItem
			w.sendSlot(k)
		}
	})
	OnBedrock(packet.IDPlayerHotBar, func(p *Player, pk *packet.PlayerHotBar) {
		if pk.WindowID != protocol.WindowIDInventory || !pk.SelectHotBarSlot || pk.SelectedHotBarSlot > 8 {
			return
		}
		p.win.held = int(pk.SelectedHotBarSlot)
		p.sendHeldSlot(p.win.held)
	})
	OnBedrockStop(packet.IDMobEquipment, func(p *Player, pk *packet.MobEquipment) bool {
		if pk.EntityRuntimeID != p.rem.gd.EntityRuntimeID {
			return false // another entity (entities)
		}
		if pk.WindowID == protocol.WindowIDInventory && pk.HotBarSlot < 9 && int(pk.HotBarSlot) != p.win.held {
			p.win.held = int(pk.HotBarSlot)
			p.sendHeldSlot(p.win.held)
		}
		return true
	})
	OnBedrock(packet.IDItemStackResponse, func(p *Player, pk *packet.ItemStackResponse) {
		p.win.responses(pk.Responses)
	})

	OnJava(v777.ServerboundPlaySetCarriedItem, func(p *Player, r *wireReader) error {
		slot := int(r.Int16())
		if r.Err != nil || slot < 0 || slot > 8 || p.rem == nil {
			return r.Err
		}
		w := p.win
		w.held = slot
		// A vanilla client sends the held item without its stack network id (hasNetId false).
		// Geyser's MobEquipment reader (CodecProcessor.fakeItemDescriptorRead) still expects the
		// pre-26.40 net id variant VarInt after hasNetId, so it read the zig-zag net id as that
		// variant and kicked with "Invalid packet received!" for any id but 0 and +-1.
		item := w.auth[slotKey{protocol.ContainerHotBar, byte(slot)}]
		item.StackNetworkID = 0
		p.Write(&packet.MobEquipment{
			EntityRuntimeID: p.rem.gd.EntityRuntimeID,
			NewItem:         item,
			InventorySlot:   byte(slot),
			HotBarSlot:      byte(slot),
			WindowID:        protocol.WindowIDInventory,
		})
		return nil
	})
	OnJava(v777.ServerboundPlayContainerClick, func(p *Player, r *wireReader) error {
		window := r.VarInt()
		r.VarInt() // state id
		slot := int(r.Int16())
		button := int(r.Int8())
		mode := int(r.VarInt())
		n := int(r.VarInt())
		if n < 0 || n > 128 {
			return fmt.Errorf("container_click: %d changed slots", n)
		}
		proto := p.win.proto()
		changed := make(map[int]jitem.HashedStack, n)
		for range n {
			s := int(r.Int16())
			var h jitem.HashedStack
			h.DecodeFor(r, proto)
			changed[s] = h
		}
		var carried jitem.HashedStack
		carried.DecodeFor(r, proto)
		if r.Err != nil {
			return r.Err
		}
		if p.rem != nil && !p.inWaiting {
			p.win.click(window, slot, button, mode, changed, carried)
		}
		return nil
	})
	OnJava(v777.ServerboundPlaySetCreativeModeSlot, func(p *Player, r *wireReader) error {
		slot := int(r.Int16())
		var js jitem.Stack
		js.DecodeUntrustedFor(r, p.win.proto())
		if r.Err != nil {
			return r.Err
		}
		if p.rem != nil && !p.inWaiting {
			p.win.creativeSlot(slot, &js)
		}
		return nil
	})
	OnTick(func(p *Player) {
		w := p.win
		if w.cur != nil && !w.cur.shown {
			w.cur.wait++
			if w.cur.wait > 10 {
				w.showWindow()
			}
		}
		if w.resendWait > 0 {
			w.resendWait--
		}
		if w.resend && w.resendWait == 0 && p.rem != nil && !p.inWaiting {
			w.resend, w.resendWait = false, resendTicks
			p.Write(&packet.InventoryTransaction{TransactionData: &protocol.MismatchTransactionData{}})
		}
		if w.dirty {
			w.dirty = false
			w.resync()
		}
		if w.recipesDirty && p.rem != nil && !p.inWaiting {
			w.recipesDirty = false
			w.sendRecipes()
		}
	})
}

// sendHeldSlot selects a hotbar slot on the Java client.
func (p *Player) sendHeldSlot(slot int) {
	wr := p.pk()
	wr.VarInt(int32(slot))
	p.send(v777.ClientboundPlaySetHeldSlot, wr)
}

// HeldSlot is the selected hotbar slot. p.mu must be held.
func (p *Player) HeldSlot() int { return p.win.held }

// HeldItem is the server's item in the selected hotbar slot (for item use transactions of other
// areas: entity interactions). p.mu must be held.
func (p *Player) HeldItem() protocol.ItemInstance {
	return p.win.auth[slotKey{protocol.ContainerHotBar, byte(p.win.held)}]
}

// nextState is the next Java container state id.
func (w *Windows) nextState() int32 {
	w.stateID = (w.stateID + 1) & 0x7fff
	return w.stateID
}

// javaWindow is the Java id of the window open now.
func (w *Windows) javaWindow() int32 {
	if w.cur != nil && w.cur.shown && w.open == w.cur.jid {
		return w.cur.jid
	}
	return 0
}

// slotStack is what a Java slot of the open window shows.
func (w *Windows) slotStack(k slotKey) protocol.ItemStack {
	if k == resultKey {
		if w.result != nil {
			st := w.result.out
			return st
		}
		return protocol.ItemStack{}
	}
	return w.auth[k].Stack
}

// sendSlot shows one server slot on the Java client: in the open window if it is there, else in
// the player's inventory or as the cursor.
func (w *Windows) sendSlot(k slotKey) {
	p := w.p
	if k == cursorKey {
		wr := p.pk()
		p.writeStack(wr, w.auth[k].Stack)
		p.send(v777.ClientboundPlaySetCursorItem, wr)
		return
	}
	if i := w.javaSlot(k); i >= 0 {
		wr := p.pk()
		wr.VarInt(w.javaWindow())
		wr.VarInt(w.nextState())
		wr.Int16(int16(i))
		p.writeStack(wr, w.slotStack(k))
		p.send(v777.ClientboundPlayContainerSetSlot, wr)
		return
	}
	if i := playerIndex(k); i >= 0 {
		wr := p.pk()
		wr.VarInt(int32(i))
		p.writeStack(wr, w.auth[k].Stack)
		p.send(v777.ClientboundPlaySetPlayerInventory, wr)
	}
}

// resync shows every server slot of the open window, the cursor and the player slots the window
// does not show (after a refused or not understood click, and after whole-content updates).
func (w *Windows) resync() {
	p := w.p
	if p.rem == nil || p.inWaiting {
		return
	}
	l := w.layout()
	wr := p.pk()
	wr.VarInt(w.javaWindow())
	wr.VarInt(w.nextState())
	wr.VarInt(int32(len(l)))
	for _, k := range l {
		p.writeStack(wr, w.slotStack(k))
	}
	p.writeStack(wr, w.auth[cursorKey].Stack)
	p.send(v777.ClientboundPlayContainerSetContent, wr)
	if w.javaWindow() != 0 {
		for _, k := range []slotKey{{protocol.ContainerArmor, 0}, {protocol.ContainerArmor, 1}, {protocol.ContainerArmor, 2},
			{protocol.ContainerArmor, 3}, {protocol.ContainerOffhand, 0}} {
			w.sendSlot(k)
		}
	}
}

// openInventory tells the server the inventory is open before the first click in it (a Bedrock
// client opens it before anything can be clicked).
func (w *Windows) openInventory() {
	if w.cur != nil || w.invOpen && !w.invReopen {
		return
	}
	w.invOpen, w.invReopen = true, false
	p := w.p
	p.Write(&packet.Interact{ActionType: packet.InteractActionOpenInventory, TargetEntityRuntimeID: p.rem.gd.EntityRuntimeID})
}

// resendTicks is the least time between two asks for the inventory (refusals come in bursts: a
// refused request fails the requests made on its prediction too).
const resendTicks = 10

// refused handles a request the server refused: the bridge's slots (stack network ids) or its
// idea of the server's open screen are not what the server has. BDS refuses a slot whose stack id
// is not the one it has (FailedToValidateSrcSlot/DstSlot, 49/50) and every main inventory slot on
// its HUD screen (no inventory screen open), and a refused request changes nothing, so the next
// click would fail the same way. So: ask for the inventory again (the mismatch transaction a
// Bedrock client sends: BDS sends every player inventory again with its stack ids, and the
// client gets the server's slots) and open the inventory screen again before the next click in it.
func (w *Windows) refused() {
	w.resend = true
	if w.cur == nil {
		w.invReopen = true
	}
}

// closeStrayInventory closes the server's inventory window when the bridge opened it for clicks
// and the Java client then uses the world: a Java client never does that with its inventory screen
// open, so that screen closed without a container_close (a client mod clicking slots with no
// screen, say). A Bedrock server ignores block and item use while the inventory is open.
func (w *Windows) closeStrayInventory() {
	if !w.invOpen || w.cur != nil {
		return
	}
	w.invOpen = false
	w.returnItems()
	w.p.Write(&packet.ContainerClose{WindowID: protocol.WindowIDInventory, ContainerType: 0xff})
}

// change is one slot a click changed: the server's stack before, the client's after.
type change struct {
	k      slotKey
	inst   protocol.ItemInstance
	bid    int32 // Java item before (0: empty)
	bn     int
	aid    int32 // Java item after
	an     int
	result bool // the after came out of a craft (created output)
}

func (c *change) beforeEmpty() bool { return c.bid == 0 || c.bn == 0 }
func (c *change) afterEmpty() bool  { return c.aid == 0 || c.an == 0 }

// amountOf is an amount of an item moved, gained or lost in a slot.
type amountOf struct {
	c  *change
	id int32
	n  int
}

// stackRequest is the request being built for a click.
type stackRequest struct {
	id          int32
	actions     []protocol.StackRequestAction
	filter      []string
	filterCause int32
	touched     map[slotKey]bool
	// remap renames containers in the request (the new trade screen's inputs).
	remap map[byte]byte
}

// tradeRemap: the new trade screen names its input slots (UI 4 and 5) by its own containers.
var tradeRemap = map[byte]byte{
	protocol.ContainerTradeIngredientOne: protocol.ContainerTradeTwoIngredientOne,
	protocol.ContainerTradeIngredientTwo: protocol.ContainerTradeTwoIngredientTwo,
}

// info is the request slot info of a slot (a slot changed earlier in this request goes by the
// request's id).
func (q *stackRequest) info(k slotKey, inst protocol.ItemInstance) protocol.StackRequestSlotInfo {
	id := inst.StackNetworkID
	if q.touched[k] {
		id = q.id
	}
	if k.c == protocol.ContainerOffhand {
		k.s = 1 // requests name the off hand's slot 1
	}
	if to, ok := q.remap[k.c]; ok {
		k.c = to
	}
	return protocol.StackRequestSlotInfo{Container: protocol.FullContainerName{ContainerID: k.c}, Slot: k.s, StackNetworkID: id}
}

func (q *stackRequest) touch(k slotKey) {
	if q.touched == nil {
		q.touched = map[slotKey]bool{}
	}
	q.touched[k] = true
}

// move adds a move of n items between two slots: a Take when it ends on the cursor, else a Place.
func (q *stackRequest) move(n int, src, dst protocol.StackRequestSlotInfo) {
	if dst.Container.ContainerID == protocol.ContainerCursor {
		t := &protocol.TakeStackRequestAction{}
		t.Count, t.Source, t.Destination = byte(n), src, dst
		q.actions = append(q.actions, t)
		return
	}
	pl := &protocol.PlaceStackRequestAction{}
	pl.Count, pl.Source, pl.Destination = byte(n), src, dst
	q.actions = append(q.actions, pl)
}

func (q *stackRequest) createdOutput() protocol.StackRequestSlotInfo {
	return protocol.StackRequestSlotInfo{Container: protocol.FullContainerName{ContainerID: protocol.ContainerCreatedOutput}, Slot: 50, StackNetworkID: q.id}
}

// newRequest starts an item stack request.
func (w *Windows) newRequest() *stackRequest {
	id := w.nextReq
	w.nextReq -= 2
	if w.nextReq < -30001 {
		w.nextReq = -1
	}
	q := &stackRequest{id: id}
	if c := w.cur; c != nil && c.trade != nil && c.trade.newUI {
		q.remap = tradeRemap
	}
	return q
}

// click applies a Java container click.
func (w *Windows) click(window int32, slot, button, mode int, changed map[int]jitem.HashedStack, carried jitem.HashedStack) {
	p := w.p
	if window != w.javaWindow() {
		p.log.Debug("click in a window not open", "window", window, "open", w.javaWindow())
		return
	}
	if window == 0 {
		w.openInventory()
	}
	l := w.layout()
	if slot >= 0 && slot < len(l) && l[slot].c == cVirtual {
		w.clickResult(mode, button)
		return
	}
	if mode == 4 && slot >= 0 || slot == -999 && mode == 0 {
		w.clickDrop(slot, button, mode)
		return
	}
	var changes []*change
	add := func(k slotKey, h jitem.HashedStack) {
		inst := w.auth[k]
		c := &change{k: k, inst: inst, bid: p.javaID(inst.Stack), bn: int(inst.Stack.Count), aid: h.ID, an: int(h.Count)}
		if c.beforeEmpty() {
			c.bid, c.bn = 0, 0
		}
		if c.afterEmpty() {
			c.aid, c.an = 0, 0
		}
		if c.bid == c.aid && c.bn == c.an {
			return
		}
		changes = append(changes, c)
	}
	keys := make([]int, 0, len(changed))
	for s := range changed {
		keys = append(keys, s)
	}
	sort.Ints(keys)
	for _, s := range keys {
		if s >= 0 && s < len(l) && l[s].c != cVirtual {
			add(l[s], changed[s])
		}
	}
	add(cursorKey, carried)
	if len(changes) == 0 {
		return
	}
	q := w.newRequest()
	if !w.plan(changes, q) || len(q.actions) == 0 {
		p.log.Debug("click not understood; showing the server's inventory again", "slot", slot, "mode", mode, "button", button, "changes", describeChanges(changes))
		w.resync()
		return
	}
	pred := map[slotKey]protocol.ItemInstance{}
	for _, c := range changes {
		pred[c.k] = w.predicted(c, changes, q.id)
	}
	w.send(q, pred)
	for _, c := range changes {
		if stationInput(c.k) {
			w.updateResult()
			break
		}
	}
}

// predicted is the server instance a slot will hold after a click's request.
func (w *Windows) predicted(c *change, changes []*change, reqID int32) protocol.ItemInstance {
	if c.afterEmpty() {
		return protocol.ItemInstance{}
	}
	inst := c.inst
	if c.beforeEmpty() || c.bid != c.aid {
		inst = w.instanceOf(c.aid, changes)
	}
	inst.Stack.Count = uint16(c.an)
	inst.StackNetworkID = reqID
	return inst
}

// instanceOf finds a server stack of a Java item: what a click moved it from, else anything of
// the same item the server has.
func (w *Windows) instanceOf(id int32, changes []*change) protocol.ItemInstance {
	for _, c := range changes {
		if !c.beforeEmpty() && c.bid == id {
			return c.inst
		}
	}
	for _, inst := range w.auth {
		if inst.Stack.Count > 0 && w.p.javaID(inst.Stack) == id {
			return inst
		}
	}
	return protocol.ItemInstance{}
}

// send sends a request and applies its predicted slots.
func (w *Windows) send(q *stackRequest, pred map[slotKey]protocol.ItemInstance) {
	w.track(q, pred)
	w.p.Write(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{RequestID: q.id, Actions: q.actions, FilterStrings: q.filter, FilterCause: q.filterCause}}})
}

// track applies a request's predicted slots and keeps it for the server's answer (send sends it;
// requests that go inside PlayerAuthInput are only tracked).
func (w *Windows) track(q *stackRequest, pred map[slotKey]protocol.ItemInstance) {
	p := w.p
	pr := &pendingReq{id: q.id, snap: map[slotKey]protocol.ItemInstance{}, pred: pred}
	for k, inst := range pred {
		pr.snap[k] = w.auth[k]
		if k == createdKey {
			continue
		}
		w.auth[k] = inst
	}
	w.pending = append(w.pending, pr)
	if len(w.pending) > 64 {
		w.pending = w.pending[1:]
	}
	p.log.Debug("item stack request", "id", q.id, "actions", describeActions(q.actions))
}

// plan turns the slot changes of one click into the request's actions, and reports whether the
// server can be asked for all of it.
func (w *Windows) plan(changes []*change, q *stackRequest) bool {
	// Swaps: two slots that exchanged their whole stacks of different items. Two stacks of one
	// item whose counts look exchanged are a move: a shift-click of a full stack onto a partial
	// one with no room for the rest (64 and 60 -> 60 and 64) moves 4, as the Java client did.
	used := map[*change]bool{}
	for _, a := range changes {
		for _, b := range changes {
			if a == b || used[a] || used[b] || a.beforeEmpty() || b.beforeEmpty() || a.bid == a.aid {
				continue
			}
			if a.bid == b.aid && a.bn == b.an && b.bid == a.aid && b.bn == a.an {
				q.actions = append(q.actions, &protocol.SwapStackRequestAction{Source: q.info(a.k, a.inst), Destination: q.info(b.k, b.inst)})
				q.touch(a.k)
				q.touch(b.k)
				used[a], used[b] = true, true
			}
		}
	}
	var losses, gains []amountOf
	for _, c := range changes {
		if used[c] {
			continue
		}
		switch {
		case c.beforeEmpty():
			gains = append(gains, amountOf{c, c.aid, c.an})
		case c.afterEmpty():
			losses = append(losses, amountOf{c, c.bid, c.bn})
		case c.bid == c.aid:
			if d := c.an - c.bn; d > 0 {
				gains = append(gains, amountOf{c, c.aid, d})
			} else {
				losses = append(losses, amountOf{c, c.bid, -d})
			}
		default:
			losses = append(losses, amountOf{c, c.bid, c.bn})
			gains = append(gains, amountOf{c, c.aid, c.an})
		}
	}
	// Moves: what one slot lost and another of the same item gained. Losses from the cursor
	// first (placing), then the rest.
	for gi := range gains {
		g := &gains[gi]
		for pass := 0; pass < 2; pass++ {
			for li := range losses {
				l := &losses[li]
				if g.n == 0 {
					break
				}
				fromCursor := l.c.k == cursorKey
				if l.n == 0 || l.c == g.c || l.id != g.id || fromCursor != (pass == 0) {
					continue
				}
				n := min(g.n, l.n)
				q.move(n, q.info(l.c.k, l.c.inst), q.info(g.c.k, g.c.inst))
				q.touch(l.c.k)
				q.touch(g.c.k)
				g.n -= n
				l.n -= n
			}
		}
	}
	var created, consumed []amountOf
	for _, g := range gains {
		if g.n > 0 {
			created = append(created, g)
		}
	}
	for _, l := range losses {
		if l.n > 0 {
			consumed = append(consumed, l)
		}
	}
	if len(created) == 0 && len(consumed) == 0 {
		return true
	}
	if w.p.self.mode == GameCreative {
		return w.planCreative(created, consumed, q)
	}
	return false
}

// planCreative takes creative items for what came from nothing and destroys what went.
func (w *Windows) planCreative(created, deleted []amountOf, q *stackRequest) bool {
	for _, c := range created {
		id, ok := w.creativeFor(c.id, nil)
		if !ok {
			w.p.log.Debug("no creative item for this", "java", jitem.Name(c.id))
			return false
		}
		q.actions = append(q.actions, &protocol.CraftCreativeStackRequestAction{CreativeItemNetworkID: id, NumberOfCrafts: 1})
		q.move(c.n, q.createdOutput(), q.info(c.c.k, c.c.inst))
		q.touch(c.c.k)
		if c.c.beforeEmpty() || c.c.bid != c.c.aid {
			c.c.inst = protocol.ItemInstance{Stack: w.creativeStack(id)}
		}
	}
	for _, d := range deleted {
		q.actions = append(q.actions, &protocol.DestroyStackRequestAction{Count: byte(d.n), Source: q.info(d.c.k, d.c.inst)})
		q.touch(d.c.k)
	}
	return true
}

// clickDrop is a throw (Q over a slot, button 1 with ctrl: the whole stack) or a click outside
// the window with the cursor (button 0: all, 1: one).
func (w *Windows) clickDrop(slot, button, mode int) {
	w.p.log.Debug("java click drop", "slot", slot, "button", button, "mode", mode)
	var k slotKey
	if slot == -999 {
		k = cursorKey
	} else {
		l := w.layout()
		if slot >= len(l) {
			return
		}
		k = l[slot]
	}
	inst := w.auth[k]
	if inst.Stack.Count == 0 || inst.Stack.NetworkID == 0 {
		return
	}
	n := int(inst.Stack.Count)
	if (slot == -999 && button == 1) || (mode == 4 && button == 0) {
		n = 1
	}
	w.drop(k, n)
}

// drop drops n items of a slot.
func (w *Windows) drop(k slotKey, n int) {
	inst := w.auth[k]
	q := w.newRequest()
	q.actions = append(q.actions, &protocol.DropStackRequestAction{Count: byte(n), Source: q.info(k, inst)})
	after := inst
	after.Stack.Count -= uint16(n)
	after.StackNetworkID = q.id
	if after.Stack.Count == 0 {
		after = protocol.ItemInstance{}
	}
	w.send(q, map[slotKey]protocol.ItemInstance{k: after})
}

// responses applies the server's answers to item stack requests. A slot a later request (still
// waiting) changed again keeps that one's prediction: what this answer says goes into the later
// request's "before" instead, which is what a refusal of that one puts back.
func (w *Windows) responses(rs []protocol.ItemStackResponse) {
	p := w.p
	failed := false
	for _, rsp := range rs {
		idx := -1
		for i, pr := range w.pending {
			if pr.id == rsp.RequestID {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		pr := w.pending[idx]
		w.pending = append(w.pending[:idx:idx], w.pending[idx+1:]...)
		later := func(k slotKey) *pendingReq {
			for _, l := range w.pending[idx:] {
				if _, ok := l.snap[k]; ok {
					return l
				}
			}
			return nil
		}
		if rsp.Status != protocol.ItemStackResponseStatusOK {
			p.log.Info("server refused an inventory action", "request", rsp.RequestID, "status", rsp.Status)
			w.refused()
			for k, before := range pr.snap {
				if k == createdKey {
					continue
				}
				if l := later(k); l != nil {
					l.snap[k] = before
				} else {
					w.auth[k] = before
				}
			}
			failed = true
			continue
		}
		for _, ci := range rsp.ContainerInfo {
			for _, si := range ci.SlotInfo {
				k := slotKey{ci.Container.ContainerID, si.Slot}
				if k.c == protocol.ContainerCombinedHotBarAndInventory {
					k.c = protocol.ContainerInventory
					if si.Slot < 9 {
						k.c = protocol.ContainerHotBar
					}
				}
				if k.c == protocol.ContainerOffhand {
					k.s = 0
				}
				if (k.c == protocol.ContainerTradeTwoIngredientOne || k.c == protocol.ContainerTradeTwoIngredientTwo) && (k.s == 4 || k.s == 5) {
					k = uiKey(int(k.s)) // the new trade screen's inputs
				}
				if k == createdKey {
					continue
				}
				dst := &w.auth
				l := later(k)
				cur := w.auth[k]
				if l != nil {
					cur = l.snap[k]
				}
				if si.Count == 0 {
					cur = protocol.ItemInstance{}
				} else {
					if cur.Stack.Count == 0 || cur.Stack.NetworkID == 0 {
						if c, ok := pr.pred[k]; ok && c.Stack.Count > 0 {
							cur = c
						} else {
							cur = pr.snap[k]
						}
					}
					if l == nil && int(cur.Stack.Count) != int(si.Count) {
						failed = true // not what we predicted: show the server's count
					}
					cur.Stack.Count = uint16(si.Count)
					cur.StackNetworkID = si.StackNetworkID
				}
				if l != nil {
					l.snap[k] = cur
				} else {
					(*dst)[k] = cur
				}
			}
		}
	}
	if failed {
		w.updateResult()
		w.resync()
	}
}

// returnItems puts what the cursor, the crafting grids and the station inputs hold back into
// the inventory, as a Bedrock client does before it closes a window (the server keeps them there
// otherwise). Creative clients' cursors are destroyed: the Java client deleted what it held.
func (w *Windows) returnItems() {
	p := w.p
	var from []slotKey
	if p.self.mode == GameCreative {
		w.dropCreativeCursor()
	} else {
		from = append(from, cursorKey)
	}
	for i := 1; i < len(uiContainers); i++ {
		if k := uiKey(i); stationInput(k) {
			from = append(from, k)
		}
	}
	var order []slotKey
	for i := 0; i < 9; i++ {
		order = append(order, slotKey{protocol.ContainerHotBar, byte(i)})
	}
	for i := 9; i < 36; i++ {
		order = append(order, slotKey{protocol.ContainerInventory, byte(i)})
	}
	q := w.newRequest()
	pred := map[slotKey]protocol.ItemInstance{}
	get := func(k slotKey) protocol.ItemInstance {
		if inst, ok := pred[k]; ok {
			return inst
		}
		return w.auth[k]
	}
	for _, src := range from {
		inst := w.auth[src]
		if inst.Stack.Count == 0 || inst.Stack.NetworkID == 0 {
			continue
		}
		id := p.javaID(inst.Stack)
		most := int(jitem.DefaultMaxStackSize(id))
		left := int(inst.Stack.Count)
		for _, pass := range []bool{false, true} { // stacks of the item first, then empty slots
			for _, k := range order {
				if left == 0 {
					break
				}
				d := get(k)
				empty := d.Stack.Count == 0 || d.Stack.NetworkID == 0
				if empty != pass || (!empty && (p.javaID(d.Stack) != id || !stackable(d.Stack, inst.Stack))) {
					continue
				}
				n := min(left, most-int(d.Stack.Count))
				if empty {
					n = min(left, most)
				}
				if n <= 0 {
					continue
				}
				q.move(n, q.info(src, inst), q.info(k, d))
				q.touch(src)
				q.touch(k)
				if empty {
					d = inst
					d.Stack.Count = 0
				}
				d.Stack.Count += uint16(n)
				d.StackNetworkID = q.id
				pred[k] = d
				left -= n
			}
		}
		rest := inst
		rest.Stack.Count, rest.StackNetworkID = uint16(left), q.id
		if left == 0 {
			rest = protocol.ItemInstance{}
		}
		pred[src] = rest
	}
	if len(q.actions) > 0 {
		w.send(q, pred)
		w.dirty = true
	}
	w.updateResult()
}

// describeActions is a short text of stack request actions, for debug logs.
func describeActions(as []protocol.StackRequestAction) string {
	var b strings.Builder
	slot := func(s protocol.StackRequestSlotInfo) string {
		return fmt.Sprintf("%d/%d#%d", s.Container.ContainerID, s.Slot, s.StackNetworkID)
	}
	for _, a := range as {
		switch a := a.(type) {
		case *protocol.TakeStackRequestAction:
			fmt.Fprintf(&b, "take %d %s->%s; ", a.Count, slot(a.Source), slot(a.Destination))
		case *protocol.PlaceStackRequestAction:
			fmt.Fprintf(&b, "place %d %s->%s; ", a.Count, slot(a.Source), slot(a.Destination))
		case *protocol.SwapStackRequestAction:
			fmt.Fprintf(&b, "swap %s<->%s; ", slot(a.Source), slot(a.Destination))
		case *protocol.DropStackRequestAction:
			fmt.Fprintf(&b, "drop %d %s; ", a.Count, slot(a.Source))
		case *protocol.DestroyStackRequestAction:
			fmt.Fprintf(&b, "destroy %d %s; ", a.Count, slot(a.Source))
		case *protocol.ConsumeStackRequestAction:
			fmt.Fprintf(&b, "consume %d %s; ", a.Count, slot(a.Source))
		case *protocol.CraftRecipeStackRequestAction:
			fmt.Fprintf(&b, "craft %d x%d; ", a.RecipeNetworkID, a.NumberOfCrafts)
		case *protocol.CraftCreativeStackRequestAction:
			fmt.Fprintf(&b, "creative %d; ", a.CreativeItemNetworkID)
		case *protocol.CraftRecipeOptionalStackRequestAction:
			fmt.Fprintf(&b, "craft optional %d filter %d; ", a.RecipeNetworkID, a.FilterStringIndex)
		case *protocol.CraftGrindstoneRecipeStackRequestAction:
			fmt.Fprintf(&b, "grindstone %d cost %d; ", a.RecipeNetworkID, a.Cost)
		case *protocol.CraftLoomRecipeStackRequestAction:
			fmt.Fprintf(&b, "loom %s; ", a.Pattern)
		case *protocol.MineBlockStackRequestAction:
			fmt.Fprintf(&b, "mine slot %d durability %d #%d; ", a.HotbarSlot, a.PredictedDurability, a.StackNetworkID)
		case *protocol.BeaconPaymentStackRequestAction:
			fmt.Fprintf(&b, "beacon %d %d; ", a.PrimaryEffect, a.SecondaryEffect)
		default:
			fmt.Fprintf(&b, "%T; ", a)
		}
	}
	return b.String()
}

func describeChanges(cs []*change) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%d/%d %s x%d -> %s x%d; ", c.k.c, c.k.s, jitem.Name(c.bid), c.bn, jitem.Name(c.aid), c.an)
	}
	return b.String()
}

// describeInventory lists the server's non-empty slots (for /bb inv and tests).
func (w *Windows) describeInventory() string {
	keys := make([]slotKey, 0, len(w.auth))
	for k, inst := range w.auth {
		if inst.Stack.NetworkID != 0 && inst.Stack.Count > 0 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].c != keys[j].c {
			return keys[i].c < keys[j].c
		}
		return keys[i].s < keys[j].s
	})
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%d/%d:%s; ", k.c, k.s, w.p.describeStack(w.auth[k].Stack))
	}
	return b.String()
}
