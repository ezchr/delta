package javapack

import "testing"

func TestJavaTextureForBedrockShortNames(t *testing.T) {
	for in, want := range map[string]string{
		"gold_sword": "item/golden_sword", "wood_sword": "item/wooden_sword", "gold_axe": "item/golden_axe",
		"wood_axe": "item/wooden_axe", "fish": "item/cod", "cooked_fish": "item/cooked_cod",
		"clownfish": "item/tropical_fish", "mutton_raw": "item/mutton", "diamond_sword": "item/diamond_sword",
	} {
		if got, ok := javaTextureFor("item", "items/"+in); !ok || got != want {
			t.Errorf("%s: got %q %v, want %q", in, got, ok, want)
		}
	}
	for _, sp := range []string{"gold_spear", "wood_spear"} {
		if got, ok := javaTextureFor("item", "items/"+sp); ok {
			t.Logf("%s -> %s", sp, got)
		} else {
			t.Logf("%s: no Java texture", sp)
		}
	}
}
