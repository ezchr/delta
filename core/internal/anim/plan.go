package anim

import (
	"math"
	"sort"
	"strings"

	"delta/core/internal/molang"
)

// PartPlan is one display of a model: the bone it moves with (-1: the model's root, which never
// moves) and the bones drawn on it, Bone itself included when it has cubes. Rel[i] takes
// Members[i]'s cubes (bind pose, Java model space) into Bone's frame: the place the member has
// against Bone in the reference pose.
type PartPlan struct {
	Bone    int
	Members []int
	Rel     []Xform
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
	// Extras is the number of animations sampled that no situation plays (one-shots).
	Extras int
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
	// tiny is the scale below which a bone is not there (packs hide bones by scaling them to 0).
	tiny = 1e-4
	// The animations no sampled situation plays (attacks and other one-shots, played on a
	// property, a variable or by the server) are sampled too, on top of the idle pose: up to
	// maxExtraAnims of them, extraSamples poses each. Their movement counts extraWeight of a
	// situation's: an attack should be visible, but a mob walks and breathes far more often
	// than it attacks.
	maxExtraAnims = 24
	extraSamples  = 4
	extraWeight   = 0.6
)

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

// MakePlan decides the displays of a model. rig must be compiled for all bones. boxes are the
// bounds of each bone's own cubes in bind pose (Java model space; OK false: no cubes). At most
// maxParts displays are planned: the bones that move least against their parent are merged
// into it first, left and right twins together.
//
// A bone a pack hides in the reference pose by scaling it to 0 (open wings while standing)
// keeps its part if the budget allows, built as it is in the first sampled pose that shows it;
// merged into a parent it is left out, as the reference pose has it.
func MakePlan(rig *Rig, boxes []Box, maxParts int) *Plan {
	return MakePlans(rig, boxes, []int{maxParts})[0]
}

// AllParts as a budget means no budget: every bone that moves keeps its part.
const AllParts = 1 << 30

// MakePlans is MakePlan for several budgets at once, largest first: the merging is one sequence,
// so the plan for a smaller budget is the plan for a larger one merged further (each of its
// parts is a union of the larger plan's parts).
func MakePlans(rig *Rig, boxes []Box, budgets []int) []*Plan {
	nb := len(rig.Bones)
	// Sample poses.
	var poses [][]Xform
	used := map[*animation]bool{}
	for pi, in := range presets {
		st := rig.NewState(1)
		st.In = in
		st.used = used
		for i := 0; i < presetSettle; i++ {
			st.Step(settleStep)
		}
		n := presetSamples
		if pi == 0 {
			n++
		}
		for i := 0; i < n; i++ {
			if i > 0 {
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
	nBase := len(poses)
	if rig.Animated() {
		// One state, settled as the reference pose is; each one-shot is laid over it.
		st := rig.NewState(1)
		st.In = presets[0]
		for i := 0; i < presetSettle; i++ {
			st.Step(settleStep)
		}
		extras := make([]*animation, 0, len(rig.all))
		for _, an := range rig.all {
			if !used[an] && len(an.tracks) > 0 {
				extras = append(extras, an)
			}
		}
		// Those that end first: they are the ones played once.
		sort.SliceStable(extras, func(a, b int) bool { return extras[a].loop != loopLoop && extras[b].loop == loopLoop })
		if len(extras) > maxExtraAnims {
			extras = extras[:maxExtraAnims]
		}
		for _, an := range extras {
			length := an.length
			if length <= 0 {
				length = 2
			}
			// It takes the place of the idle pose on the bones it moves (packs fade the idle
			// animation out while a one-shot plays), instead of adding to it.
			override := an.override
			an.override = true
			for j := 0; j < extraSamples; j++ {
				st.over = []overlay{{an: an, t: (float64(j) + 0.5) / extraSamples * length, frozen: true}}
				st.Step(0)
				g := make([]Xform, nb+1)
				copy(g, st.glob)
				g[nb] = IdentX
				poses = append(poses, g)
			}
			an.override = override
		}
	}
	plan := &Plan{Ref: poses[0][:nb], Extras: (len(poses) - nBase) / extraSamples}
	// shown[i] is the first pose that shows bone i (-1: none): the pose its part is built in.
	shown := make([]int, nb+1)
	for i := 0; i < nb; i++ {
		shown[i] = -1
		for k := range poses {
			if s := poses[k][i].S; math.Abs(s[0]) > tiny && math.Abs(s[1]) > tiny && math.Abs(s[2]) > tiny {
				shown[i] = k
				break
			}
		}
	}

	// Groups: one per bone with cubes. parent is the group a group would merge into.
	type group struct {
		alive   bool
		members []int
		lo, hi  [3]float64 // bounds of its cubes in the root bone's frame
		parent  int        // bone index, nb for the model's root
		cost    float64
	}
	groups := make([]group, nb+1)
	count := 0
	for i := 0; i < nb; i++ {
		if i < len(boxes) && boxes[i].OK && shown[i] >= 0 {
			groups[i] = group{alive: true, members: []int{i}, lo: boxes[i].Lo, hi: boxes[i].Hi}
			count++
		}
	}
	parentOf := func(i int) int {
		for j := rig.Bones[i].Parent; j >= 0; j = rig.Bones[j].Parent {
			if groups[j].alive {
				return j
			}
		}
		return nb
	}
	rel := func(k, parent, child int) (Xform, bool) { return Rel(poses[k][parent], poses[k][child]) }
	// cost is how far a group's cubes move against its parent group over the sampled poses: the
	// longest way one of its corners goes (the size of the box around its positions), in pixels;
	// over the situations' poses, or extraWeight of that over all poses when that is more.
	cost := func(i int) float64 {
		g := &groups[i]
		var lo, hi [8][3]float64
		for c := range lo {
			lo[c] = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
			hi[c] = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
		}
		var corners [8][3]float64
		for c := range corners {
			corners[c] = corner(g.lo, g.hi, c)
		}
		worst := func() float64 {
			w := 0.0
			for c := range lo {
				d := [3]float64{hi[c][0] - lo[c][0], hi[c][1] - lo[c][1], hi[c][2] - lo[c][2]}
				if d[0] >= 0 {
					w = max(w, math.Sqrt(d[0]*d[0]+d[1]*d[1]+d[2]*d[2]))
				}
			}
			return w
		}
		base := 0.0
		for k := range poses {
			if k == nBase {
				base = worst()
			}
			rk, ok := rel(k, g.parent, i)
			if !ok {
				continue // the parent is not there in this pose
			}
			for c := 0; c < 8; c++ {
				p := rk.Apply(corners[c])
				for a := 0; a < 3; a++ {
					lo[c][a], hi[c][a] = min(lo[c][a], p[a]), max(hi[c][a], p[a])
				}
			}
		}
		if len(poses) == nBase {
			return worst()
		}
		return max(base, extraWeight*worst())
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
	paired := func(i int) bool {
		t := twin[i]
		return t >= 0 && groups[t].alive && groups[t].parent != i && groups[i].parent != t
	}
	pairCost := func(i int) (raw, weighed float64) {
		raw, weighed = groups[i].cost, weigh(i)
		if paired(i) {
			raw, weighed = max(raw, groups[twin[i]].cost), max(weighed, weigh(twin[i]))
		}
		return raw, weighed
	}
	merge := func(best int) {
		g := &groups[best]
		pg := &groups[g.parent]
		// Its cubes now belong to the parent group, where the pose the parent is built in puts
		// them; not there in that pose, they are left out.
		was := [2][3]float64{pg.lo, pg.hi}
		if r0, ok := rel(max(0, shown[g.parent]), g.parent, best); ok && r0.Uniform() > tiny {
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
		if parent < nb && was != [2][3]float64{pg.lo, pg.hi} {
			groups[parent].cost = cost(parent) // its cubes reach further now
		}
	}
	snapshot := func() *Plan {
		out := &Plan{Ref: plan.Ref, Moving: plan.Moving, Extras: plan.Extras}
		for i := 0; i <= nb; i++ {
			g := &groups[i]
			if !g.alive || len(g.members) == 0 {
				continue
			}
			pp := PartPlan{Bone: i}
			if i == nb {
				pp.Bone = -1
			}
			k := max(0, shown[i])
			members := append([]int(nil), g.members...)
			sort.Ints(members)
			for _, m := range members {
				r, ok := rel(k, i, m)
				if m == i {
					r, ok = IdentX, true // its own cubes are in its own frame whatever its scale
				}
				if ok && r.Uniform() > tiny {
					pp.Members = append(pp.Members, m)
					pp.Rel = append(pp.Rel, r)
				}
			}
			if len(pp.Members) > 0 {
				out.Parts = append(out.Parts, pp)
			}
		}
		return out
	}
	var plans []*Plan
	for len(plans) < len(budgets) {
		best, bc, braw := -1, 0.0, 0.0
		for i := 0; i < nb; i++ {
			if !groups[i].alive {
				continue
			}
			if raw, c := pairCost(i); best < 0 || c < bc {
				best, bc, braw = i, c, raw
			}
		}
		if best < 0 || (braw > mergeFree && count <= budgets[len(plans)]) {
			plans = append(plans, snapshot())
			if best < 0 {
				for len(plans) < len(budgets) {
					plans = append(plans, plans[len(plans)-1])
				}
			}
			continue
		}
		t, both := twin[best], paired(best)
		merge(best)
		if both && groups[t].alive {
			merge(t)
		}
	}
	return plans
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
