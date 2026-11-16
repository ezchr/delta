package anim

import (
	"math"
	"sort"

	"delta/core/internal/molang"
)

// PartPlan is one display of a model: the bone it moves with (-1: the model's root, which never
// moves) and the bones drawn on it, Bone itself included when it has cubes. The bones other than
// Bone keep the place they have relative to Bone in the reference pose.
type PartPlan struct {
	Bone    int
	Members []int
}

// Plan is how a model is split into displays.
type Plan struct {
	Parts []PartPlan
	// Ref is every bone's transform in the reference pose: the entity standing idle on the
	// ground. Models are built in it, so what is not animated still looks right.
	Ref []Xform
	// Moving is the number of bones with cubes that move against their parent in any sampled
	// pose (before merging).
	Moving int
}

// presets are the situations whose poses decide which bones need a display of their own. The
// first is the reference pose.
var presets = []Inputs{
	{OnGround: true},
	{OnGround: true, GroundSpeed: 2},
	{OnGround: true, GroundSpeed: 8},
	{},
	{GroundSpeed: 6, VerticalSpeed: 1},
	{InWater: true},
	{InWater: true, GroundSpeed: 3},
	{OnGround: true, Flag: func(q molang.Query) bool { return q == molang.QIsSleeping }},
}

const (
	presetSettle  = 4    // steps before sampling, for controllers to arrive in their state
	presetSamples = 7    // poses sampled per preset
	settleStep    = 0.55 // seconds
	sampleStep    = 0.29
	// mergeFree is the movement (pixels) below which a bone is merged whatever the budget.
	mergeFree = 0.05
)

// MakePlan decides the displays of a model. rig must be compiled for all bones. boxes are the
// bounds of each bone's own cubes in bind pose (Java model space; ok false: no cubes). At most
// maxParts displays are planned: the bones that move least against their parent are merged
// into it first.
func MakePlan(rig *Rig, boxes []Box, maxParts int) *Plan {
	nb := len(rig.Bones)
	// Sample poses.
	var poses [][]Xform
	for pi, in := range presets {
		st := rig.NewState(1)
		st.In = in
		for i := 0; i < presetSettle; i++ {
			st.In.Distance += in.GroundSpeed * settleStep
			st.Step(settleStep)
		}
		n := presetSamples
		if pi == 0 {
			n++
		}
		for i := 0; i < n; i++ {
			if i > 0 {
				st.In.Distance += in.GroundSpeed * sampleStep
				st.Step(sampleStep)
			}
			g := make([]Xform, nb+1)
			copy(g, st.glob)
			g[nb] = IdentX // the model's root
			poses = append(poses, g)
		}
		if !rig.Animated() {
			break
		}
	}
	plan := &Plan{Ref: poses[0][:nb]}

	// Groups: one per bone with cubes. parent is the group a group would merge into.
	type group struct {
		alive   bool
		members []int
		lo, hi  [3]float64 // bounds of its cubes in the root bone's frame
		parent  int        // bone index, nb for the model's root
		cost    float64
	}
	groups := make([]group, nb+1)
	has := func(i int) bool { return i < len(boxes) && boxes[i].OK }
	parentOf := func(i int) int {
		for j := rig.Bones[i].Parent; j >= 0; j = rig.Bones[j].Parent {
			if groups[j].alive {
				return j
			}
		}
		return nb
	}
	count := 0
	for i := 0; i < nb; i++ {
		if has(i) {
			groups[i] = group{alive: true, members: []int{i}, lo: boxes[i].Lo, hi: boxes[i].Hi}
			count++
		}
	}
	rel := func(k, parent, child int) (Xform, bool) { return Rel(poses[k][parent], poses[k][child]) }
	cost := func(i int) float64 {
		g := &groups[i]
		r0, ok := rel(0, g.parent, i)
		if !ok {
			return 0 // its parent is scaled away in the reference pose: nothing to see
		}
		worst := 0.0
		for k := 1; k < len(poses); k++ {
			rk, ok := rel(k, g.parent, i)
			if !ok {
				continue
			}
			for c := 0; c < 8; c++ {
				p := corner(g.lo, g.hi, c)
				a, b := r0.Apply(p), rk.Apply(p)
				d := math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
				worst = max(worst, d)
			}
		}
		return worst
	}
	for i := 0; i < nb; i++ {
		if groups[i].alive {
			groups[i].parent = parentOf(i)
		}
	}
	for i := 0; i < nb; i++ {
		if groups[i].alive {
			groups[i].cost = cost(i)
			if groups[i].cost > mergeFree {
				plan.Moving++
			}
		}
	}
	for {
		best := -1
		for i := 0; i < nb; i++ {
			if groups[i].alive && (best < 0 || groups[i].cost < groups[best].cost) {
				best = i
			}
		}
		if best < 0 || (groups[best].cost > mergeFree && count <= maxParts) {
			break
		}
		g := &groups[best]
		pg := &groups[g.parent]
		// Its cubes now belong to the parent group, where the reference pose puts them.
		if r0, ok := rel(0, g.parent, best); ok {
			lo, hi := g.lo, g.hi
			if !pg.alive || len(pg.members) == 0 {
				pg.lo = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
				pg.hi = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
			}
			for c := 0; c < 8; c++ {
				p := r0.Apply(corner(lo, hi, c))
				for a := 0; a < 3; a++ {
					pg.lo[a], pg.hi[a] = min(pg.lo[a], p[a]), max(pg.hi[a], p[a])
				}
			}
			pg.members = append(pg.members, g.members...)
		}
		if !pg.alive { // the model's root gets its first cubes
			pg.alive, pg.parent = true, -1
			count++
		}
		parent := g.parent
		g.alive, g.members = false, nil
		count--
		for i := 0; i < nb; i++ {
			if groups[i].alive && groups[i].parent == best {
				groups[i].parent = parent
				groups[i].cost = cost(i)
			}
		}
		if parent < nb {
			groups[parent].cost = cost(parent)
		}
	}
	for i := 0; i <= nb; i++ {
		g := &groups[i]
		if !g.alive || len(g.members) == 0 {
			continue
		}
		bone := i
		if i == nb {
			bone = -1
		}
		sort.Ints(g.members)
		plan.Parts = append(plan.Parts, PartPlan{Bone: bone, Members: g.members})
	}
	return plan
}

// Box is the bounds of a bone's cubes.
type Box struct {
	Lo, Hi [3]float64
	OK     bool
}

func corner(lo, hi [3]float64, c int) [3]float64 {
	p := lo
	if c&1 != 0 {
		p[0] = hi[0]
	}
	if c&2 != 0 {
		p[1] = hi[1]
	}
	if c&4 != 0 {
		p[2] = hi[2]
	}
	return p
}
