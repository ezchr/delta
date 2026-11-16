package bridge

import (
	"fmt"
	"strings"
	"testing"

	"delta/core/internal/javamap"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/version"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Shift-clicks (QUICK_MOVE): the Java client moves the stack itself (vanilla's quickMoveStack:
// stacks of the item first, then empty slots, part of the stack when space runs out) and sends
// the slots it changed; the bridge asks BDS for the same moves. And what happens when BDS refuses
// one: BDS refuses a slot whose stack id is not its own and every main inventory slot when its
// inventory screen is not open (FailedToValidateSrcSlot/DstSlot, 49/50, seen on 1.26.60).

var qmItems = []protocol.ItemEntry{{Name: "minecraft:dirt", RuntimeID: 3}, {Name: "minecraft:cobblestone", RuntimeID: 4},
	{Name: "minecraft:iron_chestplate", RuntimeID: 5}}

func qmStack(rid int32, n int, id int32) protocol.ItemInstance {
	return protocol.ItemInstance{StackNetworkID: id, Stack: protocol.ItemStack{ItemType: protocol.ItemType{NetworkID: rid}, Count: uint16(n)}}
}

func qmJava(t *testing.T, name string) int32 {
	t.Helper()
	id, ok := javamap.ItemID(name, 0)
	if !ok {
		t.Fatalf("no Java item %s", name)
	}
	return id
}

// qmPlayer is a survival player whose server inventory screen is not open yet.
func qmPlayer(t *testing.T) (*Player, func() []packet.Packet) {
	p, _ := soundPlayer(t, version.Newest)
	p.rem.gd.Items = qmItems
	sent := func() []packet.Packet {
		out := p.rem.testOut
		p.rem.testOut = nil
		return out
	}
	return p, sent
}

// describeSent is the Bedrock packets sent, short: "open", "close", "mismatch", or a request's
// id and actions.
func describeSent(pks []packet.Packet) []string {
	var out []string
	for _, pk := range pks {
		switch pk := pk.(type) {
		case *packet.Interact:
			if pk.ActionType == packet.InteractActionOpenInventory {
				out = append(out, "open")
			}
		case *packet.ContainerClose:
			out = append(out, "close")
		case *packet.InventoryTransaction:
			if _, ok := pk.TransactionData.(*protocol.MismatchTransactionData); ok {
				out = append(out, "mismatch")
			}
		case *packet.ItemStackRequest:
			for _, r := range pk.Requests {
				out = append(out, fmt.Sprintf("%d: %s", r.RequestID, strings.TrimSuffix(describeActions(r.Actions), "; ")))
			}
		}
	}
	return out
}

func expectSent(t *testing.T, what string, got []packet.Packet, want ...string) {
	t.Helper()
	d := describeSent(got)
	if strings.Join(d, " | ") != strings.Join(want, " | ") {
		t.Fatalf("%s:\n got  %q\n want %q", what, d, want)
	}
}

func runTicks(p *Player, n int) {
	for range n {
		for _, f := range tickHooks {
			f(p)
		}
	}
}

func TestQuickMovePlayerInventory(t *testing.T) {
	p, sent := qmPlayer(t)
	w := p.win
	dirt, cobble := qmJava(t, "minecraft:dirt"), qmJava(t, "minecraft:cobblestone")
	hb := func(i byte) slotKey { return slotKey{protocol.ContainerHotBar, i} }
	inv := func(i byte) slotKey { return slotKey{protocol.ContainerInventory, i} }
	w.auth[hb(0)] = qmStack(3, 64, 52)
	w.auth[inv(9)] = qmStack(3, 10, 60)
	w.auth[hb(1)] = qmStack(4, 40, 54)
	h := func(id int32, n int) jitem.HashedStack { return jitem.HashedStack{ID: id, Count: int32(n)} }
	none := jitem.HashedStack{}

	// Hotbar 0 (Java 36) to the inventory: 54 onto the stack of 10 in slot 9, the 10 left into
	// the first empty slot (10). The server's inventory screen is opened first.
	w.click(0, 36, 0, 1, map[int]jitem.HashedStack{36: none, 9: h(dirt, 64), 10: h(dirt, 10)}, none)
	expectSent(t, "partial stack, then an empty slot", sent(), "open", "-1: place 54 28/0#52->29/9#60; place 10 28/0#-1->29/10#0")

	// Before the answer: slot 9 back to the hotbar, named by the pending request's id.
	w.click(0, 9, 0, 1, map[int]jitem.HashedStack{9: none, 36: h(dirt, 64)}, none)
	expectSent(t, "on a pending prediction", sent(), "-3: place 64 29/9#-1->28/0#0")

	// The server accepts the first and refuses the second (its screen or its ids are not what
	// the bridge thinks): the slots go back to the first's answer, the inventory is asked for
	// again (once for a burst of refusals) and the next click opens the server's screen again.
	w.responses([]protocol.ItemStackResponse{
		{Status: protocol.ItemStackResponseStatusOK, RequestID: -1, ContainerInfo: []protocol.StackResponseContainerInfo{
			{Container: protocol.FullContainerName{ContainerID: protocol.ContainerHotBar}, SlotInfo: []protocol.StackResponseSlotInfo{{Slot: 0, Count: 0}}},
			{Container: protocol.FullContainerName{ContainerID: protocol.ContainerInventory}, SlotInfo: []protocol.StackResponseSlotInfo{
				{Slot: 9, Count: 64, StackNetworkID: 60}, {Slot: 10, Count: 10, StackNetworkID: 70}}},
		}},
		{Status: protocol.ItemStackResponseStatusFailedToValidateDstSlot, RequestID: -3},
	})
	if got := w.auth[inv(9)]; got.Stack.Count != 64 || got.StackNetworkID != 60 || w.auth[hb(0)].Stack.Count != 0 {
		t.Fatalf("after the refusal: inv 9 %+v, hotbar 0 %+v", got, w.auth[hb(0)])
	}
	sent()
	runTicks(p, 1)
	expectSent(t, "after the refusal", sent(), "mismatch")
	w.refused() // another refusal right away
	runTicks(p, resendTicks-1)
	expectSent(t, "a refusal within the wait", sent())
	runTicks(p, 1)
	expectSent(t, "after the wait", sent(), "mismatch")

	// The server's answer to the mismatch: every inventory again (new stack ids here).
	content := make([]protocol.ItemInstance, 36)
	content[1] = qmStack(4, 40, 81)
	content[9] = qmStack(3, 64, 80)
	content[10] = qmStack(3, 10, 82)
	p.dispatchBedrock(&packet.InventoryContent{WindowID: protocol.WindowIDInventory, Content: content})
	if w.auth[inv(9)].StackNetworkID != 80 {
		t.Fatalf("inventory content not applied: %+v", w.auth[inv(9)])
	}

	// Cobblestone (hotbar 1, Java 37) to the inventory: the screen is opened again first, the
	// stack goes into the first empty slot (11), with the new ids.
	w.click(0, 37, 0, 1, map[int]jitem.HashedStack{37: none, 11: h(cobble, 40)}, none)
	expectSent(t, "after the refusal, next click", sent(), "open", "-5: place 40 28/1#81->29/11#0")
	w.click(0, 11, 0, 1, map[int]jitem.HashedStack{11: none, 37: h(cobble, 40)}, none)
	expectSent(t, "screen open again", sent(), "-7: place 40 29/11#-5->28/1#0")
}

// TestQuickMoveChestAndArmour: a stack split over a chest's partial stack and an empty slot
// (part of it stays when the chest is full), and armour shift-clicked onto its slot.
func TestQuickMoveChestAndArmour(t *testing.T) {
	p, sent := qmPlayer(t)
	w := p.win
	dirt, plate := qmJava(t, "minecraft:dirt"), qmJava(t, "minecraft:iron_chestplate")
	h := func(id int32, n int) jitem.HashedStack { return jitem.HashedStack{ID: id, Count: int32(n)} }
	none := jitem.HashedStack{}

	w.auth[slotKey{protocol.ContainerHotBar, 2}] = qmStack(5, 1, 40)
	// Window 0: the chestplate (hotbar 2, Java 38) onto the chest armour slot (Java 6).
	w.click(0, 38, 0, 1, map[int]jitem.HashedStack{38: none, 6: h(plate, 1)}, none)
	expectSent(t, "armour", sent(), "open", "-1: place 1 28/2#40->6/1#0")

	// A chest (window 1 on Java): 27 slots, all full but slot 0 (dirt x60) and slot 5 (empty).
	w.invOpen = false
	c := &window{bid: 7, typ: protocol.ContainerTypeContainer, cid: protocol.ContainerLevelEntity, size: 27}
	w.cur = c
	if !w.windowLayout(c) {
		t.Fatal("no chest layout")
	}
	c.shown, c.jid, w.open = true, 1, 1
	for i := 1; i < 27; i++ {
		w.auth[slotKey{protocol.ContainerLevelEntity, byte(i)}] = qmStack(4, 64, int32(100+i))
	}
	w.auth[slotKey{protocol.ContainerLevelEntity, 0}] = qmStack(3, 60, 100)
	delete(w.auth, slotKey{protocol.ContainerLevelEntity, 5})
	w.auth[slotKey{protocol.ContainerHotBar, 0}] = qmStack(3, 64, 52)
	w.auth[slotKey{protocol.ContainerHotBar, 1}] = qmStack(3, 64, 53)
	// Hotbar 0 (Java 54): 4 onto slot 0, 60 into slot 5.
	w.click(1, 54, 0, 1, map[int]jitem.HashedStack{54: none, 0: h(dirt, 64), 5: h(dirt, 60)}, none)
	expectSent(t, "into a chest", sent(), "-3: place 4 28/0#52->7/0#100; place 60 28/0#-3->7/5#0")
	// Hotbar 1 (Java 55): the chest has room for 4 only, 60 stay in the hotbar.
	w.click(1, 55, 0, 1, map[int]jitem.HashedStack{55: h(dirt, 60), 5: h(dirt, 64)}, none)
	expectSent(t, "part of the stack", sent(), "-5: place 4 28/1#53->7/5#-3")
}

// TestInventoryScreenClosedByServer: BDS names the inventory screen by the window id it opened
// it with (not 0) when it closes it; the next click must open it again, or every main inventory
// slot is refused.
func TestInventoryScreenClosedByServer(t *testing.T) {
	p, sent := qmPlayer(t)
	w := p.win
	dirt := qmJava(t, "minecraft:dirt")
	w.auth[slotKey{protocol.ContainerHotBar, 0}] = qmStack(3, 64, 52)
	none := jitem.HashedStack{}
	w.click(0, 36, 0, 1, map[int]jitem.HashedStack{36: none, 9: {ID: dirt, Count: 64}}, none)
	expectSent(t, "first click", sent(), "open", "-1: place 64 28/0#52->29/9#0")
	p.dispatchBedrock(&packet.ContainerOpen{WindowID: 2, ContainerType: 0xff})
	w.responses([]protocol.ItemStackResponse{{Status: protocol.ItemStackResponseStatusOK, RequestID: -1, ContainerInfo: []protocol.StackResponseContainerInfo{
		{Container: protocol.FullContainerName{ContainerID: protocol.ContainerHotBar}, SlotInfo: []protocol.StackResponseSlotInfo{{Slot: 0}}},
		{Container: protocol.FullContainerName{ContainerID: protocol.ContainerInventory}, SlotInfo: []protocol.StackResponseSlotInfo{{Slot: 9, Count: 64, StackNetworkID: 52}}},
	}}})
	w.click(0, 9, 0, 1, map[int]jitem.HashedStack{9: none, 36: {ID: dirt, Count: 64}}, none)
	expectSent(t, "screen open", sent(), "-3: place 64 29/9#52->28/0#0")

	// The server closes the screen itself (by its window id).
	p.dispatchBedrock(&packet.ContainerClose{WindowID: 2, ContainerType: 0xf7, ServerSide: true})
	w.click(0, 36, 0, 1, map[int]jitem.HashedStack{36: none, 9: {ID: dirt, Count: 64}}, none)
	if got := describeSent(sent()); len(got) != 2 || got[0] != "open" {
		t.Fatalf("after the server closed the screen: %q, want an open first", got)
	}

	// Our close and an open right after it: BDS answers the close (old id), then the open (new
	// id); the screen is open in the end.
	w.invOpen, w.invWindow = true, 3
	p.dispatchBedrock(&packet.ContainerClose{WindowID: 3, ContainerType: 0xf7})
	p.dispatchBedrock(&packet.ContainerOpen{WindowID: 4, ContainerType: 0xff})
	if !w.invOpen || w.invWindow != 4 {
		t.Fatalf("close then open: open %v window %d", w.invOpen, w.invWindow)
	}

	// A chest's close whose id an old inventory screen had does not touch the inventory.
	w.invOpen, w.invWindow = true, 5
	w.cur = &window{bid: 5, typ: protocol.ContainerTypeContainer}
	p.dispatchBedrock(&packet.ContainerClose{WindowID: 5})
	if !w.invOpen {
		t.Fatal("a chest's close taken for the inventory's")
	}
}
