package blocks

import (
	"strconv"
	"testing"

	"delta/core/internal/javamap"

	"github.com/ezchr/go-mcjava/version"
)

// TestVineForms: every vine state has its Java tip and body form, the growth direction, the
// berries of cave vines and, for a body state, the Bedrock age on its tip.
func TestVineForms(t *testing.T) {
	p, err := NewPalette(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		up        bool
		tip, body string
		berries   string // cave vines: the berries property of both forms
	}{
		"minecraft:weeping_vines":                {false, "minecraft:weeping_vines", "minecraft:weeping_vines_plant", ""},
		"minecraft:twisting_vines":               {true, "minecraft:twisting_vines", "minecraft:twisting_vines_plant", ""},
		"minecraft:kelp":                         {true, "minecraft:kelp", "minecraft:kelp_plant", ""},
		"minecraft:cave_vines":                   {false, "minecraft:cave_vines", "minecraft:cave_vines_plant", "false"},
		"minecraft:cave_vines_head_with_berries": {false, "minecraft:cave_vines", "minecraft:cave_vines_plant", "true"},
		"minecraft:cave_vines_body_with_berries": {false, "minecraft:cave_vines", "minecraft:cave_vines_plant", "true"},
	}
	seen := map[string]int{}
	var weeping, cave, caveBerries uint32
	for i, s := range p.States {
		idx := uint32(i)
		w, ok := want[s.Name]
		if !ok {
			if p.IsVine(idx) {
				t.Errorf("%s %v is a vine", s.Name, s.Properties)
			}
			continue
		}
		seen[s.Name]++
		if !p.IsVine(idx) || p.VineUp(idx) != w.up {
			t.Fatalf("%s %v: vine %v up %v", s.Name, s.Properties, p.IsVine(idx), p.VineUp(idx))
		}
		tip, body := p.java[i], p.vineAlt[i]
		if p.VineBody(idx) {
			tip, body = body, tip
		}
		tb, bb := JavaBlockOf(tip), JavaBlockOf(body)
		if tb == nil || bb == nil || tb.Name != w.tip || bb.Name != w.body {
			t.Fatalf("%s %v: tip %v body %v, want %s and %s", s.Name, s.Properties, tb, bb, w.tip, w.body)
		}
		age, _ := bedrockAge(s.Properties)
		if a, _ := tb.Get(tip, "age"); a != strconv.Itoa(age) {
			t.Errorf("%s %v: tip age %s, want %d", s.Name, s.Properties, a, age)
		}
		if w.berries != "" {
			b1, _ := tb.Get(tip, "berries")
			b2, _ := bb.Get(body, "berries")
			if b1 != w.berries || b2 != w.berries {
				t.Errorf("%s %v: berries %s/%s, want %s", s.Name, s.Properties, b1, b2, w.berries)
			}
		}
		if javamap.Waterloggable(int32(tip)) || javamap.Waterloggable(int32(body)) || p.wet[i] != p.java[i] {
			t.Errorf("%s %v: a waterloggable form", s.Name, s.Properties)
		}
		switch s.Name {
		case "minecraft:weeping_vines":
			weeping = idx
		case "minecraft:cave_vines":
			cave = idx
		case "minecraft:cave_vines_body_with_berries":
			caveBerries = idx
		}
	}
	for name := range want {
		if seen[name] == 0 {
			t.Errorf("no %s states", name)
		}
	}
	if !p.SameVine(cave, caveBerries) || p.SameVine(cave, weeping) || p.SameVine(weeping, p.Air) {
		t.Error("SameVine")
	}
	for _, v := range version.All {
		java, _ := p.JavaFor(v)
		alt := p.VineAltFor(v)
		if len(alt) != len(p.States) || alt[weeping] == java[weeping] || alt[p.Air] != java[p.Air] {
			t.Errorf("%v: VineAltFor", v)
		}
	}
}
