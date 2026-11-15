package bridge

import (
	"testing"

	"javabridge/core/internal/blocks"
)

// TestPackSlots checks there are spare Java states for custom blocks, solid and passable, that
// no vanilla Bedrock state maps to.
func TestPackSlots(t *testing.T) {
	pal, err := blocks.NewPalette(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	slots, states := freeSlots(pal)
	used := map[uint32]bool{}
	pal.JavaStates(func(j uint32) { used[j] = true })
	per := map[string]int{}
	for i, s := range slots {
		per[s.Block]++
		if used[states[i]] {
			t.Fatalf("slot %s[%s] is used by the block mapping", s.Block, s.Key())
		}
		if jb := blocks.JavaBlockOf(states[i]); jb == nil || jb.Name != s.Block {
			t.Fatalf("slot %s[%s]: state %d is not that block", s.Block, s.Key(), states[i])
		}
	}
	t.Logf("free slots: %v", per)
	if per["minecraft:redstone_wire"] == 0 || per["minecraft:note_block"] == 0 || per["minecraft:brown_mushroom_block"] == 0 {
		t.Fatalf("missing slots: %v", per)
	}
}
