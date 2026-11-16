package bridge

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Stations (milestone 3): the enchanting table (the server's options as the Java window's costs
// and hints, a button click as the option's craft) and the beacon (set_beacon as BeaconPayment).
// Inputs of every station are moved with plain clicks (inventory.go). Results of the anvil,
// grindstone, stonecutter, smithing table, loom and cartography table are in station_results.go
// (the stonecutter's and loom's buttons, the anvil's name land there too).

func init() {
	OnBedrock(packet.IDPlayerEnchantOptions, func(p *Player, pk *packet.PlayerEnchantOptions) {
		p.win.enchant = pk.Options
		p.win.sendEnchantOptions()
	})
	OnJava(v777.ServerboundPlayContainerButtonClick, func(p *Player, r *wireReader) error {
		window, button := r.VarInt(), r.VarInt()
		if r.Err != nil || p.rem == nil {
			return r.Err
		}
		w := p.win
		if c := w.cur; c != nil && c.shown && c.jid == window {
			if c.typ == protocol.ContainerTypeEnchantment {
				w.enchantClick(int(button))
			} else {
				w.stationButton(c, int(button))
			}
		}
		return nil
	})
	OnJava(v777.ServerboundPlayRenameItem, func(p *Player, r *wireReader) error {
		name := r.String(32767)
		if r.Err != nil || p.rem == nil {
			return r.Err
		}
		p.win.renameItem(name)
		return nil
	})
	OnJava(v777.ServerboundPlaySetBeacon, func(p *Player, r *wireReader) error {
		var prim, sec int32 = -1, -1
		if r.Bool() {
			prim = r.VarInt()
		}
		if r.Bool() {
			sec = r.VarInt()
		}
		if r.Err != nil || p.rem == nil {
			return r.Err
		}
		w := p.win
		if c := w.cur; c != nil && c.typ == protocol.ContainerTypeBeacon {
			w.beaconPay(prim, sec)
		}
		return nil
	})
}

// sendEnchantOptions shows the server's enchantment options in the Java enchanting window.
func (w *Windows) sendEnchantOptions() {
	c := w.cur
	if c == nil || !c.shown || c.typ != protocol.ContainerTypeEnchantment {
		return
	}
	for i := 0; i < 3; i++ {
		cost, hint, level := int16(0), int16(-1), int16(-1)
		if i < len(w.enchant) {
			o := w.enchant[i]
			cost = int16(o.Cost)
			for _, list := range o.Enchantments.Enchantments {
				if len(list) > 0 && hint < 0 {
					if int(list[0].Type) < len(javaEnchantment()) {
						hint = int16(javaEnchantment()[list[0].Type])
					}
					level = int16(list[0].Level)
				}
			}
		}
		w.sendData(int16(i), cost)
		w.sendData(int16(4+i), hint)
		w.sendData(int16(7+i), level)
	}
}

// enchantClick takes an enchantment option: the option's craft, the item and the lapis consumed,
// the enchanted item back into the input slot.
func (w *Windows) enchantClick(button int) {
	if button < 0 || button >= len(w.enchant) {
		return
	}
	opt := w.enchant[button]
	in, lapis := uiKey(14), uiKey(15)
	item, lap := w.auth[in], w.auth[lapis]
	need := button + 1
	if item.Stack.Count == 0 || (w.p.self.mode != GameCreative && int(lap.Stack.Count) < need) {
		return
	}
	q := w.newRequest()
	q.actions = append(q.actions, &protocol.CraftRecipeStackRequestAction{RecipeNetworkID: opt.RecipeNetworkID, NumberOfCrafts: 1})
	consume := func(k slotKey, inst protocol.ItemInstance, n int) {
		a := &protocol.ConsumeStackRequestAction{}
		a.Count, a.Source = byte(n), q.info(k, inst)
		q.actions = append(q.actions, a)
		q.touch(k)
	}
	consume(in, item, 1)
	pred := map[slotKey]protocol.ItemInstance{}
	if w.p.self.mode != GameCreative {
		consume(lapis, lap, need)
		lap.Stack.Count -= uint16(need)
		lap.StackNetworkID = q.id
		if lap.Stack.Count == 0 {
			lap = protocol.ItemInstance{}
		}
		pred[lapis] = lap
	}
	q.move(1, q.createdOutput(), q.info(in, protocol.ItemInstance{}))
	// The enchanted item: the input with the option's enchantments (the server sends no slot).
	res := item
	nbt := copyNBT(res.Stack.NBTData)
	var ench []any
	for _, list := range opt.Enchantments.Enchantments {
		for _, e := range list {
			ench = append(ench, map[string]any{"id": int16(e.Type), "lvl": int16(e.Level)})
		}
	}
	nbt["ench"] = ench
	res.Stack.NBTData = nbt
	res.StackNetworkID = q.id
	if w.p.items().names[res.Stack.NetworkID] == "minecraft:book" {
		if id, ok := w.p.items().ids["minecraft:enchanted_book"]; ok {
			res.Stack.NetworkID = id
		}
	}
	pred[in] = res
	w.send(q, pred)
	w.resync()
}

// sendBeaconData gives the Java beacon screen its pyramid levels (which effects it offers; the
// server does not send them, so they are counted from the world) and the effects set (the block
// entity's primary/secondary; Java's data is the effect id + 1, which is Bedrock's id).
func (w *Windows) sendBeaconData() {
	p := w.p
	c := w.cur
	if c == nil || c.typ != protocol.ContainerTypeBeacon || p.world == nil {
		return
	}
	levels := 0
	base := func(n string) bool {
		switch n {
		case "minecraft:iron_block", "minecraft:gold_block", "minecraft:diamond_block", "minecraft:emerald_block", "minecraft:netherite_block":
			return true
		}
		return false
	}
layers:
	for l := 1; l <= 4; l++ {
		for x := -l; x <= l; x++ {
			for z := -l; z <= l; z++ {
				b, ok := p.world.Block(cube.Pos{int(c.pos[0]) + x, int(c.pos[1]) - l, int(c.pos[2]) + z})
				if !ok || !base(p.rem.pal.Name(b)) {
					break layers
				}
			}
		}
		levels = l
	}
	var prim, sec int64
	pos := cube.Pos{int(c.pos[0]), int(c.pos[1]), int(c.pos[2])}
	if col := p.world.cols[chunkOf(pos)]; col != nil {
		prim, _ = nbtNum(col.be[pos]["primary"])
		sec, _ = nbtNum(col.be[pos]["secondary"])
	}
	w.sendData(0, int16(levels))
	w.sendData(1, int16(prim))
	w.sendData(2, int16(sec))
}

// beaconPay pays the beacon for its effects (Java effect ids are Bedrock's minus one).
func (w *Windows) beaconPay(prim, sec int32) {
	k := uiKey(27)
	inst := w.auth[k]
	if inst.Stack.Count == 0 {
		return
	}
	eff := func(id int32) int32 {
		if id < 0 {
			return 0
		}
		return id + 1
	}
	q := w.newRequest()
	q.actions = append(q.actions, &protocol.BeaconPaymentStackRequestAction{PrimaryEffect: eff(prim), SecondaryEffect: eff(sec)})
	q.actions = append(q.actions, &protocol.DestroyStackRequestAction{Count: 1, Source: q.info(k, inst)})
	after := inst
	after.Stack.Count--
	after.StackNetworkID = q.id
	if after.Stack.Count == 0 {
		after = protocol.ItemInstance{}
	}
	w.send(q, map[slotKey]protocol.ItemInstance{k: after})
	w.resync()
}
