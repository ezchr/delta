package anim

import (
	"math"
	"sort"
	"strings"

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
	presetSamples = 12   // poses sampled per preset
	settleStep    = 0.55 // seconds
	sampleStep    = 0.37 // 12 of them cover a 4 second loop
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
	// cost is how far a group's cubes move against its parent group over the sampled poses: the
	// longest way one of its corners goes (the size of the box around its positions), in pixels.
	cost := func(i int) float64 {
		g := &groups[i]
		if _, ok := rel(0, g.parent, i); !ok {
			return 0 // its parent is scaled away in the reference pose: nothing to see
		}
		var lo, hi [8][3]float64
		for c := range lo {
			lo[c] = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
			hi[c] = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
		}
		for k := range poses {
			rk, ok := rel(k, g.parent, i)
			if !ok {
				continue
			}
			for c := 0; c < 8; c++ {
				p := rk.Apply(corner(g.lo, g.hi, c))
				for a := 0; a < 3; a++ {
					lo[c][a], hi[c][a] = min(lo[c][a], p[a]), max(hi[c][a], p[a])
				}
			}
		}
		worst := 0.0
		for c := range lo {
			d := [3]float64{hi[c][0] - lo[c][0], hi[c][1] - lo[c][1], hi[c][2] - lo[c][2]}
			worst = max(worst, math.Sqrt(d[0]*d[0]+d[1]*d[1]+d[2]*d[2]))
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
	// A left bone and its right twin are merged together, so a mob does not end up with a knee
	// that bends on one side only.
	twin := make([]int, nb)
	byName := map[string]int{}
	for i, b := range rig.Bones {
		byName[strings.ToLower(b.Name)] = i
	}
	for i, b := range rig.Bones {
		twin[i] = -1
		if j, ok := byName[twinName(strings.ToLower(b.Name))]; ok && j != i {
			twin[i] = j
		}
	}
	// weigh ranks a group for merging: how far it moves, more for a big one (a head that nods a
	// little is missed sooner than a whisker that waves).
	weigh := func(i int) float64 {
		g := &groups[i]
		d := [3]float64{g.hi[0] - g.lo[0], g.hi[1] - g.lo[1], g.hi[2] - g.lo[2]}
		return g.cost * math.Sqrt(max(1, math.Sqrt(d[0]*d[0]+d[1]*d[1]+d[2]*d[2])))
	}
	pairCost := func(i int) (raw, weighed float64) {
		raw, weighed = groups[i].cost, weigh(i)
		if t := twin[i]; t >= 0 && groups[t].alive && groups[t].parent != i && groups[i].parent != t {
			raw, weighed = max(raw, groups[t].cost), max(weighed, weigh(t))
		}
		return raw, weighed
	}
	merge := func(best int) {
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
	for {
		best, bc, braw := -1, 0.0, 0.0
		for i := 0; i < nb; i++ {
			if !groups[i].alive {
				continue
			}
			if raw, c := pairCost(i); best < 0 || c < bc {
				best, bc, braw = i, c, raw
			}
		}
		if best < 0 || (braw > mergeFree && count <= maxParts) {
			break
		}
		t := twin[best]
		paired := t >= 0 && groups[t].alive && groups[t].parent != best && groups[best].parent != t
		merge(best)
		if paired && groups[t].alive {
			merge(t)
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

// twinName is the name of a bone's mirror twin by the usual naming (left/right, _l/_r), or the
// name itself when it has no side.
func twinName(n string) string {
	switch {
	case strings.Contains(n, "left"):
		return strings.Replace(n, "left", "right", 1)
	case strings.Contains(n, "right"):
		return strings.Replace(n, "right", "left", 1)
	case strings.HasSuffix(n, "_l"), strings.HasSuffix(n, ".l"):
		return n[:len(n)-1] + "r"
	case strings.HasSuffix(n, "_r"), strings.HasSuffix(n, ".r"):
		return n[:len(n)-1] + "l"
	case strings.HasPrefix(n, "l_"):
		return "r" + n[1:]
	case strings.HasPrefix(n, "r_"):
		return "l" + n[1:]
	}
	return n
}
