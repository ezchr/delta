package anim

import "sync"

// MaxParts is the most displays one mob is drawn with. A model with more moving bones than this
// has the ones that move least merged into their parents.
const MaxParts = 24

// Part is one display of an animated model.
type Part struct {
	Bone  int     // the bone it moves with; -1: the model's root (it never moves)
	Model string  // item_model id; the part's pivot is at the model's (8, 8, 8)
	K     float64 // how much the model was shrunk to fit Java's model box (<= 1)
}

// Model is an entity type's animated model: its displays, and its rig, compiled when first
// asked for.
type Model struct {
	Parts []Part

	src  *Source
	lib  *Library
	once sync.Once
	rig  *Rig
}

// NewModel returns a model whose rig is compiled from src and lib on first use.
func NewModel(parts []Part, src *Source, lib *Library) *Model {
	return &Model{Parts: parts, src: src, lib: lib}
}

// Rig is the model's rig, compiled for the bones its parts move with.
func (m *Model) Rig() *Rig {
	m.once.Do(func() {
		need := make([]int, 0, len(m.Parts))
		for _, p := range m.Parts {
			if p.Bone >= 0 {
				need = append(need, p.Bone)
			}
		}
		if len(need) == 0 {
			// Nothing moves: compile the scripts only (scripts.scale needs them).
			src := *m.src
			src.Animate = nil
			m.rig = Compile(&src, m.lib, []int{})
			return
		}
		m.rig = Compile(m.src, m.lib, need)
	})
	return m.rig
}

// Moves reports whether the model has a part that an animation moves.
func (m *Model) Moves() bool {
	if m == nil {
		return false
	}
	for _, p := range m.Parts {
		if p.Bone >= 0 {
			return true
		}
	}
	return false
}

// FullRig compiles the model's rig for every bone (for tools and tests; not kept).
func (m *Model) FullRig() *Rig { return Compile(m.src, m.lib, nil) }
