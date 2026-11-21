package bridge

import (
	"reflect"
	"strings"
	"testing"

	jitem "github.com/ezchr/go-mcjava/item"
	jrecipe "github.com/ezchr/go-mcjava/recipe"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The server's items of the recipe book tests: vanilla ones and an add-on's.
var rbItems = []protocol.ItemEntry{
	{Name: "minecraft:oak_planks", RuntimeID: 10}, {Name: "minecraft:birch_planks", RuntimeID: 11},
	{Name: "minecraft:stick", RuntimeID: 12}, {Name: "minecraft:cobblestone", RuntimeID: 13},
	{Name: "minecraft:wooden_pickaxe", RuntimeID: 14}, {Name: "minecraft:crafting_table", RuntimeID: 15},
	{Name: "ores:ruby", RuntimeID: 900}, {Name: "ores:ruby_block", RuntimeID: 901},
}

func rbDefault(name string) protocol.ItemDescriptorCount {
	return protocol.ItemDescriptorCount{Descriptor: &protocol.DefaultItemDescriptor{Name: name, MetadataValue: 32767}, Count: 1}
}

func rbTag(tag string) protocol.ItemDescriptorCount {
	return protocol.ItemDescriptorCount{Descriptor: &protocol.ItemTagItemDescriptor{Tag: tag}, Count: 1}
}

func rbOut(rid int32, n int) protocol.ItemStack {
	return protocol.ItemStack{ItemType: protocol.ItemType{NetworkID: rid}, Count: uint16(n)}
}

var rbNone = protocol.ItemDescriptorCount{Descriptor: &protocol.InvalidItemDescriptor{}}

// rbRecipes: a pickaxe (shaped, 3 x 3, its planks a tag), a crafting table (shaped, 2 x 2) and an
// add-on's block made of its gem and any planks (shapeless).
func rbRecipes() *recipeBook {
	planks, stick := rbTag("minecraft:planks"), rbDefault("minecraft:stick")
	return &recipeBook{multi: map[string]uint32{}, crafting: []recipe{
		{id: 101, shaped: true, w: 3, h: 3, out: rbOut(14, 1), inputs: []protocol.ItemDescriptorCount{
			planks, planks, planks,
			rbNone, stick, rbNone,
			rbNone, stick, rbNone}},
		{id: 102, shaped: true, w: 2, h: 2, out: rbOut(15, 1), inputs: []protocol.ItemDescriptorCount{planks, planks, planks, planks}},
		{id: 103, out: rbOut(901, 2), inputs: []protocol.ItemDescriptorCount{rbDefault("ores:ruby"), planks}},
		{id: 104, out: rbOut(14, 1), inputs: []protocol.ItemDescriptorCount{rbDefault("minecraft:no_such_item")}},
	}}
}

// rbPlayer is a player on a server with rbItems and rbRecipes, at an open crafting table.
func rbPlayer(t *testing.T) (*Player, func() []packet.Packet) {
	p, sent := qmPlayer(t)
	p.rem.gd.Items = rbItems
	w := p.win
	w.recipes = rbRecipes()
	c := &window{bid: 3, typ: protocol.ContainerTypeWorkbench, jid: 1, shown: true, slots: []slotKey{resultKey}}
	for i := 32; i <= 40; i++ {
		c.slots = append(c.slots, uiKey(i))
	}
	w.cur, w.open = c, 1
	return p, sent
}

func rbJava(t *testing.T, name string) int32 {
	t.Helper()
	id, ok := jitem.ByName(name)
	if !ok {
		t.Fatalf("no Java item %s", name)
	}
	return id
}

// TestRecipeBookEntries: the server's recipes as recipe book entries: a shaped one whose planks
// are a tag (the server's planks in turn), and an add-on's shapeless one whose custom items are
// their Java stand-in stacks.
func TestRecipeBookEntries(t *testing.T) {
	p, _ := rbPlayer(t)
	w := p.win
	oak, birch, stick := rbJava(t, "minecraft:oak_planks"), rbJava(t, "minecraft:birch_planks"), rbJava(t, "minecraft:stick")
	entries, shown := w.bookEntries()
	if len(entries) != 3 || len(shown) != 3 { // the recipe of an item the server has not is left out
		t.Fatalf("%d entries, %d recipes", len(entries), len(shown))
	}

	pick := entries[0]
	planks := jrecipe.Composite(jrecipe.Item(birch), jrecipe.Item(oak)) // by name
	want := jrecipe.Shaped(3, 3, []jrecipe.Slot{
		planks, planks, planks,
		jrecipe.Empty(), jrecipe.Item(stick), jrecipe.Empty(),
		jrecipe.Empty(), jrecipe.Item(stick), jrecipe.Empty(),
	}, jrecipe.Item(rbJava(t, "minecraft:wooden_pickaxe")), jrecipe.Item(javaCraftingTable))
	if !reflect.DeepEqual(pick.Display, want) {
		t.Errorf("pickaxe display:\n got  %+v\n want %+v", pick.Display, want)
	}
	set := jrecipe.HolderSet{IDs: []int32{birch, oak}}
	if pick.ID != 0 || pick.Category != jrecipe.CraftingEquipment ||
		!reflect.DeepEqual(pick.Requirements, []jrecipe.HolderSet{set, set, set, {IDs: []int32{stick}}, {IDs: []int32{stick}}}) {
		t.Errorf("pickaxe entry: id %d, category %d, requirements %+v", pick.ID, pick.Category, pick.Requirements)
	}
	if d := entries[1].Display; d.Kind != jrecipe.CraftingShaped || d.Width != 2 || d.Height != 2 || len(d.Ingredients) != 4 {
		t.Errorf("crafting table display: %+v", d)
	}

	// The add-on's recipe: its gem and its block are named paper, not paper.
	block := entries[2]
	if block.Display.Kind != jrecipe.CraftingShapeless || len(block.Display.Ingredients) != 2 || shown[2].id != 103 {
		t.Fatalf("add-on display: %+v", block.Display)
	}
	gem := block.Display.Ingredients[0]
	if gem.Kind != jrecipe.SlotItemStack || gem.Stack.ID != javaPaper || gem.Stack.Count != 1 || !gem.Stack.Has(jitem.CompItemName) ||
		!strings.Contains(gem.Stack.ItemName.Text, "Ruby") {
		t.Errorf("custom ingredient: %+v", gem.Stack)
	}
	res := block.Display.Result
	if res.Kind != jrecipe.SlotItemStack || res.Stack.ID != javaPaper || res.Stack.Count != 2 || !strings.Contains(res.Stack.ItemName.Text, "Ruby Block") {
		t.Errorf("custom result: %+v", res.Stack)
	}
	if !reflect.DeepEqual(block.Requirements, []jrecipe.HolderSet{{IDs: []int32{javaNoItem}}, set}) || block.Category != jrecipe.CraftingMisc {
		t.Errorf("add-on entry: requirements %+v, category %d", block.Requirements, block.Category)
	}

	// The packets, as both client versions read them.
	for _, v := range []*version.Version{version.Newest, version.V776} {
		pr := jitem.For(v)
		wr := &wire.Writer{}
		jrecipe.WriteBookAdd(wr, entries, true, pr)
		r := wire.NewReader(wr.B)
		back, replace := jrecipe.ReadBookAdd(r, pr)
		if r.Err != nil || r.Len() != 0 || !replace || len(back) != 3 || back[2].Display.Result.Stack.Count != 2 {
			t.Errorf("%s: the book is not read back: %v", v.Name, r.Err)
		}
	}
}

func TestBookCategory(t *testing.T) {
	for _, c := range []struct {
		name     string
		creative byte
		want     jrecipe.Category
	}{
		{"minecraft:oak_planks", protocol.CreativeCategoryConstruction, jrecipe.CraftingBuildingBlocks},
		{"minecraft:oak_door", protocol.CreativeCategoryConstruction, jrecipe.CraftingRedstone},
		{"minecraft:sticky_piston", protocol.CreativeCategoryItems, jrecipe.CraftingRedstone},
		{"minecraft:iron_sword", 0, jrecipe.CraftingEquipment},
		{"minecraft:saddle", protocol.CreativeCategoryEquipment, jrecipe.CraftingEquipment},
		{"minecraft:bread", protocol.CreativeCategoryItems, jrecipe.CraftingMisc},
		{"minecraft:stone_bricks", 0, jrecipe.CraftingBuildingBlocks},
		{"minecraft:paper", 0, jrecipe.CraftingMisc},
	} {
		if got := bookCategory(c.name, c.creative); got != c.want {
			t.Errorf("%s (creative %d): %d, want %d", c.name, c.creative, got, c.want)
		}
	}
}

func hb(i int) slotKey  { return slotKey{protocol.ContainerHotBar, byte(i)} }
func inv(i int) slotKey { return slotKey{protocol.ContainerInventory, byte(i)} }

// TestPlanRecipe: the moves of a click on the pickaxe at a crafting table: what the grid holds
// goes back, then planks and sticks go to their places, from the inventory in its order.
func TestPlanRecipe(t *testing.T) {
	p, _ := rbPlayer(t)
	w := p.win
	w.auth[hb(0)] = qmStack(10, 2, 1)     // 2 oak planks
	w.auth[hb(1)] = qmStack(12, 10, 2)    // 10 sticks
	w.auth[inv(9)] = qmStack(11, 5, 3)    // 5 birch planks
	w.auth[uiKey(36)] = qmStack(13, 1, 4) // a cobblestone in the middle of the grid
	keys, width := w.gridKeys()
	if len(keys) != 9 || width != 3 {
		t.Fatalf("grid %v %d", keys, width)
	}
	rc := &w.recipes.crafting[0]

	moves, crafts := w.planRecipe(rc, keys, width, 1, false)
	want := []recipeMove{
		{uiKey(36), hb(2), 1},                                                // the cobblestone: the first empty slot
		{hb(0), uiKey(32), 1}, {hb(0), uiKey(33), 1}, {inv(9), uiKey(34), 1}, // oak, oak, then birch
		{hb(1), uiKey(36), 1}, {hb(1), uiKey(39), 1},
	}
	if crafts != 1 || !reflect.DeepEqual(moves, want) {
		t.Errorf("one craft: %d crafts\n got  %v\n want %v", crafts, moves, want)
	}

	// With shift: as many as there are items for. Two: the planks run out (7 of them, a kind a
	// slot), the sticks would do for five.
	moves, crafts = w.planRecipe(rc, keys, width, 1, true)
	want = []recipeMove{
		{uiKey(36), hb(2), 1},
		{hb(0), uiKey(32), 2}, {inv(9), uiKey(33), 2}, {inv(9), uiKey(34), 2},
		{hb(1), uiKey(36), 2}, {hb(1), uiKey(39), 2},
	}
	if crafts != 2 || !reflect.DeepEqual(moves, want) {
		t.Errorf("max: %d crafts\n got  %v\n want %v", crafts, moves, want)
	}

	// One more than the grid holds, and one when there is not enough for that.
	if _, crafts = w.planRecipe(rc, keys, width, 2, false); crafts != 2 {
		t.Errorf("one more: %d crafts", crafts)
	}
	if _, crafts = w.planRecipe(rc, keys, width, 3, false); crafts != 1 {
		t.Errorf("more than there is: %d crafts", crafts)
	}

	// An ingredient spread over slots is gathered; a named stack is left alone.
	w.auth[hb(1)] = qmStack(12, 1, 2)
	w.auth[inv(20)] = qmStack(12, 1, 5)
	named := qmStack(12, 30, 6)
	named.Stack.NBTData = map[string]any{"display": map[string]any{"Name": "Wand"}}
	w.auth[hb(3)] = named
	moves, crafts = w.planRecipe(rc, keys, width, 1, false)
	if crafts != 1 || !reflect.DeepEqual(moves[4:], []recipeMove{{hb(1), uiKey(36), 1}, {inv(20), uiKey(39), 1}}) {
		t.Errorf("sticks from two slots: %d crafts, %v", crafts, moves)
	}

	// No sticks: only the grid is emptied. No room for what the grid holds: nothing at all.
	delete(w.auth, hb(1))
	delete(w.auth, inv(20))
	if moves, crafts = w.planRecipe(rc, keys, width, 1, false); crafts != 0 || !reflect.DeepEqual(moves, []recipeMove{{uiKey(36), hb(1), 1}}) {
		t.Errorf("nothing to craft with: %d crafts, %v", crafts, moves)
	}
	for i := 0; i < 9; i++ {
		w.auth[hb(i)] = qmStack(10, 64, int32(100+i))
	}
	for i := 9; i < 36; i++ {
		w.auth[inv(i)] = qmStack(10, 64, int32(100+i))
	}
	if moves, crafts = w.planRecipe(rc, keys, width, 1, false); crafts != 0 || moves != nil {
		t.Errorf("a full inventory: %d crafts, %v", crafts, moves)
	}

	// A 3 x 3 recipe does not go into the inventory's 2 x 2 grid; a 2 x 2 one does, in its rows.
	w.cur, w.open = nil, 0
	delete(w.auth, uiKey(36))
	keys, width = w.gridKeys()
	if _, crafts = w.planRecipe(rc, keys, width, 1, false); crafts != 0 {
		t.Errorf("3 x 3 in the 2 x 2 grid: %d crafts", crafts)
	}
	moves, crafts = w.planRecipe(&w.recipes.crafting[1], keys, width, 1, false)
	if crafts != 1 || len(moves) != 4 || moves[0].to != uiKey(28) || moves[3].to != uiKey(31) {
		t.Errorf("2 x 2: %d crafts, %v", crafts, moves)
	}
}

// TestPlaceRecipe: the click as packets: one item stack request of the moves and the slots as
// they will be; the ghost recipe when the items are not there.
func TestPlaceRecipe(t *testing.T) {
	p, sent := rbPlayer(t)
	w := p.win
	w.auth[hb(0)] = qmStack(10, 3, 1)
	w.auth[hb(1)] = qmStack(12, 2, 2)
	w.sendRecipeBook()
	if out := takeJava(p); countJava(out, v777.ClientboundPlayRecipeBookAdd) != 1 {
		t.Fatalf("%d recipe_book_add packets", countJava(out, v777.ClientboundPlayRecipeBookAdd))
	}

	w.placeRecipe(jrecipe.PlaceRecipe{Container: 1, Recipe: 0})
	expectSent(t, "pickaxe", sent(),
		"-1: place 1 28/0#1->13/32#0; place 1 28/0#-1->13/33#0; place 1 28/0#-1->13/34#0; "+
			"place 1 28/1#2->13/36#0; place 1 28/1#-1->13/39#0")
	if w.auth[hb(0)].Stack.Count != 0 || w.auth[uiKey(34)].Stack.Count != 1 || w.auth[uiKey(39)].Stack.NetworkID != 12 {
		t.Errorf("slots after the click: %s", w.describeInventory())
	}
	if w.result == nil || w.result.rc.id != 101 {
		t.Error("the grid does not make the pickaxe")
	}
	if out := takeJava(p); countJava(out, v777.ClientboundPlayPlaceGhostRecipe) != 0 || countJava(out, v777.ClientboundPlayContainerSetContent) != 1 {
		t.Errorf("after the click: %d ghost recipes, %d contents", countJava(out, v777.ClientboundPlayPlaceGhostRecipe), countJava(out, v777.ClientboundPlayContainerSetContent))
	}

	// The crafting table next: the grid's planks go back, but there are three and it takes four,
	// so the grid stays empty and the client gets the ghost.
	w.placeRecipe(jrecipe.PlaceRecipe{Container: 1, Recipe: 1})
	if got := describeSent(sent()); len(got) != 1 || strings.Contains(got[0], "->13/") {
		t.Errorf("crafting table without planks: %q", got)
	}
	if w.auth[uiKey(32)].Stack.Count != 0 || w.auth[hb(0)].Stack.Count != 3 || w.result != nil {
		t.Errorf("the grid was not emptied: %s", w.describeInventory())
	}
	out := takeJava(p)
	if countJava(out, v777.ClientboundPlayPlaceGhostRecipe) != 1 {
		t.Fatalf("%d ghost recipes", countJava(out, v777.ClientboundPlayPlaceGhostRecipe))
	}
	for _, pk := range out {
		if pk.id == v777.ClientboundPlayPlaceGhostRecipe {
			r := wire.NewReader(pk.w.B)
			container, d := jrecipe.ReadPlaceGhost(r, nil)
			if r.Err != nil || container != 1 || d.Kind != jrecipe.CraftingShaped || d.Width != 2 {
				t.Errorf("ghost recipe: window %d, %+v, %v", container, d, r.Err)
			}
		}
	}

	// A click for another window, or a recipe there is none of, does nothing.
	w.placeRecipe(jrecipe.PlaceRecipe{Container: 9, Recipe: 0})
	w.placeRecipe(jrecipe.PlaceRecipe{Container: 1, Recipe: 77})
	if got := sent(); len(got) != 0 {
		t.Errorf("stray clicks sent %q", describeSent(got))
	}
}

func takeJava(p *Player) []outPacket {
	p.java.outMu.Lock()
	defer p.java.outMu.Unlock()
	out := p.java.out
	p.java.out = nil
	return out
}

func countJava(out []outPacket, id int32) int {
	n := 0
	for _, pk := range out {
		if pk.id == id {
			n++
		}
	}
	return n
}
