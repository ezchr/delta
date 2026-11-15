package blocks

import "testing"

func TestPaletteData(t *testing.T) {
	p, err := NewPalette(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	found, java := 0, 0
	for _, s := range p.States {
		if _, ok := lookupData(s.Hash); ok {
			found++
		}
	}
	for i := range p.States {
		if p.java[i] != 1 {
			java++
		}
	}
	t.Logf("states %d, with data %d, with a non-stone java state %d", len(p.States), found, java)
	for _, name := range []string{"minecraft:air", "minecraft:short_grass", "minecraft:grass_block", "minecraft:water", "minecraft:torch", "minecraft:oak_leaves", "minecraft:snow_layer"} {
		for i, s := range p.States {
			if s.Name == name {
				t.Logf("%s %v: filter %d emits %d solid %v full %v liquid %v java %d", name, s.Properties, p.Filter(uint32(i)), p.Emits(uint32(i)), p.Solid(uint32(i)), p.FullSolid(uint32(i)), p.Liquid(uint32(i)), p.java[i])
				break
			}
		}
	}
	if found < len(p.States)*9/10 {
		t.Fatalf("only %d of %d states have data", found, len(p.States))
	}
}
