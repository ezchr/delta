package bridge

import (
	"testing"

	"javabridge/core/internal/blocks"
)

// TestDoorHalves: an opened door (open in the lower half, hinge in the upper half) shows both
// halves open, with the hinge of the upper half.
func TestDoorHalves(t *testing.T) {
	pal, err := blocks.NewPalette(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var lower, upper uint32
	found := 0
	for i, s := range pal.States {
		if s.Name != "minecraft:wooden_door" {
			continue
		}
		pr := s.Properties
		if pr["minecraft:cardinal_direction"] != "east" {
			continue
		}
		switch {
		case pr["upper_block_bit"] == uint8(0) && pr["open_bit"] == uint8(1) && pr["door_hinge_bit"] == uint8(0):
			lower, found = uint32(i), found+1
		case pr["upper_block_bit"] == uint8(1) && pr["open_bit"] == uint8(0) && pr["door_hinge_bit"] == uint8(1):
			upper, found = uint32(i), found+1
		}
	}
	if found != 2 || !pal.IsDoor(lower) || !pal.IsDoor(upper) {
		t.Fatalf("door states not found (%d)", found)
	}
	w := &World{pal: pal}
	get := func(y int) uint32 {
		switch y {
		case 64:
			return lower
		case 65:
			return upper
		}
		return pal.Air
	}
	u := w.doorIndex(upper, get, 65)
	l := w.doorIndex(lower, get, 64)
	up, lp := pal.States[u].Properties, pal.States[l].Properties
	if up["open_bit"] != uint8(1) || up["door_hinge_bit"] != uint8(1) || up["upper_block_bit"] != uint8(1) {
		t.Errorf("upper half shown as %v", up)
	}
	if lp["open_bit"] != uint8(1) || lp["door_hinge_bit"] != uint8(1) || lp["upper_block_bit"] != uint8(0) {
		t.Errorf("lower half shown as %v", lp)
	}
	if pal.Java(u, false) == pal.Java(upper, false) || pal.Java(l, false) == pal.Java(lower, false) {
		t.Error("the Java states did not change")
	}
}

// TestJavaAction: 26.3 inserted CHANGE_DESTROY_DIRECTION at 1 in player_action; a 26.3 STOP (3)
// is a finished dig, not "drop the whole stack".
func TestJavaAction(t *testing.T) {
	cases := []struct {
		proto, raw, want int32
		ok               bool
	}{
		{777, 0, digStart, true}, {777, 1, 0, false}, {777, 2, digAbort, true}, {777, 3, digFinish, true},
		{777, 4, dropStack, true}, {777, 5, dropOne, true}, {777, 6, releaseUse, true}, {777, 7, swapHands, true},
		{777, 8, 0, false},
		{776, 0, digStart, true}, {776, 1, digAbort, true}, {776, 2, digFinish, true}, {776, 3, dropStack, true},
		{776, 6, swapHands, true}, {776, 7, 0, false},
	}
	for _, c := range cases {
		got, ok := javaAction(c.proto, c.raw)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("javaAction(%d, %d) = %d, %v; want %d, %v", c.proto, c.raw, got, ok, c.want, c.ok)
		}
	}
}
