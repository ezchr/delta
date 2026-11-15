package bridge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Crafting and creative items (milestone 3). The Java client shows a crafting result only when
// the server sends it, so the bridge matches the grid (the server's slots) against the server's
// own recipes (CraftingData) and shows the output. Taking it is asked of the server the way a
// Bedrock client asks: CraftRecipe with the recipe's network id and the number of crafts, the
// grid's items consumed, the output taken out of the created output slot. Creative items come
// from the server's creative content (CraftCreative).

// recipeBook is the server's crafting recipes.
type recipeBook struct {
	crafting []recipe
	multi    map[string]uint32 // multi recipe UUID -> network id
	stone    []stoneRecipe     // stonecutter (station_results.go)
	smith    []smithRecipe     // smithing table
}

// recipe is one crafting table recipe.
type recipe struct {
	id     uint32
	shaped bool
	w, h   int
	inputs []protocol.ItemDescriptorCount // shaped: w*h, row-major
	out    protocol.ItemStack
}

// craftResult is what the open grid makes.
type craftResult struct {
	rc    *recipe
	out   protocol.ItemStack
	grid  []slotKey // the grid's slots with something in them
	needs []int     // items each takes per craft
	// Stations (station_results.go): craft adds the craft action (nil: CraftRecipe of rc), single
	// makes one at a time, cost is the anvil's level cost.
	craft  func(q *stackRequest, times int)
	single bool
	cost   int
	limit  int // most crafts at once (a trade's uses left; 0: no limit)
	taken  func(times int) // called once the craft was asked for
}

func init() {
	OnBedrock(packet.IDCraftingData, func(p *Player, pk *packet.CraftingData) {
		w := p.win
		if w.recipes == nil || pk.ClearRecipes {
			w.recipes = &recipeBook{multi: map[string]uint32{}}
		}
		b := w.recipes
		for _, sr := range pk.ShapedRecipes {
			if sr.Block != "crafting_table" || len(sr.Output) == 0 {
				continue
			}
			b.crafting = append(b.crafting, recipe{id: sr.RecipeNetworkID, shaped: true, w: int(sr.Width), h: int(sr.Height), inputs: sr.Input, out: sr.Output[0]})
		}
		for _, sr := range pk.ShapelessRecipes {
			if sr.Block != "crafting_table" || len(sr.Output) == 0 {
				continue
			}
			b.crafting = append(b.crafting, recipe{id: sr.RecipeNetworkID, inputs: sr.Input, out: sr.Output[0]})
		}
		for _, mr := range pk.MultiRecipes {
			b.multi[mr.UUID.String()] = mr.RecipeNetworkID
		}
		w.addStationRecipes(pk.ShapelessRecipes, pk.SmithingTransformRecipes, pk.SmithingTrimRecipes)
		w.recipesDirty = true
		p.log.Debug("server recipes", "crafting", len(b.crafting), "multi", len(b.multi), "stonecutter", len(b.stone), "smithing", len(b.smith))
	})
	OnBedrock(packet.IDCreativeContent, func(p *Player, pk *packet.CreativeContent) {
		w := p.win
		w.creative = w.creative[:0]
		w.cindex = nil
		for _, it := range pk.Items {
			w.creative = append(w.creative, creativeEntry{id: it.CreativeItemNetworkID, stack: it.Item})
		}
	})
}

// gridKeys are the slots of the crafting grid open now (nil: none) and its width.
func (w *Windows) gridKeys() ([]slotKey, int) {
	if c := w.cur; c != nil {
		if c.typ == protocol.ContainerTypeWorkbench && c.shown {
			return c.slots[1:10], 3
		}
		return nil, 0
	}
	return inventoryLayout[1:5], 2
}

// updateResult works out what the grid makes and shows it in the result slot.
func (w *Windows) updateResult() {
	old := w.result
	w.result = nil
	if c := w.cur; c != nil && c.shown && c.typ != protocol.ContainerTypeWorkbench && (c.station() || c.typ == protocol.ContainerTypeTrade) {
		w.result = w.stationResult(c)
		if old != nil || w.result != nil {
			w.sendSlot(resultKey)
		}
		if c.typ == protocol.ContainerTypeAnvil {
			w.sendData(0, int16(w.sta.cost))
		}
		return
	}
	keys, width := w.gridKeys()
	if keys != nil && w.recipes != nil {
		w.result = w.match(keys, width)
	}
	if old == nil && w.result == nil {
		return
	}
	w.sendSlot(resultKey)
}

// cell is one grid slot for matching.
type cell struct {
	name string
	meta int32
	n    int
}

// match finds the recipe the grid's items make.
func (w *Windows) match(keys []slotKey, width int) *craftResult {
	p := w.p
	cells := make([]cell, len(keys))
	var used []slotKey
	minX, minY, maxX, maxY := width, width, -1, -1
	for i, k := range keys {
		st := w.auth[k].Stack
		if st.Count == 0 || st.NetworkID == 0 {
			continue
		}
		cells[i] = cell{name: p.items().names[st.NetworkID], meta: int32(st.MetadataValue), n: int(st.Count)}
		used = append(used, k)
		x, y := i%width, i/width
		minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
	}
	if len(used) == 0 {
		return nil
	}
	bw, bh := maxX-minX+1, maxY-minY+1
	for i := range w.recipes.crafting {
		rc := &w.recipes.crafting[i]
		var needs []int
		if rc.shaped {
			if rc.w != bw || rc.h != bh || len(rc.inputs) != rc.w*rc.h {
				continue
			}
			for _, mirror := range []bool{false, true} {
				needs = needs[:0]
				ok := true
				for y := 0; y < bh && ok; y++ {
					for x := 0; x < bw && ok; x++ {
						c := cells[(minY+y)*width+minX+x]
						rx := x
						if mirror {
							rx = bw - 1 - x
						}
						d := rc.inputs[y*rc.w+rx]
						if c.name == "" {
							ok = emptyDescriptor(d)
							continue
						}
						ok = !emptyDescriptor(d) && descriptorMatches(d.Descriptor, c.name, c.meta) && c.n >= max(1, int(d.Count))
						needs = append(needs, max(1, int(d.Count)))
					}
				}
				if ok {
					return &craftResult{rc: rc, out: rc.out, grid: used, needs: append([]int(nil), needs...)}
				}
			}
			continue
		}
		var ins []protocol.ItemDescriptorCount
		for _, d := range rc.inputs {
			if !emptyDescriptor(d) {
				ins = append(ins, d)
			}
		}
		if len(ins) != len(used) {
			continue
		}
		var filled []cell
		for _, c := range cells {
			if c.name != "" {
				filled = append(filled, c)
			}
		}
		assign := make([]int, len(filled)) // input index per filled cell
		taken := make([]bool, len(ins))
		var try func(i int) bool
		try = func(i int) bool {
			if i == len(filled) {
				return true
			}
			for j, d := range ins {
				if taken[j] || !descriptorMatches(d.Descriptor, filled[i].name, filled[i].meta) || filled[i].n < max(1, int(d.Count)) {
					continue
				}
				taken[j], assign[i] = true, j
				if try(i + 1) {
					return true
				}
				taken[j] = false
			}
			return false
		}
		if try(0) {
			needs = make([]int, len(filled))
			for i := range filled {
				needs[i] = max(1, int(ins[assign[i]].Count))
			}
			return &craftResult{rc: rc, out: rc.out, grid: used, needs: needs}
		}
	}
	return nil
}

func emptyDescriptor(d protocol.ItemDescriptorCount) bool {
	switch d.Descriptor.(type) {
	case nil, *protocol.InvalidItemDescriptor:
		return true
	}
	return d.Count == 0
}

var molangTag = regexp.MustCompile(`'([a-z0-9_:.]+)'`)

// descriptorItem is the item name and metadata of a default descriptor. Some recipes (BDS's
// stonecutter ones) name it "ns:item:meta".
func descriptorItem(d *protocol.DefaultItemDescriptor) (string, int32) {
	name, meta := d.Name, d.MetadataValue
	if i := strings.LastIndexByte(name, ':'); i > 0 && strings.Count(name, ":") == 2 {
		if v, err := strconv.Atoi(name[i+1:]); err == nil {
			name = name[:i]
			if meta == 0 {
				meta = int32(v)
			}
		}
	}
	return name, meta
}

// descriptorMatches reports whether an ingredient takes the item.
func descriptorMatches(d protocol.ItemDescriptor, name string, meta int32) bool {
	switch d := d.(type) {
	case *protocol.DefaultItemDescriptor:
		dn, dm := descriptorItem(d)
		return dn == name && (dm == 32767 || dm == -1 || dm == meta)
	case *protocol.ItemTagItemDescriptor:
		return tagHas(d.Tag, name)
	case *protocol.MoLangItemDescriptor:
		for _, m := range molangTag.FindAllStringSubmatch(d.Expression, -1) {
			if tagHas(m[1], name) {
				return true
			}
		}
	}
	return false
}

// tagHas reports whether a vanilla item tag holds an item. Servers do not send vanilla tags, so
// the common crafting tags are known by name and the rest guessed from the tag's name.
func tagHas(tag, name string) bool {
	n := strings.TrimPrefix(name, "minecraft:")
	t := strings.TrimPrefix(tag, "minecraft:")
	wood := func(n string) bool {
		for _, w := range []string{"oak", "spruce", "birch", "jungle", "acacia", "dark_oak", "mangrove", "cherry", "pale_oak", "bamboo", "crimson", "warped"} {
			if strings.HasPrefix(n, w+"_") {
				return true
			}
		}
		return false
	}
	switch t {
	case "planks":
		return strings.HasSuffix(n, "_planks")
	case "logs", "logs_that_burn":
		log := strings.HasSuffix(n, "_log") || strings.HasSuffix(n, "_wood") || strings.HasSuffix(n, "_stem") ||
			strings.HasSuffix(n, "_hyphae") || n == "bamboo_block" || n == "stripped_bamboo_block"
		if t == "logs_that_burn" && (strings.Contains(n, "crimson") || strings.Contains(n, "warped")) {
			return false
		}
		return log
	case "wool":
		return strings.HasSuffix(n, "_wool")
	case "wooden_slabs":
		return strings.HasSuffix(n, "_slab") && wood(n)
	case "wooden_stairs":
		return strings.HasSuffix(n, "_stairs") && wood(n)
	case "coals":
		return n == "coal" || n == "charcoal"
	case "stone_tool_materials", "stone_crafting_materials":
		return n == "cobblestone" || n == "cobbled_deepslate" || n == "blackstone"
	case "sand":
		return n == "sand" || n == "red_sand"
	case "soul_fire_base_blocks":
		return n == "soul_sand" || n == "soul_soil"
	case "boats", "boat":
		return strings.HasSuffix(n, "_boat") || strings.HasSuffix(n, "_raft")
	case "wool_carpets", "carpet":
		return strings.HasSuffix(n, "_carpet") && n != "moss_carpet" && n != "pale_moss_carpet"
	case "digger", "is_pickaxe":
		return strings.HasSuffix(n, "_pickaxe")
	case "crimson_stems":
		return strings.Contains(n, "crimson_stem") || strings.Contains(n, "crimson_hyphae")
	case "warped_stems":
		return strings.Contains(n, "warped_stem") || strings.Contains(n, "warped_hyphae")
	case "trim_templates":
		return strings.HasSuffix(n, "_armor_trim_smithing_template")
	case "trimmable_armors", "trimmable_armor":
		return strings.HasSuffix(n, "_helmet") || strings.HasSuffix(n, "_chestplate") || strings.HasSuffix(n, "_leggings") || strings.HasSuffix(n, "_boots")
	case "trim_materials":
		_, ok := trimMaterials[n]
		return ok
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(t, "es"), "s")
	if i := strings.LastIndexAny(stem, ":."); i >= 0 {
		stem = stem[i+1:]
	}
	return n == stem || strings.HasSuffix(n, "_"+stem) || strings.HasPrefix(n, stem+"_")
}

// clickResult takes the crafting result: one craft onto the cursor (pickup), into a hotbar slot
// (number key), dropped (Q), or as many as fit into the inventory (shift-click).
func (w *Windows) clickResult(mode, button int) {
	p := w.p
	res := w.result
	if res == nil {
		w.resync()
		return
	}
	if res.cost > 0 && p.self.mode != GameCreative && int(p.self.xpLevel) < res.cost {
		w.resync() // the anvil wants more levels than the player has
		return
	}
	outID := p.javaID(res.out)
	per := int(res.out.Count)
	maxStack := int(jitem.DefaultMaxStackSize(outID))
	cur := w.auth[cursorKey]
	type dest struct {
		k slotKey
		n int
	}
	var dests []dest
	times := 1
	drop := false
	switch mode {
	case 0:
		switch {
		case cur.Stack.Count == 0:
		case p.javaID(cur.Stack) == outID && int(cur.Stack.Count)+per <= maxStack:
		default:
			w.resync()
			return
		}
		dests = []dest{{cursorKey, per}}
	case 2:
		k := slotKey{protocol.ContainerHotBar, byte(button)}
		if button == 40 {
			k = slotKey{protocol.ContainerOffhand, 0}
		} else if button < 0 || button > 8 {
			w.resync()
			return
		}
		if w.auth[k].Stack.Count != 0 {
			w.resync()
			return
		}
		dests = []dest{{k, per}}
	case 4:
		if cur.Stack.Count != 0 {
			w.resync()
			return
		}
		dests, drop = []dest{{cursorKey, per}}, true
	case 1:
		// As many crafts as the grid allows and the inventory takes, filled as vanilla does
		// (hotbar from its end, then the inventory from its end; stacks first, then empties).
		most := 64
		for i, k := range res.grid {
			if res.needs[i] > 0 {
				most = min(most, int(w.auth[k].Stack.Count)/res.needs[i])
			}
		}
		if res.single {
			most = min(most, 1)
		}
		if res.limit > 0 {
			most = min(most, res.limit)
		}
		var order []slotKey
		for i := 8; i >= 0; i-- {
			order = append(order, slotKey{protocol.ContainerHotBar, byte(i)})
		}
		for i := 35; i >= 9; i-- {
			order = append(order, slotKey{protocol.ContainerInventory, byte(i)})
		}
		space := map[slotKey]int{}
		for _, k := range order {
			st := w.auth[k].Stack
			switch {
			case st.Count == 0:
				space[k] = maxStack
			case p.javaID(st) == outID:
				space[k] = maxStack - int(st.Count)
			}
		}
		filled := map[slotKey]int{}
		times = 0
		for times < most {
			left := per
			trial := map[slotKey]int{}
			for _, pass := range []bool{true, false} {
				for _, k := range order {
					if left == 0 {
						break
					}
					empty := w.auth[k].Stack.Count == 0 && filled[k] == 0
					if empty == pass {
						continue
					}
					free := space[k] - filled[k] - trial[k]
					if free <= 0 {
						continue
					}
					n := min(free, left)
					trial[k] += n
					left -= n
				}
			}
			if left > 0 {
				break
			}
			for k, n := range trial {
				filled[k] += n
			}
			times++
		}
		if times == 0 {
			w.resync()
			return
		}
		for _, k := range order {
			if filled[k] > 0 {
				dests = append(dests, dest{k, filled[k]})
			}
		}
	default:
		w.resync()
		return
	}
	q := w.newRequest()
	if res.craft != nil {
		res.craft(q, times)
	} else {
		q.actions = append(q.actions, &protocol.CraftRecipeStackRequestAction{RecipeNetworkID: res.rc.id, NumberOfCrafts: byte(times)})
	}
	pred := map[slotKey]protocol.ItemInstance{}
	for i, k := range res.grid {
		inst := w.auth[k]
		n := res.needs[i] * times
		if n <= 0 {
			continue
		}
		a := &protocol.ConsumeStackRequestAction{}
		a.Count, a.Source = byte(n), q.info(k, inst)
		q.actions = append(q.actions, a)
		q.touch(k)
		inst.Stack.Count -= uint16(n)
		inst.StackNetworkID = q.id
		if inst.Stack.Count == 0 {
			inst = protocol.ItemInstance{}
		}
		pred[k] = inst
	}
	for _, d := range dests {
		inst := w.auth[d.k]
		q.move(d.n, q.createdOutput(), q.info(d.k, inst))
		q.touch(d.k)
		if inst.Stack.Count == 0 {
			inst = protocol.ItemInstance{Stack: res.out}
			inst.Stack.Count = 0
		}
		inst.Stack.Count += uint16(d.n)
		inst.StackNetworkID = q.id
		pred[d.k] = inst
	}
	if drop {
		q.actions = append(q.actions, &protocol.DropStackRequestAction{Count: byte(per), Source: q.info(cursorKey, cur)})
		pred[cursorKey] = protocol.ItemInstance{}
	}
	w.send(q, pred)
	if res.taken != nil {
		res.taken(times)
	}
	w.updateResult()
	w.resync()
}

// creativeEntry is one of the server's creative items.
type creativeEntry struct {
	id    uint32
	stack protocol.ItemStack
}

// creativeKey is what tells creative items of one Java item apart (potions, enchanted books).
func creativeKey(js *jitem.Stack) string {
	k := fmt.Sprint(js.ID)
	if js.Has(jitem.CompPotionContents) {
		k += fmt.Sprintf("/p%d", js.PotionContents.Potion)
	}
	if js.Has(jitem.CompStoredEnchantments) && len(js.StoredEnchantments) > 0 {
		k += fmt.Sprintf("/e%d.%d", js.StoredEnchantments[0].ID, js.StoredEnchantments[0].Level)
	}
	return k
}

// creativeFor finds the server's creative item for a Java item (js, when given, tells potions
// and enchanted books apart).
func (w *Windows) creativeFor(id int32, js *jitem.Stack) (uint32, bool) {
	if w.cindex == nil {
		w.cindex = map[string]uint32{}
		var s jitem.Stack
		for _, c := range w.creative {
			w.p.javaStack(c.stack, &s)
			if s.Empty() {
				continue
			}
			for _, k := range []string{creativeKey(&s), fmt.Sprint(s.ID)} {
				if _, ok := w.cindex[k]; !ok {
					w.cindex[k] = c.id
				}
			}
		}
	}
	if js != nil {
		if c, ok := w.cindex[creativeKey(js)]; ok {
			return c, true
		}
	}
	c, ok := w.cindex[fmt.Sprint(id)]
	return c, ok
}

// creativeStack is the stack of a creative item.
func (w *Windows) creativeStack(id uint32) protocol.ItemStack {
	for _, c := range w.creative {
		if c.id == id {
			return c.stack
		}
	}
	return protocol.ItemStack{}
}

// creativeSlot applies a creative client's set_creative_mode_slot: the client changed a slot of
// its inventory itself (from the creative tabs, or moving its own items, whose cursor the server
// never sees: a picked up stack is put on the server's cursor until it goes somewhere).
func (w *Windows) creativeSlot(slot int, js *jitem.Stack) {
	p := w.p
	if p.self.mode != GameCreative {
		w.resync()
		return
	}
	w.openInventory()
	cur := w.auth[cursorKey]
	curID, curN := p.javaID(cur.Stack), int(cur.Stack.Count)
	q := w.newRequest()
	pred := map[slotKey]protocol.ItemInstance{}
	destroyCursor := func() {
		if curN == 0 {
			return
		}
		q.actions = append(q.actions, &protocol.DestroyStackRequestAction{Count: byte(curN), Source: q.info(cursorKey, cur)})
		q.touch(cursorKey)
		pred[cursorKey] = protocol.ItemInstance{}
		curN = 0
	}
	creative := func(k slotKey, n int) bool {
		id, ok := w.creativeFor(js.ID, js)
		if !ok {
			p.log.Debug("no creative item", "java", jitem.Name(js.ID))
			return false
		}
		q.actions = append(q.actions, &protocol.CraftCreativeStackRequestAction{CreativeItemNetworkID: id, NumberOfCrafts: 1})
		if k == (slotKey{}) {
			q.actions = append(q.actions, &protocol.DropStackRequestAction{Count: byte(n), Source: q.createdOutput()})
			return true
		}
		inst := w.auth[k]
		q.move(n, q.createdOutput(), q.info(k, inst))
		q.touch(k)
		if inst.Stack.Count == 0 || p.javaID(inst.Stack) != js.ID {
			inst = protocol.ItemInstance{Stack: w.creativeStack(id)}
			inst.Stack.Count = 0
		}
		inst.Stack.Count += uint16(n)
		inst.StackNetworkID = q.id
		pred[k] = inst
		return true
	}
	if slot == -1 { // thrown out of the creative screen
		if js.Empty() || !creative(slotKey{}, int(min(js.Count, 64))) {
			return
		}
		w.send(q, pred)
		return
	}
	if slot < 0 || slot >= len(inventoryLayout) || inventoryLayout[slot].c == cVirtual {
		return
	}
	k := inventoryLayout[slot]
	before := w.auth[k]
	bid, bn := p.javaID(before.Stack), int(before.Stack.Count)
	an := int(min(max(js.Count, 0), 64))
	if js.Empty() {
		if bn == 0 {
			return
		}
		destroyCursor()
		q.move(bn, q.info(k, before), q.info(cursorKey, cur))
		q.touch(k)
		q.touch(cursorKey)
		moved := before
		moved.StackNetworkID = q.id
		pred[k], pred[cursorKey] = protocol.ItemInstance{}, moved
		w.send(q, pred)
		return
	}
	if curN > 0 && curID == js.ID {
		switch {
		case bn == 0 && an <= curN:
			q.move(an, q.info(cursorKey, cur), q.info(k, before))
			placed, left := cur, cur
			placed.Stack.Count, placed.StackNetworkID = uint16(an), q.id
			left.Stack.Count, left.StackNetworkID = uint16(curN-an), q.id
			if curN == an {
				left = protocol.ItemInstance{}
			}
			pred[k], pred[cursorKey] = placed, left
			w.send(q, pred)
			return
		case bid == js.ID && an > bn && an-bn <= curN:
			q.move(an-bn, q.info(cursorKey, cur), q.info(k, before))
			placed, left := before, cur
			placed.Stack.Count, placed.StackNetworkID = uint16(an), q.id
			left.Stack.Count, left.StackNetworkID = uint16(curN-(an-bn)), q.id
			if curN == an-bn {
				left = protocol.ItemInstance{}
			}
			pred[k], pred[cursorKey] = placed, left
			w.send(q, pred)
			return
		case bn > 0 && bid != js.ID && an == curN:
			q.actions = append(q.actions, &protocol.SwapStackRequestAction{Source: q.info(cursorKey, cur), Destination: q.info(k, before)})
			a, b := cur, before
			a.StackNetworkID, b.StackNetworkID = q.id, q.id
			pred[k], pred[cursorKey] = a, b
			w.send(q, pred)
			return
		}
	}
	destroyCursor()
	switch {
	case bn > 0 && bid == js.ID && an < bn:
		q.actions = append(q.actions, &protocol.DestroyStackRequestAction{Count: byte(bn - an), Source: q.info(k, before)})
		after := before
		after.Stack.Count, after.StackNetworkID = uint16(an), q.id
		pred[k] = after
	case bn > 0 && bid == js.ID && an > bn:
		if !creative(k, an-bn) {
			w.resync()
			return
		}
	case bn > 0 && bid == js.ID:
		return
	default:
		if bn > 0 {
			q.actions = append(q.actions, &protocol.DestroyStackRequestAction{Count: byte(bn), Source: q.info(k, before)})
			q.touch(k)
		}
		// The creative stack goes into an emptied slot: make its instance from the creative item.
		w.auth[k] = protocol.ItemInstance{}
		ok := creative(k, an)
		w.auth[k] = before
		if !ok {
			w.resync()
			return
		}
	}
	w.send(q, pred)
}

// dropCreativeCursor destroys what a creative client left on the server's cursor (the client
// deleted it) when its inventory closes.
func (w *Windows) dropCreativeCursor() {
	cur := w.auth[cursorKey]
	if cur.Stack.Count == 0 {
		return
	}
	q := w.newRequest()
	q.actions = append(q.actions, &protocol.DestroyStackRequestAction{Count: byte(cur.Stack.Count), Source: q.info(cursorKey, cur)})
	w.send(q, map[slotKey]protocol.ItemInstance{cursorKey: {}})
}
