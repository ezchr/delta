package bridge

import (
	"bytes"

	jitem "github.com/ezchr/go-mcjava/item"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Villager and wandering trader trading. The server opens a trade window with UpdateTrade (its
// offers as NBT, each with its recipe network id); the Java client gets the merchant screen and
// merchant_offers. The payment slots are the UI inventory's trade inputs (4 and 5; the new trade
// screen names them by its own containers in requests). Choosing a trade (select_trade) fills them
// from the inventory as vanilla's client does on its own; taking the result is the offer's
// CraftRecipe, the price consumed and the goods taken out of the created output slot. The server
// sends a new UpdateTrade after a trade (uses, the villager's level).

// tradeOffer is one offer of a trade window.
type tradeOffer struct {
	netID            uint32
	buyA, buyB, sell protocol.ItemStack // buy counts are the price asked
	uses, maxUses    int32
	xp               int32
}

func (o *tradeOffer) disabled() bool { return o.maxUses > 0 && o.uses >= o.maxUses }

// tradeWindow is the open trade window's offers.
type tradeWindow struct {
	offers []tradeOffer
	sel    int
	newUI  bool
	tier   int32 // 0-4
	trader bool  // a wandering trader (no level bar)
}

var (
	tradeInA = uiKey(4)
	tradeInB = uiKey(5)
)

func init() {
	stationTypes[protocol.ContainerTypeTrade] = true // no content of its own: the inputs are UI slots
	OnBedrock(packet.IDUpdateTrade, (*Player).updateTrade)
	OnJava(v777.ServerboundPlaySelectTrade, func(p *Player, r *wireReader) error {
		i := int(r.VarInt())
		if r.Err != nil || p.rem == nil || p.inWaiting {
			return r.Err
		}
		p.win.selectTrade(i)
		return nil
	})
}

// updateTrade opens (or refreshes) the trade window.
func (p *Player) updateTrade(pk *packet.UpdateTrade) {
	w := p.win
	var data map[string]any
	if err := nbt.NewDecoderWithEncoding(bytes.NewReader(pk.SerialisedOffers), nbt.NetworkLittleEndian).Decode(&data); err != nil {
		p.log.Info("trade offers not readable", "err", err)
		return
	}
	tw := &tradeWindow{sel: -1, newUI: pk.NewTradeUI, tier: pk.TradeTier}
	recipes, _ := data["Recipes"].([]any)
	for _, rv := range recipes {
		rm, ok := rv.(map[string]any)
		if !ok {
			continue
		}
		num := func(k string) int32 { v, _ := nbtNum(rm[k]); return int32(v) }
		o := tradeOffer{
			netID: uint32(num("netId")),
			buyA:  p.nbtItem(rm["buyA"], int(num("buyCountA"))),
			buyB:  p.nbtItem(rm["buyB"], int(num("buyCountB"))),
			sell:  p.nbtItem(rm["sell"], 0),
			uses:  num("uses"), maxUses: num("maxUses"), xp: num("traderExp"),
		}
		if !filled(o.buyA) || !filled(o.sell) {
			continue
		}
		tw.offers = append(tw.offers, o)
		p.log.Debug("trade offer", "buy", p.describeStack(o.buyA), "buy2", p.describeStack(o.buyB), "sell", p.describeStack(o.sell), "netId", o.netID, "uses", o.uses, "max", o.maxUses)
	}
	if rid, ok := p.ents.RuntimeOf(pk.VillagerUniqueID); ok {
		if e := p.ents.shown[rid]; e != nil && e.bedrock == "minecraft:wandering_trader" {
			tw.trader = true
		}
	}
	if c := w.cur; c != nil && c.typ == protocol.ContainerTypeTrade && c.bid == pk.WindowID && c.shown {
		if old := c.trade; old != nil && old.sel < len(tw.offers) {
			tw.sel = old.sel
		}
		c.trade = tw
		w.sendOffers()
		w.updateResult()
		return
	}
	if w.cur != nil && w.cur.shown {
		w.closeJava()
	}
	title := pk.DisplayName
	if title != "" {
		title = translate(title, nil)
	}
	w.cur = &window{bid: pk.WindowID, typ: protocol.ContainerTypeTrade, entity: pk.VillagerUniqueID, cid: protocol.ContainerLevelEntity, title: title, trade: tw}
	w.invOpen = false
	w.showWindow()
}

// nbtItem is the server stack of an item in NBT form (trade offers); count > 0 overrides its count.
func (p *Player) nbtItem(v any, count int) protocol.ItemStack {
	m, ok := v.(map[string]any)
	if !ok {
		return protocol.ItemStack{}
	}
	name, _ := m["Name"].(string)
	id, ok := p.items().ids[name]
	if name == "" || !ok {
		return protocol.ItemStack{}
	}
	n, _ := nbtNum(m["Count"])
	if count > 0 {
		n = int64(count)
	}
	meta, _ := nbtNum(m["Damage"])
	st := protocol.ItemStack{ItemType: protocol.ItemType{NetworkID: id, MetadataValue: uint32(meta)}, Count: uint16(max(n, 1))}
	if tag, ok := m["tag"].(map[string]any); ok && len(tag) > 0 {
		st.NBTData = tag
	}
	// Blocks: the block runtime id of the same creative item, so that placing it predicts.
	for _, c := range p.win.creative {
		if c.stack.NetworkID == id && c.stack.MetadataValue == st.MetadataValue && c.stack.BlockRuntimeID != 0 {
			st.BlockRuntimeID = c.stack.BlockRuntimeID
			break
		}
	}
	return st
}

// sendOffers sends merchant_offers for the open trade window.
func (w *Windows) sendOffers() {
	p := w.p
	c := w.cur
	if c == nil || c.trade == nil || !c.shown {
		return
	}
	tw := c.trade
	pr := w.proto()
	wr := p.pk()
	wr.VarInt(c.jid)
	wr.VarInt(int32(len(tw.offers)))
	cost := func(st protocol.ItemStack) {
		wr.VarInt(pr.Item(p.javaID(st)))
		wr.VarInt(int32(max(st.Count, 1)))
		wr.VarInt(0) // no component predicates
	}
	for i := range tw.offers {
		o := &tw.offers[i]
		cost(o.buyA)
		p.writeStack(wr, o.sell)
		wr.Bool(filled(o.buyB))
		if filled(o.buyB) {
			cost(o.buyB)
		}
		wr.Bool(o.disabled())
		wr.Int32(o.uses)
		wr.Int32(o.maxUses)
		wr.Int32(o.xp)
		wr.Int32(0)   // special price: the price sent is what the server asks
		wr.Float32(0) // price multiplier
		wr.Int32(0)   // demand
	}
	xp := int32(0)
	if rid, ok := p.ents.RuntimeOf(c.entity); ok {
		if e := p.ents.shown[rid]; e != nil {
			if v, ok := nbtNum(e.meta[protocol.EntityDataKeyTradeExperience]); ok {
				xp = int32(v)
			}
		}
	}
	wr.VarInt(tw.tier + 1)
	wr.VarInt(xp)
	wr.Bool(!tw.trader)
	wr.Bool(!tw.trader)
	p.send(v777.ClientboundPlayMerchantOffers, wr)
}

// pays reports whether a payment slot holds the price.
func (w *Windows) pays(have, want protocol.ItemStack) bool {
	if !filled(want) {
		return true
	}
	return filled(have) && w.p.javaID(have) == w.p.javaID(want) && have.Count >= want.Count
}

// tradeResult is the chosen offer's goods if the payment slots pay for it.
func (w *Windows) tradeResult() *craftResult {
	c := w.cur
	if c == nil || c.trade == nil {
		return nil
	}
	tw := c.trade
	if tw.sel < 0 || tw.sel >= len(tw.offers) {
		return nil
	}
	o := &tw.offers[tw.sel]
	a, b := w.auth[tradeInA].Stack, w.auth[tradeInB].Stack
	if o.disabled() || !w.pays(a, o.buyA) || !w.pays(b, o.buyB) {
		return nil
	}
	r := &craftResult{rc: &recipe{id: o.netID}, out: o.sell, grid: []slotKey{tradeInA}, needs: []int{int(o.buyA.Count)}}
	if filled(o.buyB) {
		r.grid, r.needs = append(r.grid, tradeInB), append(r.needs, int(o.buyB.Count))
	}
	if o.maxUses > 0 {
		r.limit = int(o.maxUses - o.uses)
	}
	// The server sends no new offers after a trade; the Java client counts the uses itself.
	r.taken = func(times int) { o.uses += int32(times) }
	return r
}

// selectTrade is the Java client choosing an offer: like vanilla's MerchantMenu.tryMoveItems
// (which the client runs itself), the payment slots go back into the inventory and are filled
// from it with the offer's price items, up to a stack each.
func (w *Windows) selectTrade(i int) {
	p := w.p
	c := w.cur
	if c == nil || c.trade == nil || i < 0 || i >= len(c.trade.offers) {
		return
	}
	tw := c.trade
	tw.sel = i
	o := &tw.offers[i]
	q := w.newRequest()
	pred := map[slotKey]protocol.ItemInstance{}
	get := func(k slotKey) protocol.ItemInstance {
		if inst, ok := pred[k]; ok {
			return inst
		}
		return w.auth[k]
	}
	// Java slots 3..38 of the merchant window: the inventory, then the hotbar.
	var inv []slotKey
	for s := 9; s < 36; s++ {
		inv = append(inv, slotKey{protocol.ContainerInventory, byte(s)})
	}
	for s := 0; s < 9; s++ {
		inv = append(inv, slotKey{protocol.ContainerHotBar, byte(s)})
	}
	move := func(src, dst slotKey, n int) {
		s, d := get(src), get(dst)
		q.move(n, q.info(src, s), q.info(dst, d))
		q.touch(src)
		q.touch(dst)
		if !filled(d.Stack) {
			d = s
			d.Stack.Count = 0
		}
		d.Stack.Count += uint16(n)
		d.StackNetworkID = q.id
		s.Stack.Count -= uint16(n)
		s.StackNetworkID = q.id
		if s.Stack.Count == 0 {
			s = protocol.ItemInstance{}
		}
		pred[src], pred[dst] = s, d
	}
	// Back to the inventory (moveItemStackTo(3, 39, reverse): stacks of the item first, then
	// empty slots, from the end of the hotbar).
	for _, k := range []slotKey{tradeInA, tradeInB} {
		inst := get(k)
		if !filled(inst.Stack) {
			continue
		}
		id := p.javaID(inst.Stack)
		most := int(jitem.DefaultMaxStackSize(id))
		for _, empty := range []bool{false, true} {
			for j := len(inv) - 1; j >= 0 && filled(get(k).Stack); j-- {
				d := get(inv[j]).Stack
				if filled(d) == empty || (filled(d) && (p.javaID(d) != id || !stackable(d, inst.Stack))) {
					continue
				}
				n := min(int(get(k).Stack.Count), most-int(d.Count))
				if n > 0 {
					move(k, inv[j], n)
				}
			}
		}
	}
	if !filled(get(tradeInA).Stack) && !filled(get(tradeInB).Stack) {
		fill := func(dst slotKey, want protocol.ItemStack) {
			if !filled(want) {
				return
			}
			id := p.javaID(want)
			most := int(jitem.DefaultMaxStackSize(id))
			for _, k := range inv {
				s := get(k).Stack
				if !filled(s) || p.javaID(s) != id {
					continue
				}
				cur := get(dst).Stack
				if filled(cur) && !stackable(cur, s) {
					continue
				}
				n := min(most-int(cur.Count), int(s.Count))
				if n > 0 {
					move(k, dst, n)
				}
				if int(get(dst).Stack.Count) >= most {
					break
				}
			}
		}
		fill(tradeInA, o.buyA)
		fill(tradeInB, o.buyB)
	}
	if len(q.actions) > 0 {
		w.send(q, pred)
	}
	w.updateResult()
	w.resync()
}
