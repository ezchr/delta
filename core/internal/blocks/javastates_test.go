package blocks

import "testing"

// TestJavaStates checks the Java state table against ids from Mojang's 26.3 blocks report.
func TestJavaStates(t *testing.T) {
	bed := JavaBlockNamed("minecraft:red_bed")
	if bed == nil || bed.First != 2254 || bed.Default != 2257 {
		t.Fatalf("red_bed: %+v", bed)
	}
	// facing=north occupied=true part=foot is the second state.
	if s, ok := bed.With(bed.First, "part", "foot"); !ok || s != 2255 {
		t.Fatalf("red_bed part=foot: %d %v", s, ok)
	}
	if v, _ := bed.Get(2255, "part"); v != "foot" {
		t.Fatalf("red_bed 2255 part: %q", v)
	}
	// The same bed in blue keeps facing/occupied/part.
	blue := JavaBlockNamed("minecraft:blue_bed")
	if s, ok := JavaSwap(2255, "minecraft:blue_bed"); !ok || s != blue.First+1 {
		t.Fatalf("blue bed: %d, want %d", s, blue.First+1)
	}
	if b := JavaBlockOf(2255); b != bed {
		t.Fatalf("JavaBlockOf(2255) = %v", b)
	}
	// Chest type left is two states after single (chests.go relies on it).
	chest := JavaBlockNamed("minecraft:chest")
	if s, _ := chest.With(chest.Default, "type", "left"); s != chest.Default+2 {
		t.Fatalf("chest left: %d, default %d", s, chest.Default)
	}
	// skeleton_skull: powered (true, false) x rotation 0..15, from 12682.
	skull := JavaBlockNamed("minecraft:skeleton_skull")
	if s, _ := skull.With(skull.Default, "rotation", "4"); s != 12682+16+4 {
		t.Fatalf("skull rotation 4: %d", s)
	}
	if JavaBlockNamed("minecraft:potted_poppy") == nil || JavaBlockOf(0).Name != "minecraft:air" {
		t.Fatal("potted_poppy / air")
	}
}
