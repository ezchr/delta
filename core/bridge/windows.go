package bridge

import (
	"strings"

	"github.com/df-mc/dragonfly/server/block/cube"
	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Windows and inventories (milestone 3). inventory.go keeps the server's slots and turns Java
// clicks into item stack requests; this file opens the server's windows (ContainerOpen) as Java
// screens with their slot layout, closes them both ways and shows their data (furnace and
// brewing progress); recipes.go computes crafting results and creative items; interact.go is
// block breaking/placing and item use; forms.go the server's forms.
//
// Java window ids: 0 is the player's inventory; a server window gets the next id of 1-100.

// Windows is the windows state of a player.
type Windows struct {
	p *Player
	// open is the Java window id open (0: the player inventory).
	open int32
	// state is free for later use.
	state map[string]any

	itab    *itemTable
	prot    *jitem.Proto
	protSet bool
	scratch jitem.Stack

	auth    map[slotKey]protocol.ItemInstance // the server's slots
	held    int                               // selected hotbar slot
	invOpen bool                              // the server was told the inventory is open
	nextReq int32
	pending []*pendingReq
	stateID int32
	dirty   bool // resync the open window at the next tick

	// invWindow is the server's window id of its open inventory screen (its ContainerOpen of
	// type 0xff; 0: none). The server closes that screen by this id, not by 0.
	invWindow byte
	// invReopen: a request was refused; open the inventory again before the next click in it
	// (the server's screen may be gone; an open while it is open is ignored by BDS).
	invReopen bool
	// resend: ask the server for the inventory again at the next tick (a refused request);
	// resendWait ticks are left before the next such ask.
	resend     bool
	resendWait int

	cur    *window // the server's open window
	nextID int32   // last Java window id given

	recipes  *recipeBook
	result   *craftResult // what the open crafting grid makes
	creative []creativeEntry
	cindex   map[string]uint32

	enchant []protocol.EnchantmentOption

	sta          stationSel    // the open station's choices (station_results.go)
	recipesDirty bool          // update_recipes to send at the next tick
	equipNext    *pendingEquip // the last UpdateEquip (mounts.go)

	forms formState
	act   actState
}

func newWindows(p *Player) *Windows {
	return &Windows{p: p, state: map[string]any{}, auth: map[slotKey]protocol.ItemInstance{}, nextReq: -1}
}

func init() {
	OnReset(func(p *Player, newConn bool) {
		w := p.win
		w.open = 0
		if w.cur != nil {
			w.cur = nil
		}
		w.result = nil
		w.act = actState{}
		w.resetStation()
		w.equipNext = nil
		if newConn {
			clear(w.state)
			clear(w.auth)
			w.held, w.invOpen, w.pending, w.dirty = 0, false, nil, false
			w.invWindow, w.invReopen, w.resend, w.resendWait = 0, false, false, 0
			w.recipes, w.creative, w.cindex, w.itab = nil, nil, nil, nil
			w.forms = formState{}
		}
	})
}

// proto is the Java item numbering of the client's version.
func (w *Windows) proto() *jitem.Proto {
	if !w.protSet {
		w.prot, w.protSet = jitem.For(w.p.ver()), true
	}
	return w.prot
}

// window is a window the server opened.
type window struct {
	bid    byte // Bedrock window id
	typ    byte // Bedrock container type
	pos    protocol.BlockPos
	entity int64
	cid    byte // the container of its slots (chests, barrels...)
	size   int  // slots of its own
	jid    int32
	menu   string
	title  string
	slots  []slotKey // the Java slots of its own
	layout []slotKey // slots + inventory + hotbar
	shown  bool
	wait   int // ticks waited for the content

	trade   *tradeWindow // a trade window's offers (trade.go)
	equip   map[int]bool // a mount window's equipment slots (mounts.go)
	columns int          // a mount window's chest columns
}

// stationTypes are the windows without an inventory of their own (inputs in the UI inventory).
var stationTypes = map[byte]bool{
	protocol.ContainerTypeWorkbench: true, protocol.ContainerTypeAnvil: true, protocol.ContainerTypeEnchantment: true,
	protocol.ContainerTypeGrindstone: true, protocol.ContainerTypeStonecutter: true, protocol.ContainerTypeSmithingTable: true,
	protocol.ContainerTypeLoom: true, protocol.ContainerTypeBeacon: true, protocol.ContainerTypeCartography: true,
}

func (c *window) station() bool { return stationTypes[c.typ] }

func furnaceLike(typ byte) bool {
	switch typ {
	case protocol.ContainerTypeFurnace, protocol.ContainerTypeBlastFurnace, protocol.ContainerTypeSmoker:
		return true
	}
	return false
}

// contentKey is the slot of index i of the window's InventoryContent/InventorySlot.
func (c *window) contentKey(i int, name protocol.FullContainerName) (slotKey, bool) {
	switch {
	case furnaceLike(c.typ):
		ing := byte(protocol.ContainerFurnaceIngredient)
		if c.typ == protocol.ContainerTypeBlastFurnace {
			ing = protocol.ContainerBlastFurnaceIngredient
		} else if c.typ == protocol.ContainerTypeSmoker {
			ing = protocol.ContainerSmokerIngredient
		}
		ids := []byte{ing, protocol.ContainerFurnaceFuel, protocol.ContainerFurnaceResult}
		if i >= 0 && i < 3 {
			return slotKey{ids[i], byte(i)}, true
		}
		return slotKey{}, false
	case c.typ == protocol.ContainerTypeBrewingStand:
		switch {
		case i == 0:
			return slotKey{protocol.ContainerBrewingStandInput, 0}, true
		case i >= 1 && i <= 3:
			return slotKey{protocol.ContainerBrewingStandResult, byte(i)}, true
		case i == 4:
			return slotKey{protocol.ContainerBrewingStandFuel, 4}, true
		}
		return slotKey{}, false
	case c.station():
		return slotKey{}, false
	case c.typ == protocol.ContainerTypeHorse:
		if i < 0 || i >= 54 {
			return slotKey{}, false
		}
		return c.mountKey(i), true
	}
	if i < 0 || i >= 54 {
		return slotKey{}, false
	}
	return slotKey{c.cid, byte(i)}, true
}

// setContent fixes a plain window's size and container from its first content.
func (c *window) setContent(n int, name protocol.FullContainerName) {
	if furnaceLike(c.typ) || c.typ == protocol.ContainerTypeBrewingStand || c.station() || c.shown {
		return
	}
	if c.typ == protocol.ContainerTypeHorse {
		c.size = min(n, 54)
		return
	}
	if name.ContainerID != 0 {
		c.cid = name.ContainerID
	}
	if n > 0 && n <= 54 {
		c.size = n
	}
}

// Java menu ids.
func menuID(name string) int32 { return v777.BuiltinID("minecraft:menu", "minecraft:"+name) }

// windowLayout sets the Java menu and slots of a window.
func (w *Windows) windowLayout(c *window) bool {
	keys := func(cid byte, from, n int) []slotKey {
		out := make([]slotKey, n)
		for i := range out {
			out[i] = slotKey{cid, byte(from + i)}
		}
		return out
	}
	switch c.typ {
	case protocol.ContainerTypeFurnace, protocol.ContainerTypeBlastFurnace, protocol.ContainerTypeSmoker:
		c.menu = map[byte]string{protocol.ContainerTypeFurnace: "furnace", protocol.ContainerTypeBlastFurnace: "blast_furnace",
			protocol.ContainerTypeSmoker: "smoker"}[c.typ]
		for i := 0; i < 3; i++ {
			k, _ := c.contentKey(i, protocol.FullContainerName{})
			c.slots = append(c.slots, k)
		}
	case protocol.ContainerTypeBrewingStand:
		c.menu = "brewing_stand"
		c.slots = []slotKey{{protocol.ContainerBrewingStandResult, 1}, {protocol.ContainerBrewingStandResult, 2},
			{protocol.ContainerBrewingStandResult, 3}, {protocol.ContainerBrewingStandInput, 0}, {protocol.ContainerBrewingStandFuel, 4}}
	case protocol.ContainerTypeWorkbench:
		c.menu = "crafting"
		c.slots = append([]slotKey{resultKey}, keys(protocol.ContainerCraftingInput, 32, 9)...)
	case protocol.ContainerTypeAnvil:
		c.menu = "anvil"
		c.slots = []slotKey{uiKey(1), uiKey(2), resultKey}
	case protocol.ContainerTypeEnchantment:
		c.menu = "enchantment"
		c.slots = []slotKey{uiKey(14), uiKey(15)}
	case protocol.ContainerTypeGrindstone:
		c.menu = "grindstone"
		c.slots = []slotKey{uiKey(16), uiKey(17), resultKey}
	case protocol.ContainerTypeStonecutter:
		c.menu = "stonecutter"
		c.slots = []slotKey{uiKey(3), resultKey}
	case protocol.ContainerTypeSmithingTable:
		c.menu = "smithing"
		c.slots = []slotKey{uiKey(53), uiKey(51), uiKey(52), resultKey}
	case protocol.ContainerTypeLoom:
		c.menu = "loom"
		c.slots = []slotKey{uiKey(9), uiKey(10), uiKey(11), resultKey}
	case protocol.ContainerTypeCartography:
		c.menu = "cartography_table"
		c.slots = []slotKey{uiKey(12), uiKey(13), resultKey}
	case protocol.ContainerTypeBeacon:
		c.menu = "beacon"
		c.slots = []slotKey{uiKey(27)}
	case protocol.ContainerTypeTrade:
		c.menu = "merchant"
		c.slots = []slotKey{tradeInA, tradeInB, resultKey}
	case protocol.ContainerTypeHopper, protocol.ContainerTypeCartHopper:
		c.menu = "hopper"
		c.slots = keys(c.cid, 0, 5)
	case protocol.ContainerTypeDispenser, protocol.ContainerTypeDropper, protocol.ContainerTypeCrafter:
		c.menu = "generic_3x3"
		c.slots = keys(c.cid, 0, 9)
	case protocol.ContainerTypeContainer, protocol.ContainerTypeCartChest, protocol.ContainerTypeChestBoat:
		n := c.size
		if n <= 0 {
			n = 27
		}
		rows := min(6, max(1, (n+8)/9))
		c.menu = "generic_9x" + string(rune('0'+rows))
		if c.blockName() != "" && strings.HasSuffix(c.blockName(), "shulker_box") && n == 27 {
			c.menu = "shulker_box"
		}
		c.slots = keys(c.cid, 0, min(n, rows*9))
		for len(c.slots) < rows*9 {
			c.slots = append(c.slots, slotKey{cVirtual, 1})
		}
	default:
		return false
	}
	return true
}

// blockName is the server's block at the window's position ("" if unknown).
func (c *window) blockName() string { return c.title }

// windowTitle is the Java title of a window: the block entity's custom name, else the block's.
func (w *Windows) windowTitle(c *window) string {
	p := w.p
	pos := cube.Pos{int(c.pos[0]), int(c.pos[1]), int(c.pos[2])}
	if p.world != nil && (c.entity == -1 || c.entity == 0) {
		if col := p.world.cols[chunkOf(pos)]; col != nil {
			if n, ok := col.be[pos]["CustomName"].(string); ok && n != "" {
				return n
			}
		}
	}
	switch c.typ {
	case protocol.ContainerTypeFurnace:
		return "Furnace"
	case protocol.ContainerTypeBlastFurnace:
		return "Blast Furnace"
	case protocol.ContainerTypeSmoker:
		return "Smoker"
	case protocol.ContainerTypeBrewingStand:
		return "Brewing Stand"
	case protocol.ContainerTypeWorkbench:
		return "Crafting"
	case protocol.ContainerTypeAnvil:
		return "Repair & Name"
	case protocol.ContainerTypeEnchantment:
		return "Enchant"
	case protocol.ContainerTypeGrindstone:
		return "Repair & Disenchant"
	case protocol.ContainerTypeStonecutter:
		return "Stonecutter"
	case protocol.ContainerTypeSmithingTable:
		return "Upgrade Gear"
	case protocol.ContainerTypeLoom:
		return "Loom"
	case protocol.ContainerTypeCartography:
		return "Cartography Table"
	case protocol.ContainerTypeBeacon:
		return "Beacon"
	case protocol.ContainerTypeTrade:
		if c.title != "" {
			return c.title
		}
		return "Trade"
	case protocol.ContainerTypeHopper:
		return "Hopper"
	case protocol.ContainerTypeCartHopper:
		return "Minecart with Hopper"
	case protocol.ContainerTypeDispenser:
		return "Dispenser"
	case protocol.ContainerTypeDropper:
		return "Dropper"
	case protocol.ContainerTypeCrafter:
		return "Crafter"
	case protocol.ContainerTypeCartChest:
		return "Minecart with Chest"
	case protocol.ContainerTypeChestBoat:
		return "Boat with Chest"
	}
	name := ""
	if p.world != nil {
		if b, ok := p.world.Block(pos); ok {
			name = p.rem.pal.Name(b)
		}
	}
	name = strings.TrimPrefix(name, "minecraft:")
	switch {
	case name == "barrel":
		return "Barrel"
	case name == "ender_chest":
		return "Ender Chest"
	case strings.HasSuffix(name, "shulker_box"):
		return "Shulker Box"
	case c.size > 27:
		return "Large Chest"
	}
	return "Chest"
}

func init() {
	OnBedrock(packet.IDContainerOpen, func(p *Player, pk *packet.ContainerOpen) {
		w := p.win
		p.log.Debug("container open", "window", pk.WindowID, "type", pk.ContainerType, "pos", pk.ContainerPosition, "entity", pk.ContainerEntityUniqueID)
		if pk.WindowID == protocol.WindowIDInventory || pk.ContainerType == 0xff {
			// The player's own inventory (our Interact's answer): the Java client opens it itself.
			// Its window id is how the server names it when it closes it.
			if pk.ContainerType == 0xff && w.cur == nil {
				w.invWindow, w.invOpen = pk.WindowID, true
			}
			return
		}
		if pk.ContainerType == protocol.ContainerTypeTrade {
			return // the trade window opens with UpdateTrade (trade.go)
		}
		if w.cur != nil && w.cur.shown {
			w.closeJava()
		}
		c := &window{bid: pk.WindowID, typ: pk.ContainerType, pos: pk.ContainerPosition, entity: pk.ContainerEntityUniqueID, cid: protocol.ContainerLevelEntity}
		w.cur = c
		w.invOpen, w.invWindow = false, 0
		pos := cube.Pos{int(c.pos[0]), int(c.pos[1]), int(c.pos[2])}
		if p.world != nil && (c.entity == -1 || c.entity == 0) {
			if b, ok := p.world.Block(pos); ok {
				name := p.rem.pal.Name(b)
				switch {
				case strings.HasSuffix(name, "shulker_box"):
					c.cid = protocol.ContainerShulkerBox
				case name == "minecraft:barrel":
					c.cid = protocol.ContainerBarrel
				}
				c.title = name
				if col := p.world.cols[chunkOf(pos)]; col != nil {
					if _, paired := col.be[pos]["pairx"]; paired && (name == "minecraft:chest" || name == "minecraft:trapped_chest") {
						c.size = 54
					}
				}
			}
		}
		switch {
		case pk.ContainerType == protocol.ContainerTypeHorse:
			// A mount inventory: its equipment slots came with UpdateEquip; it shows with its content.
			c.equip = map[int]bool{}
			if e := w.equipNext; e != nil && e.window == pk.WindowID {
				c.equip = e.equip
			}
		case c.station() || furnaceLike(c.typ) || c.typ == protocol.ContainerTypeBrewingStand:
			w.showWindow() // no content of its own to wait for (an empty furnace gets none)
		}
		// Plain containers wait for their content (its size: single or double chest), or ten ticks.
	})
	OnBedrock(packet.IDContainerClose, func(p *Player, pk *packet.ContainerClose) {
		w := p.win
		// The inventory screen closes as window 0 or by the id it opened with: the server's
		// answer to our close, or the server closing it itself (the Java client keeps its screen
		// open; the next click opens the server's again).
		if pk.WindowID == protocol.WindowIDInventory || w.invWindow != 0 && pk.WindowID == w.invWindow &&
			(w.cur == nil || w.cur.bid != pk.WindowID) {
			w.invOpen, w.invWindow = false, 0
		}
		if c := w.cur; c != nil && c.bid == pk.WindowID {
			if pk.ServerSide {
				w.returnItems()
			}
			if c.shown && w.open == c.jid {
				wr := p.pk()
				wr.VarInt(c.jid)
				p.send(v777.ClientboundPlayContainerClose, wr)
			}
			w.cur, w.open = nil, 0
			w.updateResult()
		}
	})
	OnBedrock(packet.IDContainerSetData, (*Player).containerData)
	OnJava(v777.ServerboundPlayContainerClose, func(p *Player, r *wireReader) error {
		id := r.VarInt()
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		w := p.win
		if id == 0 {
			w.returnItems()
			w.invReopen = false
			if w.invOpen {
				w.invOpen = false
				p.Write(&packet.ContainerClose{WindowID: protocol.WindowIDInventory, ContainerType: 0xff})
			}
			return nil
		}
		if c := w.cur; c != nil && c.jid == id {
			w.returnItems()
			p.Write(&packet.ContainerClose{WindowID: c.bid, ContainerType: c.typ})
			w.cur, w.open = nil, 0
			w.updateResult()
		}
		return nil
	})
}

// unsupported closes a server window the bridge cannot show.
func (w *Windows) unsupported(c *window) {
	p := w.p
	p.log.Info("no Java screen for this window", "type", c.typ)
	p.message("§7(this kind of window has no Java screen yet)")
	p.Write(&packet.ContainerClose{WindowID: c.bid, ContainerType: c.typ})
	w.cur = nil
}

// showWindow opens the Java screen of the server's open window.
func (w *Windows) showWindow() {
	p := w.p
	c := w.cur
	if c == nil || c.shown {
		return
	}
	w.resetStation()
	if c.typ == protocol.ContainerTypeHorse {
		if !w.showMount(c) {
			w.unsupported(c)
		}
		return
	}
	if !w.windowLayout(c) {
		w.unsupported(c)
		return
	}
	title := w.windowTitle(c)
	w.nextID = w.nextID%100 + 1
	c.jid = w.nextID
	c.shown = true
	c.layout = nil
	w.open = c.jid
	wr := p.pk()
	wr.VarInt(c.jid)
	wr.VarInt(menuID(c.menu))
	writeText(wr, title)
	p.send(v777.ClientboundPlayOpenScreen, wr)
	w.updateResult()
	w.resync()
	switch c.typ {
	case protocol.ContainerTypeFurnace:
		w.sendData(3, 200)
	case protocol.ContainerTypeBlastFurnace, protocol.ContainerTypeSmoker:
		w.sendData(3, 100)
	case protocol.ContainerTypeEnchantment:
		w.sendEnchantOptions()
	case protocol.ContainerTypeTrade:
		w.sendOffers()
	case protocol.ContainerTypeBeacon:
		w.sendBeaconData()
	}
}

// closeJava closes the Java screen of the server's window (another one replaces it).
func (w *Windows) closeJava() {
	p := w.p
	if c := w.cur; c != nil && c.shown {
		wr := p.pk()
		wr.VarInt(c.jid)
		p.send(v777.ClientboundPlayContainerClose, wr)
	}
	w.cur, w.open = nil, 0
}

// sendData sets a property of the open Java window.
func (w *Windows) sendData(key, val int16) {
	c := w.cur
	if c == nil || !c.shown {
		return
	}
	wr := w.p.pk()
	wr.VarInt(c.jid)
	wr.Int16(key)
	wr.Int16(val)
	w.p.send(v777.ClientboundPlayContainerSetData, wr)
}

// containerData shows the server's window data (furnace and brewing progress).
func (p *Player) containerData(pk *packet.ContainerSetData) {
	c := p.win.cur
	if c == nil || c.bid != pk.WindowID {
		return
	}
	key := int16(-1)
	switch {
	case furnaceLike(c.typ):
		// Bedrock: 0 cook progress, 1 burn time left, 2 burn duration. Java: 0 burn left, 1 burn
		// duration, 2 cook progress, 3 cook total.
		switch pk.Key {
		case 0:
			key = 2
		case 1:
			key = 0
		case 2:
			key = 1
		}
	case c.typ == protocol.ContainerTypeBrewingStand:
		// Bedrock: 0 brew time, 1 fuel, 2 fuel total. Java: 0 brew time, 1 fuel.
		switch pk.Key {
		case 0, 1:
			key = int16(pk.Key)
		}
	}
	if key >= 0 {
		p.win.sendData(key, int16(pk.Value))
	}
}
