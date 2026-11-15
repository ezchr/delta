package bridge

import (
	"strings"

	"github.com/df-mc/dragonfly/server/block/cube"
)

// Double chests: Bedrock keeps a chest's pairing in its block entity (pairx/pairz), Java in the
// block state (type left/right), which the state table leaves at single. Without this a double
// chest shows as two single chests (it still opens as a large chest).

func isChestName(n string) bool {
	n = strings.TrimPrefix(n, "minecraft:")
	return n == "chest" || n == "trapped_chest" || strings.HasSuffix(n, "copper_chest")
}

// chestState is state (palette index idx's Java state, a single chest) with the type its pairing
// gives. Java chest states go facing x type (single, left, right) x waterlogged, so left is +2 and
// right +4 in every version's numbering; a left chest's other half is clockwise of its facing.
func (w *World) chestState(state, idx uint32, pos cube.Pos, be map[string]any) uint32 {
	if be == nil || !isChestName(w.pal.Name(idx)) {
		return state
	}
	px, okx := be["pairx"]
	pz, okz := be["pairz"]
	if !okx || !okz {
		return state
	}
	dx, dz := nbtInt(px)-pos[0], nbtInt(pz)-pos[2]
	var cw [2]int
	switch w.pal.States[idx].Properties["minecraft:cardinal_direction"] {
	case "north":
		cw = [2]int{1, 0}
	case "east":
		cw = [2]int{0, 1}
	case "south":
		cw = [2]int{-1, 0}
	case "west":
		cw = [2]int{0, -1}
	default:
		return state
	}
	switch {
	case dx == cw[0] && dz == cw[1]:
		return state + 2
	case dx == -cw[0] && dz == -cw[1]:
		return state + 4
	}
	return state
}

// javaStateAt is the Java state (client numbering) of the block at pos in col.
func (p *Player) javaStateAt(col *column, pos cube.Pos) uint32 {
	w := p.world
	java, wet := w.pal.JavaFor(p.ver())
	x, y, z := uint8(pos[0]&15), int16(pos[1]), uint8(pos[2]&15)
	main := col.c.Block(x, y, z, 0)
	if w.pal.IsDoor(main) {
		main = w.doorIndexAt(col, main, pos)
	}
	state := java[main]
	isWet := w.pal.IsWater(col.c.Block(x, y, z, 1))
	if isWet {
		state = wet[main]
	}
	if w.pal.IsVine(main) {
		state = w.vineState(col, main, pos, state, w.pal.VineAltFor(p.ver()))
	}
	if w.pal.BlockEntity(main) != "" || w.pal.BlockEntityState(main) {
		state = p.beStateFor(state, main, isWet, pos, col.be[pos])
	}
	return state
}

func init() {
	// A chest's pairing arrives (or goes) with its block entity: redraw the chest.
	blockEntityHooks = append(blockEntityHooks, func(p *Player, pos cube.Pos, data map[string]any) {
		w := p.world
		if w == nil || pos.OutOfBounds(w.r) {
			return
		}
		col := w.cols[chunkOf(pos)]
		if col == nil || !col.sent {
			return
		}
		idx := col.c.Block(uint8(pos[0]&15), int16(pos[1]), uint8(pos[2]&15), 0)
		if !isChestName(w.pal.Name(idx)) {
			return
		}
		p.sendBlockUpdateRaw(pos[0], pos[1], pos[2], p.javaStateAt(col, pos))
	})
}
