package bridge

import (
	"github.com/df-mc/dragonfly/server/block/cube"
)

// Vines: Java shows weeping vines, twisting vines, kelp and cave vines as a tip block and a body
// block, Bedrock as one (blocks/vines.go). A vine block is drawn as the body when the block next
// to it in the growth direction (below for weeping and cave vines, above for twisting vines and
// kelp) is the same vine. Like door halves, a vine's neighbours are redrawn when it changes
// (updateBlocks).

// vineBodyAt reports whether the vine idx at pos of col is a body: the same vine is next to it in
// its growth direction. Past the top or bottom of the column there is none.
func (w *World) vineBodyAt(col *column, idx uint32, pos cube.Pos) bool {
	y := pos[1] - 1
	if w.pal.VineUp(idx) {
		y = pos[1] + 1
	}
	if y < w.r[0] || y > w.r[1] {
		return false
	}
	return w.pal.SameVine(idx, col.c.Block(uint8(pos[0]&15), int16(y), uint8(pos[2]&15), 0))
}

// vineState is the Java state of the vine idx at pos of col, given the palette's state for it and
// the table of the other forms (Palette.VineAltFor).
func (w *World) vineState(col *column, idx uint32, pos cube.Pos, state uint32, alt []uint32) uint32 {
	if w.vineBodyAt(col, idx, pos) != w.pal.VineBody(idx) {
		return alt[idx]
	}
	return state
}

// vineDependsOn reports whether the block idx, dy blocks above a changed block (dy is 1 or -1),
// is a vine whose form depends on the changed block.
func (w *World) vineDependsOn(idx uint32, dy int) bool {
	return w.pal.IsVine(idx) && w.pal.VineUp(idx) == (dy < 0)
}
