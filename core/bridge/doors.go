package bridge

import (
	"github.com/df-mc/dragonfly/server/block/cube"
)

// Doors: Bedrock keeps a door's facing and open state in its lower half and the hinge in its upper
// half (the other half's copies go stale: opening a door changes the lower block only). Java draws
// each half from its own state, so the halves are combined: without this an opened door shows its
// upper half closed.

// doorIndex is the palette index the door half idx at (x, y, z) of c is shown as: its own state
// with the facing and open state of the lower half and the hinge of the upper half.
func (w *World) doorIndex(idx uint32, get func(y int) uint32, y int) uint32 {
	st := w.pal.States[idx]
	upper, _ := st.Properties["upper_block_bit"].(uint8)
	other := get(y - 1)
	if upper == 0 {
		other = get(y + 1)
	}
	if !w.pal.IsDoor(other) || w.pal.States[other].Name != st.Name {
		return idx
	}
	lower, top := st.Properties, w.pal.States[other].Properties
	if upper != 0 {
		lower, top = top, st.Properties
	}
	props := make(map[string]any, len(lower))
	for k, v := range lower {
		props[k] = v
	}
	props["door_hinge_bit"] = top["door_hinge_bit"]
	props["upper_block_bit"] = upper
	if i, ok := w.pal.Lookup(st.Name, props); ok {
		return i
	}
	return idx
}

// doorIndexAt is doorIndex for the block idx at pos of col.
func (w *World) doorIndexAt(col *column, idx uint32, pos cube.Pos) uint32 {
	x, z := uint8(pos[0]&15), uint8(pos[2]&15)
	return w.doorIndex(idx, func(y int) uint32 {
		if y < w.r[0] || y > w.r[1] {
			return w.pal.Air
		}
		return col.c.Block(x, int16(y), z, 0)
	}, pos[1])
}
