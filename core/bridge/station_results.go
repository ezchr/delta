package bridge

import (
	"fmt"
	"sort"
	"strings"

	"javabridge/core/internal/javamap"

	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Results of the work stations (anvil, grindstone, stonecutter, smithing table, loom, cartography
// table). Bedrock servers send no result for these: a Bedrock client works it out from the
// inputs and asks for it with the station's craft action, so the bridge does the same. The result
// shows in the Java result slot (clickResult in recipes.go takes it, as for crafting): the craft
// action, the inputs consumed, the output taken out of the created output slot.
//
// Java clients choose things the Bedrock UI asks for elsewhere: the anvil's name (rename_item),
// the stonecutter's recipe and the loom's pattern (container_button_click with the index in the
// list the client shows, built from update_recipes and the vanilla banner pattern tags).

// stationSel is what the Java client chose in the open station.
type stationSel struct {
	name     *string // anvil: the name typed (nil: none sent)
	cost     int     // anvil: the level cost shown
	stoneSel int     // stonecutter: the recipe chosen (-1: none)
	stoneIn  int32   // the Java item it was chosen for
	loomSel  int     // loom: the pattern chosen (-1: none)
	loomFor  string  // the pattern item it was chosen with
}

func (w *Windows) resetStation() {
	w.sta = stationSel{stoneSel: -1, loomSel: -1}
}

// stoneRecipe is one of the server's stonecutter recipes, in the order the Java client got them.
type stoneRecipe struct {
	id      uint32
	in      []int32 // the Java items its input takes
	out     protocol.ItemStack
	outJava int32
}

// smithRecipe is one of the server's smithing table recipes.
type smithRecipe struct {
	id                       uint32
	trim                     bool
	template, base, addition protocol.ItemDescriptor
	out                      protocol.ItemStack
}

// anvilRecipeUUID is the multi recipe the anvil's craft names (item repair).
const anvilRecipeUUID = "00000000-0000-0000-0000-000000000001"

// addStationRecipes keeps the server's stonecutter and smithing recipes.
func (w *Windows) addStationRecipes(stone []protocol.ShapelessRecipe, transform []protocol.SmithingTransformRecipe, trim []protocol.SmithingTrimRecipe) {
	p := w.p
	b := w.recipes
	for _, sr := range stone {
		if sr.Block != "stonecutter" || len(sr.Output) == 0 || len(sr.Input) == 0 {
			continue
		}
		rc := stoneRecipe{id: sr.RecipeNetworkID, out: sr.Output[0], outJava: p.javaID(sr.Output[0])}
		switch d := sr.Input[0].Descriptor.(type) {
		case *protocol.DefaultItemDescriptor:
			name, m := descriptorItem(d)
			meta := int16(m)
			if m == 32767 || m == -1 {
				meta = 0
			}
			if id, ok := javamap.ItemID(name, meta); ok {
				rc.in = append(rc.in, id)
			}
		default:
			for _, name := range p.items().names {
				if descriptorMatches(d, name, 0) {
					if id, ok := javamap.ItemID(name, 0); ok {
						rc.in = append(rc.in, id)
					}
				}
			}
		}
		if len(rc.in) == 0 || rc.outJava == 0 {
			p.log.Debug("stonecutter recipe not shown", "input", fmt.Sprintf("%#v", sr.Input[0].Descriptor), "out", p.describeStack(rc.out))
			continue
		}
		b.stone = append(b.stone, rc)
	}
	sort.SliceStable(b.stone, func(i, j int) bool {
		if b.stone[i].in[0] != b.stone[j].in[0] {
			return b.stone[i].in[0] < b.stone[j].in[0]
		}
		return jitem.Name(b.stone[i].outJava) < jitem.Name(b.stone[j].outJava)
	})
	for _, sr := range transform {
		if sr.Block != "smithing_table" {
			continue
		}
		b.smith = append(b.smith, smithRecipe{id: sr.RecipeNetworkID, template: sr.Template.Descriptor, base: sr.Base.Descriptor, addition: sr.Addition.Descriptor, out: sr.Result})
	}
	for _, sr := range trim {
		if sr.Block != "smithing_table" {
			continue
		}
		b.smith = append(b.smith, smithRecipe{id: sr.RecipeNetworkID, trim: true, template: sr.Template.Descriptor, base: sr.Base.Descriptor, addition: sr.Addition.Descriptor})
	}
}

// stoneList is the stonecutter list the Java client shows for an input item.
func (w *Windows) stoneList(javaIn int32) []*stoneRecipe {
	if w.recipes == nil || javaIn == 0 {
		return nil
	}
	var out []*stoneRecipe
	for i := range w.recipes.stone {
		rc := &w.recipes.stone[i]
		for _, id := range rc.in {
			if id == javaIn {
				out = append(out, rc)
				break
			}
		}
	}
	return out
}

func filled(st protocol.ItemStack) bool { return st.Count > 0 && st.NetworkID != 0 }

// stationResult works out what the open station makes.
func (w *Windows) stationResult(c *window) *craftResult {
	p := w.p
	slot := func(i int) (slotKey, protocol.ItemStack) {
		k := uiKey(i)
		return k, w.auth[k].Stack
	}
	recipe := func(id uint32) *recipe { return &recipe{id: id} }
	switch c.typ {
	case protocol.ContainerTypeAnvil:
		ki, in := slot(1)
		km, mat := slot(2)
		res, cost, n := p.anvilResult(in, mat, w.sta.name)
		w.sta.cost = cost
		if !filled(res) {
			return nil
		}
		r := &craftResult{out: res, single: true, cost: cost, grid: []slotKey{ki}, needs: []int{int(in.Count)}}
		if filled(mat) {
			use := int(mat.Count)
			if n > 0 {
				use = n
			}
			r.grid, r.needs = append(r.grid, km), append(r.needs, use)
		}
		r.craft = func(q *stackRequest, _ int) {
			a := &protocol.CraftRecipeOptionalStackRequestAction{RecipeNetworkID: w.recipes.multi[anvilRecipeUUID], FilterStringIndex: -1}
			if name := customNameOf(res); name != "" {
				q.filter, q.filterCause, a.FilterStringIndex = []string{name}, protocol.FilterCauseAnvilText, 0
			}
			q.actions = append(q.actions, a)
		}
		return r
	case protocol.ContainerTypeGrindstone:
		k1, s1 := slot(16)
		k2, s2 := slot(17)
		res := p.grindResult(s1, s2)
		if !filled(res) {
			return nil
		}
		r := &craftResult{out: res, single: true}
		for _, k := range []slotKey{k1, k2} {
			if filled(w.auth[k].Stack) {
				r.grid, r.needs = append(r.grid, k), append(r.needs, 1)
			}
		}
		// BDS checks the craft against the result: its "recipe" is the stack network id of the input
		// the result is made from, its cost the result's prior-work cost.
		src := r.grid[0]
		r.craft = func(q *stackRequest, _ int) {
			q.actions = append(q.actions, &protocol.CraftGrindstoneRecipeStackRequestAction{
				RecipeNetworkID: uint32(q.info(src, w.auth[src]).StackNetworkID), NumberOfCrafts: 1, Cost: int32(repairCostOf(res))})
		}
		return r
	case protocol.ContainerTypeStonecutter:
		k, in := slot(3)
		jin := p.javaID(in)
		if jin != w.sta.stoneIn {
			w.sta.stoneIn, w.sta.stoneSel = jin, -1 // vanilla forgets the choice when the input changes
		}
		list := w.stoneList(jin)
		if w.sta.stoneSel < 0 || w.sta.stoneSel >= len(list) {
			return nil
		}
		rc := list[w.sta.stoneSel]
		return &craftResult{rc: recipe(rc.id), out: rc.out, grid: []slotKey{k}, needs: []int{1}}
	case protocol.ContainerTypeSmithingTable:
		kt, tmpl := slot(53)
		kb, base := slot(51)
		ka, add := slot(52)
		if !filled(tmpl) || !filled(base) || !filled(add) || w.recipes == nil {
			return nil
		}
		names := p.items().names
		tn, bn, an := names[tmpl.NetworkID], names[base.NetworkID], names[add.NetworkID]
		for i := range w.recipes.smith {
			sr := &w.recipes.smith[i]
			if !descriptorMatches(sr.template, tn, int32(tmpl.MetadataValue)) || !descriptorMatches(sr.base, bn, int32(base.MetadataValue)) ||
				!descriptorMatches(sr.addition, an, int32(add.MetadataValue)) {
				continue
			}
			res := base
			res.Count = 1
			if sr.trim {
				mat, ok := trimMaterials[strings.TrimPrefix(an, "minecraft:")]
				pat := trimPattern(tn)
				if !ok || pat == "" {
					continue
				}
				res = withNBT(res, "Trim", map[string]any{"Material": mat, "Pattern": pat})
			} else {
				res.NetworkID, res.MetadataValue, res.BlockRuntimeID = sr.out.NetworkID, sr.out.MetadataValue, sr.out.BlockRuntimeID
			}
			return &craftResult{rc: recipe(sr.id), out: res, single: true, grid: []slotKey{kt, kb, ka}, needs: []int{1, 1, 1}}
		}
	case protocol.ContainerTypeLoom:
		kb, banner := slot(9)
		kd, dye := slot(10)
		_, pat := slot(11)
		key := p.stackName(pat)
		if key != w.sta.loomFor {
			w.sta.loomFor, w.sta.loomSel = key, -1
		}
		list := p.loomPatterns(pat)
		if w.sta.loomSel < 0 || w.sta.loomSel >= len(list) {
			return nil
		}
		code := list[w.sta.loomSel]
		res := p.loomResult(banner, dye, code)
		if !filled(res) {
			return nil
		}
		r := &craftResult{out: res, single: true, grid: []slotKey{kb, kd}, needs: []int{1, 1}}
		r.craft = func(q *stackRequest, _ int) {
			q.actions = append(q.actions, &protocol.CraftLoomRecipeStackRequestAction{Pattern: code, TimesCrafted: 1})
		}
		return r
	case protocol.ContainerTypeCartography:
		km, m := slot(12)
		ke, e := slot(13)
		res, uuid := p.cartographyResult(m, e)
		id, ok := uint32(0), false
		if w.recipes != nil {
			id, ok = w.recipes.multi[uuid]
		}
		if !filled(res) || !ok {
			return nil
		}
		return &craftResult{rc: recipe(id), out: res, single: true, grid: []slotKey{km, ke}, needs: []int{1, 1}}
	case protocol.ContainerTypeTrade:
		return w.tradeResult()
	}
	return nil
}

// stationButton is a container_button_click in a station: the stonecutter's recipe, the loom's
// pattern.
func (w *Windows) stationButton(c *window, button int) {
	switch c.typ {
	case protocol.ContainerTypeStonecutter:
		in := w.auth[uiKey(3)].Stack
		w.sta.stoneIn, w.sta.stoneSel = w.p.javaID(in), button
	case protocol.ContainerTypeLoom:
		w.sta.loomFor, w.sta.loomSel = w.p.stackName(w.auth[uiKey(11)].Stack), button
	default:
		return
	}
	w.updateResult()
	w.p.log.Debug("station button", "type", c.typ, "button", button, "result", w.result != nil, "stone", len(w.stoneList(w.sta.stoneIn)))
}

// renameItem is the anvil's name field (rename_item), cleaned as vanilla does.
func (w *Windows) renameItem(name string) {
	c := w.cur
	if c == nil || c.typ != protocol.ContainerTypeAnvil {
		return
	}
	name = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r == '§' {
			return -1
		}
		return r
	}, name)
	if len([]rune(name)) > 50 {
		return
	}
	w.sta.name = &name
	w.updateResult()
}

// sendRecipes sends update_recipes: which items each furnace and smithing slot takes (vanilla's
// recipe property sets, what Java shift-clicks follow) and the server's stonecutter recipes (the
// list the stonecutter shows; its buttons are indices in it).
func (w *Windows) sendRecipes() {
	p := w.p
	pr := w.proto()
	wr := p.pk()
	wr.VarInt(int32(len(vanillaPropertySets)))
	for _, s := range vanillaPropertySets {
		wr.String(s.key)
		ids := make([]int32, 0, len(s.items))
		for _, n := range s.items {
			if id, ok := jitem.ByName("minecraft:" + n); ok {
				if o := pr.Item(id); o >= 0 {
					ids = append(ids, o)
				}
			}
		}
		wr.VarInt(int32(len(ids)))
		for _, id := range ids {
			wr.VarInt(id)
		}
	}
	var stone []stoneRecipe
	if w.recipes != nil {
		stone = w.recipes.stone
	}
	wr.VarInt(int32(len(stone)))
	display := v777.BuiltinID("minecraft:slot_display", "minecraft:item")
	if display < 0 {
		display = 4
	}
	for _, rc := range stone {
		wr.VarInt(int32(len(rc.in)) + 1)
		for _, id := range rc.in {
			wr.VarInt(pr.Item(id))
		}
		wr.VarInt(display)
		wr.VarInt(pr.Item(rc.outJava))
	}
	p.send(v777.ClientboundPlayUpdateRecipes, wr)
	p.log.Debug("sent update_recipes", "stonecutter", len(stone))
}
