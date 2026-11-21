package bridge

import (
	"sort"
	"strings"

	jitem "github.com/ezchr/go-mcjava/item"
	jrecipe "github.com/ezchr/go-mcjava/recipe"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The Java recipe book. A Java client knows no recipes (1.21.2+): the server sends a display of
// each recipe for the book, and the client sends a display's id back when the player clicks it.
//
// The book is the server's crafting table recipes (recipes.go, from CraftingData), an add-on's
// and a plugin's among them: each as a shaped or shapeless display whose slots are the Java items
// of the Bedrock ingredients. An ingredient that is a tag is the server's items the tag holds,
// shown in turn. A custom item, as an ingredient or as the result, is its Java stand-in stack
// (paper with the pack's model and the item's name, items.go), not the paper it is made of. The
// client works out what the player can make now from the "requirements" sent with each display:
// the Java item ids each ingredient takes. It is sent again, whole, whenever the server sends its
// recipes again.
//
// A click on a recipe (place_recipe) is done the way Java does it, with the server's own slots:
// what the grid holds goes back to the inventory, then the ingredients go from the inventory to
// the grid's slots, one craft's worth, one more than the grid already held of the same recipe, or
// with shift as many as there are. It is one item stack request of plain moves (what a Bedrock
// client's touch screen sends for dragging stacks into the grid), so the server needs to know
// nothing of the book, and the result shows as for a grid filled by hand (recipes.go). When the
// items are not there the grid is emptied and the client shows the recipe as a ghost.

const (
	// bookChoices is the most items shown in turn for one ingredient (a tag's).
	bookChoices = 24
	// bookChunk is how many recipes go into one recipe_book_add packet.
	bookChunk = 256
)

var javaCraftingTable = mustJavaItem("minecraft:crafting_table")

// javaNoItem is an item no player has: the requirement of an ingredient the player does not have
// that the client cannot count itself (below).
var javaNoItem = mustJavaItem("minecraft:structure_void")

// bookState is the recipe book as the client has it.
type bookState struct {
	dirty bool     // the server sent recipes: send the book at the next tick
	shown []recipe // the recipe of each display id
	// cats is the creative category of the server's items, by network id (CreativeContent).
	cats map[int32]byte
	// choices are the server's items an ingredient that is a tag takes, by tag.
	choices map[string][]protocol.ItemStack
	// A custom item (and an item Java has none of) is paper on the client, so the client cannot
	// tell from its inventory whether the player has it: a recipe that takes one would show as
	// one to make whenever the player held any custom item. Delta counts those recipes itself,
	// on the server's items, and gives the client requirements that come out the same: paper for
	// such an ingredient when the player has the items, an item nobody has when not. own are the
	// display ids of those recipes with what was last sent; inv is the inventory they were
	// counted for.
	own map[int32]bool
	inv  uint64
	tick int
}

// book is the player's recipe book state (kept in the windows' free state, so it goes with it on
// another server).
func (w *Windows) book() *bookState {
	b, _ := w.state["recipeBook"].(*bookState)
	if b == nil {
		b = &bookState{}
		w.state["recipeBook"] = b
	}
	return b
}

func init() {
	// These run before recipes.go's and inventory.go's handlers of the same packets (file order):
	// they only note what came, the book is made at the next tick.
	OnBedrock(packet.IDCraftingData, func(p *Player, pk *packet.CraftingData) {
		b := p.win.book()
		b.dirty, b.choices = true, nil
	})
	OnBedrock(packet.IDCreativeContent, func(p *Player, pk *packet.CreativeContent) {
		b := p.win.book()
		b.cats = make(map[int32]byte, len(pk.Items))
		for _, it := range pk.Items {
			if int(it.GroupIndex) < len(pk.Groups) {
				b.cats[it.Item.NetworkID] = pk.Groups[it.GroupIndex].Category
			}
		}
		if len(b.shown) > 0 {
			b.dirty = true // the categories came after the recipes
		}
	})
	OnTick(func(p *Player) {
		if b := p.win.book(); b.dirty && p.rem != nil && !p.inWaiting && p.win.recipes != nil {
			b.dirty = false
			p.win.sendRecipeBook()
		} else if b.tick++; p.rem != nil && !p.inWaiting && b.tick%5 == 0 {
			p.win.updateOwnCounts()
		}
	})
	OnJava(v777.ServerboundPlayPlaceRecipe, func(p *Player, r *wireReader) error {
		var pr jrecipe.PlaceRecipe
		pr.Read(r)
		if r.Err != nil {
			return r.Err
		}
		if p.rem != nil && !p.inWaiting {
			p.win.placeRecipe(pr)
		}
		return nil
	})
}

// plainStack reports whether a Java stack is its item and nothing more.
func plainStack(js *jitem.Stack) bool {
	return js.Added.Len() == 0 && js.Removed.Len() == 0 && len(js.Raw) == 0
}

// ingredientChoices are the server's items an ingredient takes, as stacks of one: the item of a
// plain descriptor, the server's items a tag holds (the first bookChoices by name).
func (w *Windows) ingredientChoices(d protocol.ItemDescriptor) []protocol.ItemStack {
	t := w.p.items()
	var key string
	switch d := d.(type) {
	case *protocol.DefaultItemDescriptor:
		name, meta := descriptorItem(d)
		id, ok := t.ids[name]
		if !ok {
			return nil
		}
		if meta == 32767 || meta < 0 {
			meta = 0
		}
		return []protocol.ItemStack{{ItemType: protocol.ItemType{NetworkID: id, MetadataValue: uint32(meta)}, Count: 1}}
	case *protocol.ItemTagItemDescriptor:
		key = "t" + d.Tag
	case *protocol.MoLangItemDescriptor:
		key = "m" + d.Expression
	default:
		return nil
	}
	b := w.book()
	if c, ok := b.choices[key]; ok {
		return c
	}
	var names []string
	for _, name := range t.names {
		if descriptorMatches(d, name, 0) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > bookChoices {
		names = names[:bookChoices]
	}
	out := make([]protocol.ItemStack, 0, len(names))
	for _, name := range names {
		out = append(out, protocol.ItemStack{ItemType: protocol.ItemType{NetworkID: t.ids[name]}, Count: 1})
	}
	if b.choices == nil {
		b.choices = map[string][]protocol.ItemStack{}
	}
	b.choices[key] = out
	return out
}

// stackSlot is the display of a server stack: its Java item, or the whole Java stack when that
// says more (a count, a custom item's model and name).
func (w *Windows) stackSlot(st protocol.ItemStack) (jrecipe.Slot, int32, bool) {
	js := &jitem.Stack{}
	w.p.javaStack(st, js)
	if js.Empty() {
		return jrecipe.Slot{}, 0, false
	}
	if plainStack(js) && js.Count == 1 {
		return jrecipe.Item(js.ID), js.ID, true
	}
	return jrecipe.Stack(js), js.ID, true
}

// ingredientSlot is the display of an ingredient and the Java items that will do for it; false
// when the server has no item for it.
//
// standIn: some of the items are custom ones (or ones Java has none of), which are paper on the
// client and are not in the set: the client cannot count them (bookState.own).
func (w *Windows) ingredientSlot(d protocol.ItemDescriptor) (s jrecipe.Slot, set jrecipe.HolderSet, standIn, ok bool) {
	var slots []jrecipe.Slot
	seen := map[int32]bool{}
	for _, st := range w.ingredientChoices(d) {
		s, id, ok := w.stackSlot(st)
		if !ok || (s.Kind == jrecipe.SlotItem && seen[id]) {
			continue
		}
		slots = append(slots, s)
		if id == javaPaper && w.p.items().names[st.NetworkID] != "minecraft:paper" {
			standIn = true
			continue
		}
		if !seen[id] {
			seen[id] = true
			set.IDs = append(set.IDs, id)
		}
	}
	switch len(slots) {
	case 0:
		return jrecipe.Slot{}, set, false, false
	case 1:
		return slots[0], set, standIn, true
	}
	return jrecipe.Composite(slots...), set, standIn, true
}

// bookEntry is the recipe book entry of a recipe; false for one the book cannot show (an
// ingredient or a result the server has no item for, a shape that is none).
func (w *Windows) bookEntry(id int32, rc *recipe) (jrecipe.Entry, bool) {
	result, resultID, ok := w.stackSlot(rc.out)
	if !ok {
		return jrecipe.Entry{}, false
	}
	e := jrecipe.Entry{ID: id, Requirements: []jrecipe.HolderSet{}}
	var slots []jrecipe.Slot
	var standIns []int // the requirements with custom items
	for _, d := range rc.inputs {
		if emptyDescriptor(d) {
			if rc.shaped {
				slots = append(slots, jrecipe.Empty())
			}
			continue
		}
		s, set, standIn, ok := w.ingredientSlot(d.Descriptor)
		if !ok {
			return jrecipe.Entry{}, false
		}
		slots = append(slots, s)
		if standIn {
			standIns = append(standIns, len(e.Requirements))
		}
		e.Requirements = append(e.Requirements, set)
	}
	if len(standIns) > 0 {
		has := w.hasIngredients(rc)
		for _, i := range standIns {
			r := &e.Requirements[i]
			if has {
				r.IDs = append(r.IDs, javaPaper)
			} else if len(r.IDs) == 0 {
				r.IDs = []int32{javaNoItem}
			}
		}
		b := w.book()
		if b.own == nil {
			b.own = map[int32]bool{}
		}
		b.own[id] = has
	}
	station := jrecipe.Item(javaCraftingTable)
	switch {
	case len(e.Requirements) == 0 || len(slots) > 9:
		return jrecipe.Entry{}, false
	case rc.shaped:
		if rc.w < 1 || rc.h < 1 || rc.w > 3 || rc.h > 3 || len(slots) != rc.w*rc.h {
			return jrecipe.Entry{}, false
		}
		e.Display = jrecipe.Shaped(rc.w, rc.h, slots, result, station)
	default:
		e.Display = jrecipe.Shapeless(slots, result, station)
	}
	e.Category = bookCategory(jitem.Name(resultID), w.book().cats[rc.out.NetworkID])
	return e, true
}

// redstoneWords name the Java items of the book's redstone tab.
var redstoneWords = []string{"redstone", "repeater", "comparator", "piston", "observer", "dispenser", "dropper", "hopper",
	"lever", "_button", "pressure_plate", "_door", "trapdoor", "fence_gate", "rail", "tnt", "daylight_detector", "target",
	"note_block", "tripwire_hook", "sculk_sensor", "lightning_rod", "crafter", "copper_bulb"}

var equipmentEnds = []string{"_sword", "_pickaxe", "_axe", "_shovel", "_hoe", "_helmet", "_chestplate", "_leggings", "_boots",
	"_spear", "_horse_armor", "bow", "shield", "fishing_rod", "flint_and_steel", "shears", "mace", "trident", "brush", "spyglass"}

var buildingEnds = []string{"_planks", "_slab", "_stairs", "_wall", "_block", "_bricks", "_tiles", "_wood", "_log", "_fence",
	"_glass", "_pane", "_terracotta", "_concrete", "_wool", "_pillar", "stone", "_copper", "_sandstone", "_deepslate"}

// bookCategory is the tab of the crafting book a recipe is under: by the Java item it makes (the
// redstone tab's and the tools', as vanilla has them), else by the creative category the server
// has the item in (construction: building blocks, equipment: equipment), else misc.
func bookCategory(javaName string, creative byte) jrecipe.Category {
	n := strings.TrimPrefix(javaName, "minecraft:")
	for _, word := range redstoneWords {
		if strings.Contains(n, word) {
			return jrecipe.CraftingRedstone
		}
	}
	for _, end := range equipmentEnds {
		if strings.HasSuffix(n, end) {
			return jrecipe.CraftingEquipment
		}
	}
	switch creative {
	case protocol.CreativeCategoryConstruction:
		return jrecipe.CraftingBuildingBlocks
	case protocol.CreativeCategoryEquipment:
		return jrecipe.CraftingEquipment
	case protocol.CreativeCategoryNature, protocol.CreativeCategoryItems:
		return jrecipe.CraftingMisc
	}
	for _, end := range buildingEnds {
		if strings.HasSuffix(n, end) {
			return jrecipe.CraftingBuildingBlocks
		}
	}
	return jrecipe.CraftingMisc
}

// bookEntries are the entries of the server's crafting recipes and the recipe of each (its
// display id is its index).
func (w *Windows) bookEntries() ([]jrecipe.Entry, []recipe) {
	if w.recipes == nil {
		return nil, nil
	}
	var entries []jrecipe.Entry
	var shown []recipe
	for i := range w.recipes.crafting {
		rc := w.recipes.crafting[i]
		e, ok := w.bookEntry(int32(len(shown)), &rc)
		if !ok {
			continue
		}
		entries = append(entries, e)
		shown = append(shown, rc)
	}
	return entries, shown
}

// sendRecipeBook sends the recipe book: the server's crafting recipes, in place of what the
// client had.
func (w *Windows) sendRecipeBook() {
	p := w.p
	b := w.book()
	b.own = nil
	b.inv = w.invSum()
	entries, shown := w.bookEntries()
	b.shown = shown
	bytes := 0
	for first := true; first || len(entries) > 0; first = false {
		n := min(len(entries), bookChunk)
		wr := p.pk()
		jrecipe.WriteBookAdd(wr, entries[:n], first, w.proto())
		bytes += len(wr.B)
		p.send(v777.ClientboundPlayRecipeBookAdd, wr)
		entries = entries[n:]
	}
	p.log.Debug("sent the recipe book", "recipes", len(shown), "of", len(w.recipes.crafting), "bytes", bytes)
}

// invKeys are the player's slots a recipe's items are taken from: hotbar, inventory, and the grid
// open (the Java client counts the grid's items too).
func (w *Windows) invKeys() []slotKey {
	keys := make([]slotKey, 0, 45)
	for i := 0; i < 9; i++ {
		keys = append(keys, slotKey{protocol.ContainerHotBar, byte(i)})
	}
	for i := 9; i < 36; i++ {
		keys = append(keys, slotKey{protocol.ContainerInventory, byte(i)})
	}
	if grid, _ := w.gridKeys(); grid != nil {
		keys = append(keys, grid...)
	}
	return keys
}

// invSum is a checksum of what invKeys hold.
func (w *Windows) invSum() uint64 {
	h := uint64(14695981039346656037)
	for _, k := range w.invKeys() {
		st := w.auth[k].Stack
		if st.Count == 0 {
			st = protocol.ItemStack{}
		}
		for _, v := range []uint64{uint64(st.NetworkID), uint64(st.MetadataValue), uint64(st.Count)} {
			h = (h ^ v) * 1099511628211
		}
	}
	return h
}

// hasIngredients reports whether the player's slots hold a craft's worth of a recipe's items.
func (w *Windows) hasIngredients(rc *recipe) bool {
	type have struct {
		name string
		meta int32
		n    int
	}
	t := w.p.items()
	var pool []have
	for _, k := range w.invKeys() {
		st := w.auth[k].Stack
		if st.Count == 0 || st.NetworkID == 0 || ownData(st) {
			continue
		}
		pool = append(pool, have{t.names[st.NetworkID], int32(st.MetadataValue), int(st.Count)})
	}
	for _, d := range rc.inputs {
		if emptyDescriptor(d) {
			continue
		}
		need := max(int(d.Count), 1)
		for i := range pool {
			if need == 0 {
				break
			}
			if pool[i].n > 0 && descriptorMatches(d.Descriptor, pool[i].name, pool[i].meta) {
				n := min(need, pool[i].n)
				pool[i].n -= n
				need -= n
			}
		}
		if need > 0 {
			return false
		}
	}
	return true
}

// updateOwnCounts sends again the recipes the client cannot count whose count changed with the
// inventory (bookState.own).
func (w *Windows) updateOwnCounts() {
	b := w.book()
	if len(b.own) == 0 {
		return
	}
	sum := w.invSum()
	if sum == b.inv {
		return
	}
	b.inv = sum
	var changed []jrecipe.Entry
	for id, had := range b.own {
		if int(id) >= len(b.shown) {
			continue
		}
		rc := &b.shown[id]
		if w.hasIngredients(rc) == had {
			continue
		}
		if e, ok := w.bookEntry(id, rc); ok {
			changed = append(changed, e)
		}
	}
	for len(changed) > 0 {
		n := min(len(changed), bookChunk)
		wr := w.p.pk()
		jrecipe.WriteBookAdd(wr, changed[:n], false, w.proto())
		w.p.send(v777.ClientboundPlayRecipeBookAdd, wr)
		changed = changed[n:]
	}
}

// ownData reports whether a stack is one Java leaves out of recipes (Inventory.isUsableForCrafting):
// damaged, enchanted or renamed. Other data does not count: BDS gives custom items a tag of
// their own (an add-on's lore, for one), and the book would take none of an add-on's items.
func ownData(st protocol.ItemStack) bool {
	if n, ok := nbtNum(st.NBTData["Damage"]); ok && n > 0 {
		return true
	}
	if e, ok := st.NBTData["ench"].([]any); ok && len(e) > 0 {
		return true
	}
	if e, ok := st.NBTData["ench"].([]map[string]any); ok && len(e) > 0 {
		return true
	}
	if d, ok := st.NBTData["display"].(map[string]any); ok {
		if n, ok := d["Name"].(string); ok && n != "" {
			return true
		}
	}
	return false
}

// recipeMove is n items going from one slot to another.
type recipeMove struct {
	from, to slotKey
	n        int
}

// planRecipe plans a click on a recipe of the book for the grid keys (width slots a row): the
// moves that put the grid's items back into the inventory and then lay the recipe out for a
// number of crafts, taken from the hotbar and the inventory in their order. most: as many crafts
// as the items and the stacks allow; else times, or one when there is not enough for that many.
// crafts 0: the items are not there (or the recipe does not fit the grid) and the moves only empty
// the grid; no moves at all when the inventory cannot take what the grid holds.
func (w *Windows) planRecipe(rc *recipe, keys []slotKey, width, times int, most bool) (moves []recipeMove, crafts int) {
	p := w.p
	sim := map[slotKey]protocol.ItemInstance{}
	get := func(k slotKey) protocol.ItemInstance {
		if inst, ok := sim[k]; ok {
			return inst
		}
		return w.auth[k]
	}
	empty := func(st protocol.ItemStack) bool { return st.Count == 0 || st.NetworkID == 0 }
	var order []slotKey
	for i := 0; i < 9; i++ {
		order = append(order, slotKey{protocol.ContainerHotBar, byte(i)})
	}
	for i := 9; i < 36; i++ {
		order = append(order, slotKey{protocol.ContainerInventory, byte(i)})
	}

	// The grid goes back: onto stacks of the item first, then into empty slots.
	for _, src := range keys {
		inst := get(src)
		if empty(inst.Stack) {
			continue
		}
		maxStack := int(jitem.DefaultMaxStackSize(p.javaID(inst.Stack)))
		left := int(inst.Stack.Count)
		for _, pass := range []bool{false, true} {
			for _, k := range order {
				if left == 0 {
					break
				}
				d := get(k)
				free := empty(d.Stack)
				if free != pass || (!free && !stackable(d.Stack, inst.Stack)) {
					continue
				}
				n := min(left, maxStack-int(d.Stack.Count))
				if free {
					n, d = min(left, maxStack), inst
					d.Stack.Count = 0
				}
				if n <= 0 {
					continue
				}
				d.Stack.Count += uint16(n)
				sim[k] = d
				moves = append(moves, recipeMove{src, k, n})
				left -= n
			}
		}
		if left > 0 {
			return nil, 0 // no room: nothing is touched
		}
		sim[src] = protocol.ItemInstance{}
	}

	// The grid slot of each ingredient.
	type want struct {
		k slotKey
		d protocol.ItemDescriptorCount
	}
	var wants []want
	if rc.shaped {
		if width <= 0 || rc.w > width || rc.h > len(keys)/width || len(rc.inputs) != rc.w*rc.h {
			return moves, 0
		}
		for y := 0; y < rc.h; y++ {
			for x := 0; x < rc.w; x++ {
				if d := rc.inputs[y*rc.w+x]; !emptyDescriptor(d) {
					wants = append(wants, want{keys[y*width+x], d})
				}
			}
		}
	} else {
		for _, d := range rc.inputs {
			if emptyDescriptor(d) {
				continue
			}
			if len(wants) == len(keys) {
				return moves, 0
			}
			wants = append(wants, want{keys[len(wants)], d})
		}
	}
	if len(wants) == 0 {
		return moves, 0
	}

	// try lays the recipe out n times: each ingredient is the first kind of item, in the
	// inventory's order, that it takes and that there is enough of, from as many slots as that
	// needs. Items with data of their own (damaged, named, enchanted) are left alone, as Java does.
	try := func(n int) ([]recipeMove, bool) {
		left := map[slotKey]int{}
		have := func(k slotKey) int {
			if v, ok := left[k]; ok {
				return v
			}
			return int(get(k).Stack.Count)
		}
		var out []recipeMove
		for _, wt := range wants {
			need := max(1, int(wt.d.Count)) * n
			found := false
			for i, k := range order {
				st := get(k).Stack
				if empty(st) || have(k) == 0 || ownData(st) ||
					!descriptorMatches(wt.d.Descriptor, p.items().names[st.NetworkID], int32(st.MetadataValue)) {
					continue
				}
				total := 0
				for _, k2 := range order[i:] {
					if st2 := get(k2).Stack; !empty(st2) && stackable(st, st2) {
						total += have(k2)
					}
				}
				if total < need {
					continue
				}
				if need > int(jitem.DefaultMaxStackSize(p.javaID(st))) {
					return nil, false
				}
				for _, k2 := range order[i:] {
					if need == 0 {
						break
					}
					if st2 := get(k2).Stack; !empty(st2) && stackable(st, st2) && have(k2) > 0 {
						take := min(have(k2), need)
						left[k2] = have(k2) - take
						need -= take
						out = append(out, recipeMove{k2, wt.k, take})
					}
				}
				found = true
				break
			}
			if !found {
				if n == 1 {
					for _, k := range order {
						if st := get(k).Stack; !empty(st) && descriptorMatches(wt.d.Descriptor, p.items().names[st.NetworkID], int32(st.MetadataValue)) {
							p.log.Debug("recipe book: ingredient left out", "item", p.items().names[st.NetworkID], "count", st.Count, "have", have(k), "need", need, "nbt", st.NBTData)
						}
					}
				}
				return nil, false
			}
		}
		return out, true
	}
	var tries []int
	if most {
		for n := 64; n >= 1; n-- {
			tries = append(tries, n)
		}
	} else if times > 1 {
		tries = []int{times, 1}
	} else {
		tries = []int{1}
	}
	for _, n := range tries {
		if place, ok := try(n); ok {
			return append(moves, place...), n
		}
	}
	return moves, 0
}

// sendMoves asks the server for moves, as one item stack request, and predicts them.
func (w *Windows) sendMoves(moves []recipeMove) {
	if len(moves) == 0 {
		return
	}
	q := w.newRequest()
	pred := map[slotKey]protocol.ItemInstance{}
	get := func(k slotKey) protocol.ItemInstance {
		if inst, ok := pred[k]; ok {
			return inst
		}
		return w.auth[k]
	}
	for _, m := range moves {
		src, dst := get(m.from), get(m.to)
		q.move(m.n, q.info(m.from, src), q.info(m.to, dst))
		q.touch(m.from)
		q.touch(m.to)
		if dst.Stack.Count == 0 || dst.Stack.NetworkID == 0 {
			dst = src
			dst.Stack.Count = 0
		}
		dst.Stack.Count += uint16(m.n)
		dst.StackNetworkID = q.id
		src.Stack.Count -= uint16(m.n)
		src.StackNetworkID = q.id
		if src.Stack.Count == 0 {
			src = protocol.ItemInstance{}
		}
		pred[m.from], pred[m.to] = src, dst
	}
	w.send(q, pred)
}

// placeRecipe handles the client's click on a recipe of the book.
func (w *Windows) placeRecipe(pr jrecipe.PlaceRecipe) {
	p := w.p
	b := w.book()
	keys, width := w.gridKeys()
	if pr.Container != w.javaWindow() || keys == nil || pr.Recipe < 0 || int(pr.Recipe) >= len(b.shown) {
		p.log.Debug("place_recipe not for the open grid", "window", pr.Container, "recipe", pr.Recipe)
		return
	}
	rc := &b.shown[pr.Recipe]
	if pr.Container == 0 {
		w.openInventory()
	}
	// The same recipe again: one craft more than the grid holds.
	times := 1
	if res := w.result; res != nil && res.rc != nil && res.rc.id == rc.id && !pr.UseMax {
		held := 64
		for i, k := range res.grid {
			if res.needs[i] > 0 {
				held = min(held, int(w.auth[k].Stack.Count)/res.needs[i])
			}
		}
		times = held + 1
	}
	moves, crafts := w.planRecipe(rc, keys, width, times, pr.UseMax)
	w.sendMoves(moves)
	w.updateResult()
	w.resync()
	p.log.Debug("place_recipe", "recipe", pr.Recipe, "max", pr.UseMax, "crafts", crafts, "moves", len(moves))
	if crafts > 0 {
		return
	}
	// Not enough items: the client shows the recipe in the grid as a ghost.
	if e, ok := w.bookEntry(pr.Recipe, rc); ok {
		wr := p.pk()
		jrecipe.WritePlaceGhost(wr, pr.Container, &e.Display, w.proto())
		p.send(v777.ClientboundPlayPlaceGhostRecipe, wr)
	}
}
