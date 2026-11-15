package bridge

import (
	"bytes"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Mount inventories (horse, donkey, mule, llama, camel...). The server sends UpdateEquip (which
// of the window's slots are equipment: saddle 0, a horse's armour or a llama's carpet 1), then
// ContainerOpen of type horse and the window's content: the equipment slots, then the chest. The
// Java client gets mount_screen_open for the entity (slot 0 saddle, 1 body armour, then the chest
// in columns of three). Equipment slots are the horse equipment container in requests, the chest
// the entity's own container.

// pendingEquip is the last UpdateEquip, until its window opens.
type pendingEquip struct {
	window byte
	entity int64
	equip  map[int]bool
}

func init() {
	OnBedrock(packet.IDUpdateEquip, func(p *Player, pk *packet.UpdateEquip) {
		var data map[string]any
		_ = nbt.NewDecoderWithEncoding(bytes.NewReader(pk.SerialisedInventoryData), nbt.NetworkLittleEndian).Decode(&data)
		eq := map[int]bool{}
		slots, _ := data["slots"].([]any)
		for _, s := range slots {
			if m, ok := s.(map[string]any); ok {
				n, _ := nbtNum(m["slotNumber"])
				eq[int(n)] = true
			}
		}
		p.log.Debug("mount inventory", "window", pk.WindowID, "size", pk.Size, "entity", pk.EntityUniqueID, "equipment", eq)
		w := p.win
		w.equipNext = &pendingEquip{window: pk.WindowID, entity: pk.EntityUniqueID, equip: eq}
		if c := w.cur; c != nil && c.typ == protocol.ContainerTypeHorse && c.bid == pk.WindowID {
			c.equip = eq
			return
		}
		// BDS opens the window with this alone (no ContainerOpen): it shows with its content.
		if w.cur != nil && w.cur.shown {
			w.closeJava()
		}
		w.cur = &window{bid: pk.WindowID, typ: protocol.ContainerTypeHorse, entity: pk.EntityUniqueID, cid: protocol.ContainerLevelEntity, equip: eq}
		w.invOpen = false
	})
	// The Java client asks for its mount's inventory with player_command open_inventory (riding).
	OnJava(v777.ServerboundPlayPlayerCommand, func(p *Player, r *wireReader) error {
		r.VarInt()
		action := r.VarInt()
		if r.Err != nil || p.rem == nil || p.inWaiting || action != 5 { // 5: open_inventory
			return r.Err
		}
		if p.ents.ride.uniq != 0 {
			p.Write(&packet.Interact{ActionType: packet.InteractActionOpenInventory, TargetEntityRuntimeID: p.rem.gd.EntityRuntimeID})
		}
		return nil
	})
}

// equipSlots are a mount's equipment slot numbers in order (0 saddle, 1 body).
func (c *window) equipSlots() []int {
	var out []int
	for s := 0; s < 8; s++ {
		if c.equip[s] {
			out = append(out, s)
		}
	}
	return out
}

// mountKey is index i of a mount window's content. Its equipment comes first, as the horse
// equipment container counted from 0 (a llama's carpet, equipment slot 1, is its slot 0: BDS's
// HorseEquipContainerValidation shifts the slots of a mount without a saddle); the rest is the
// entity's own container at the same index (its slots under the equipment are not used).
func (c *window) mountKey(i int) slotKey {
	if i < len(c.equipSlots()) {
		return slotKey{protocol.ContainerHorseEquip, byte(i)}
	}
	return slotKey{protocol.ContainerLevelEntity, byte(i)}
}

// mountLayout sets a mount window's Java slots from its content size.
func (c *window) mountLayout() {
	eq := c.equipSlots()
	n := c.size
	if n <= 0 {
		n = len(eq)
	}
	c.columns = max(0, n-len(eq)) / 3
	c.slots = make([]slotKey, 2+c.columns*3)
	for j := range 2 {
		c.slots[j] = slotKey{cVirtual, 1}
		for e, s := range eq {
			if s == j {
				c.slots[j] = c.mountKey(e)
			}
		}
	}
	for j := 2; j < len(c.slots); j++ {
		c.slots[j] = c.mountKey(len(eq) + j - 2)
	}
}

// showMount opens the Java mount screen of the open mount window (false: its entity is not on the
// Java client).
func (w *Windows) showMount(c *window) bool {
	p := w.p
	rid, ok := p.ents.RuntimeOf(c.entity)
	if !ok {
		return false
	}
	jid, ok := p.ents.Lookup(rid)
	if !ok {
		return false
	}
	c.mountLayout()
	w.nextID = w.nextID%100 + 1
	c.jid = w.nextID
	c.shown = true
	c.layout = nil
	w.open = c.jid
	wr := p.pk()
	wr.VarInt(c.jid)
	wr.VarInt(int32(c.columns))
	wr.Int32(jid)
	p.send(v777.ClientboundPlayMountScreenOpen, wr)
	p.log.Debug("mount screen", "window", c.bid, "columns", c.columns, "entity", jid)
	w.resync()
	return true
}
