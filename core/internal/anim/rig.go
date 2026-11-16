package anim

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"delta/core/internal/molang"
)

// BoneDef is a geometry bone as the pack has it (Bedrock values).
type BoneDef struct {
	Name, Parent string
	Pivot        [3]float64
	Rotation     [3]float64
}

// Source is what a rig is compiled from: a geometry's bones and the animation parts of a client
// entity file.
type Source struct {
	Bones []BoneDef
	// Animations is the client entity's "animations": short name -> animation or controller id.
	Animations map[string]string
	// Animate is scripts.animate: names (or {name: condition}) as JSON.
	Animate []json.RawMessage
	// Initialize and PreAnimation are scripts.initialize and scripts.pre_animation.
	Initialize, PreAnimation []string
	// Scale is scripts.scale ("" for none).
	Scale string
}

// Limits of what one rig compiles (a pack is not trusted).
const (
	maxBones       = 1024
	maxAnimations  = 256
	maxControllers = 64
	maxDepth       = 4 // controllers inside controllers
)

// Bone is a rig bone.
type Bone struct {
	Name   string
	Parent int        // index, -1 for a root
	Pivot  [3]float64 // Java model space
	Rot    [3]float64 // bind rotation, Bedrock degrees
}

// Rig is one entity type's skeleton and animations, compiled. It is read-only once made: any
// number of States run on it.
type Rig struct {
	Bones []Bone
	// Notes are the things that could not be used (expressions, missing animations).
	Notes []string

	need    []bool // bones that are evaluated
	order   []int  // the evaluated bones, parents first
	syms    *molang.Symbols
	init    []*molang.Expr
	pre     []*molang.Expr
	scale   *molang.Expr
	animate []animRef
	ctrls   []*controller
	slots   int
}

// Compile makes a rig. need lists the bones whose pose will be read (nil: all); they and their
// parents are evaluated, animations of other bones are dropped.
func Compile(src *Source, lib *Library, need []int) *Rig {
	r := &Rig{syms: molang.NewSymbols()}
	c := &compiler{syms: r.syms, notes: map[string]bool{}}
	byName := map[string]int{}
	for i, b := range src.Bones {
		if i >= maxBones {
			break
		}
		r.Bones = append(r.Bones, Bone{Name: b.Name, Parent: -1, Pivot: [3]float64{-b.Pivot[0], b.Pivot[1], b.Pivot[2]}, Rot: b.Rotation})
		if _, dup := byName[strings.ToLower(b.Name)]; !dup {
			byName[strings.ToLower(b.Name)] = i
		}
	}
	for i := range r.Bones {
		if p, ok := byName[strings.ToLower(src.Bones[i].Parent)]; ok && p != i && src.Bones[i].Parent != "" {
			r.Bones[i].Parent = p
		}
	}
	// Break parent cycles, and order parents first.
	depth := make([]int, len(r.Bones))
	for i := range r.Bones {
		d, j := 0, i
		for r.Bones[j].Parent >= 0 {
			j = r.Bones[j].Parent
			d++
			if d > len(r.Bones) {
				r.Bones[i].Parent = -1
				d = 0
				break
			}
		}
		depth[i] = d
	}
	r.need = make([]bool, len(r.Bones))
	if need == nil {
		for i := range r.need {
			r.need[i] = true
		}
	}
	for _, i := range need {
		for j := i; j >= 0 && j < len(r.Bones) && !r.need[j]; j = r.Bones[j].Parent {
			r.need[j] = true
		}
	}
	for i := range r.Bones {
		if r.need[i] {
			r.order = append(r.order, i)
		}
	}
	sort.SliceStable(r.order, func(a, b int) bool { return depth[r.order[a]] < depth[r.order[b]] })

	r.init = c.statements(src.Initialize)
	r.pre = c.statements(src.PreAnimation)
	if strings.TrimSpace(src.Scale) != "" {
		r.scale = c.expr(src.Scale)
	}

	anims := map[string]*animation{}
	ctrls := map[string]*controller{}
	var ref func(name, weight string, depth int) (animRef, bool)
	var ctrl func(id string, depth int) *controller
	ref = func(name, weight string, depth int) (animRef, bool) {
		id, ok := src.Animations[name]
		if !ok {
			c.note("animation name " + name + " is not in the entity file")
			return animRef{}, false
		}
		ar := animRef{slot: r.slots}
		if strings.TrimSpace(weight) != "" {
			ar.weight = c.expr(weight)
			if ar.weight == nil {
				return animRef{}, false // played on a condition that cannot be evaluated: not played
			}
		}
		if strings.HasPrefix(id, "controller.") {
			if depth >= maxDepth {
				return animRef{}, false
			}
			ar.ctrl = ctrl(id, depth+1)
			return ar, ar.ctrl != nil
		}
		an, done := anims[id]
		if !done {
			if raw, ok := lib.get(id); ok && len(anims) < maxAnimations {
				an = c.animation(id, raw, byName, r.need)
			} else {
				c.note("animation " + id + " is not in the packs")
			}
			anims[id] = an
		}
		if an == nil {
			return animRef{}, false
		}
		ar.anim = an
		r.slots++
		return ar, true
	}
	ctrl = func(id string, depth int) *controller {
		if ct, done := ctrls[id]; done {
			return ct
		}
		ctrls[id] = nil
		raw, ok := lib.get(id)
		var rc rawController
		if !ok || len(r.ctrls) >= maxControllers || json.Unmarshal(raw, &rc) != nil || len(rc.States) == 0 {
			c.note("controller " + id + " is not in the packs")
			return nil
		}
		ct := &controller{id: id, idx: len(r.ctrls)}
		r.ctrls = append(r.ctrls, ct)
		ctrls[id] = ct
		names := make([]string, 0, len(rc.States))
		for n := range rc.States {
			names = append(names, n)
		}
		sort.Strings(names)
		index := map[string]int{}
		for i, n := range names {
			index[n] = i
		}
		if rc.Initial == "" {
			rc.Initial = "default"
		}
		ct.initial = index[rc.Initial]
		ct.states = make([]cstate, len(names))
		for i, n := range names {
			rs := rc.States[n]
			st := &ct.states[i]
			st.name = n
			for _, a := range rs.Animations {
				if an, w, ok := named(a); ok {
					if ar, ok := ref(an, w, depth); ok {
						st.anims = append(st.anims, ar)
					}
				}
			}
			for _, t := range rs.Transitions {
				to, cond, ok := named(t)
				ti, known := index[to]
				if !ok || !known {
					continue
				}
				if x := c.expr(cond); x != nil {
					st.trans = append(st.trans, transition{to: ti, cond: x})
				}
			}
			if f, _, ok := c.scalar(rs.Blend); ok && f > 0 {
				st.blend = min(f, 5)
			} else if b := trim(rs.Blend); len(b) > 0 && b[0] == '{' {
				// A blend curve: its length is all that is used.
				st.blend = min(lastKeyTime(b), 5)
			}
			st.entry, st.exit = c.statements(rs.OnEntry), c.statements(rs.OnExit)
		}
		return ct
	}
	for _, a := range src.Animate {
		if n, w, ok := named(a); ok {
			if ar, ok := ref(n, w, 0); ok {
				r.animate = append(r.animate, ar)
			}
		}
	}
	for n := range c.notes {
		r.Notes = append(r.Notes, n)
	}
	sort.Strings(r.Notes)
	return r
}

// Animated reports whether the rig plays anything at all.
func (r *Rig) Animated() bool { return r != nil && len(r.animate) > 0 }

// Inputs is what is known about an entity when its animation is stepped.
type Inputs struct {
	OnGround, InWater bool
	// GroundSpeed and VerticalSpeed are in blocks per second.
	GroundSpeed, VerticalSpeed float64
	// HeadYaw (relative to the body) and HeadPitch, degrees.
	HeadYaw, HeadPitch float64
	BodyYaw            float64
	Variant, MarkVariant, SkinID,
	Health, MaxHealth, TimeOfDay float64
	// Flag answers the yes/no queries that are entity flags (is_sleeping, is_baby...); nil: no.
	Flag func(q molang.Query) bool
	// Prop is an actor property's value (strings as molang.Intern); nil: none.
	Prop func(name string) (float64, bool)
}

// State is one entity's running animation.
type State struct {
	In  Inputs
	rig *Rig
	env *molang.Env

	started bool
	times   []float64 // per animation slot: its clock
	done    []bool    // per slot: a non-looping animation reached its end
	ctrl    []ctrlState
	acc     []boneAcc
	glob    []Xform
	dt      float64
	swing   float64 // q.modified_distance_moved
	// cur is the controller state whose transitions are being evaluated (for
	// q.all_animations_finished).
	cur *cstate
}

type ctrlState struct {
	started   bool
	cur, prev int
	blendT    float64
	blendDur  float64 // > 0 while blending from prev
}

type boneAcc struct {
	rot, pos, scale [3]float64
}

// NewState starts an entity's animation. seed varies math.random between entities.
func (r *Rig) NewState(seed uint64) *State {
	s := &State{rig: r, times: make([]float64, r.slots), done: make([]bool, r.slots), ctrl: make([]ctrlState, len(r.ctrls)),
		acc: make([]boneAcc, len(r.Bones)), glob: make([]Xform, len(r.Bones))}
	s.env = molang.NewEnv(s, seed)
	for i := range s.glob {
		s.glob[i] = IdentX
	}
	return s
}

// Query answers a Molang query from the inputs.
func (s *State) Query(q molang.Query, args []float64) float64 {
	in := &s.In
	switch q {
	case molang.QIsOnGround:
		return b2f(in.OnGround)
	case molang.QIsInWater, molang.QIsInWaterOrRain, molang.QIsInContactWithWater:
		return b2f(in.InWater)
	case molang.QIsMoving:
		return b2f(in.GroundSpeed > 0.1)
	case molang.QGroundSpeed:
		return in.GroundSpeed
	case molang.QVerticalSpeed:
		return in.VerticalSpeed
	case molang.QModifiedMoveSpeed:
		return swingSpeed(in.GroundSpeed)
	case molang.QModifiedDistanceMoved, molang.QWalkDistance:
		return s.swing
	case molang.QTargetXRotation:
		return in.HeadPitch
	case molang.QTargetYRotation:
		return in.HeadYaw
	case molang.QBodyYRotation:
		return in.BodyYaw
	case molang.QBodyXRotation:
		return 0
	case molang.QVariant:
		return in.Variant
	case molang.QMarkVariant:
		return in.MarkVariant
	case molang.QSkinID:
		return in.SkinID
	case molang.QHealth:
		return in.Health
	case molang.QMaxHealth:
		return in.MaxHealth
	case molang.QTimeOfDay:
		return in.TimeOfDay
	case molang.QIsAlive:
		if in.Flag != nil {
			return b2f(in.Flag(q))
		}
		return 1
	case molang.QProperty, molang.QHasProperty:
		if len(args) == 0 || in.Prop == nil {
			return 0
		}
		name, ok := molang.StringOf(args[0])
		if !ok {
			return 0
		}
		v, has := in.Prop(name)
		if q == molang.QHasProperty {
			return b2f(has)
		}
		return v
	case molang.QAllAnimationsFinished, molang.QAnyAnimationFinished:
		if s.cur == nil {
			return 0
		}
		all, any, n := true, false, 0
		for _, a := range s.cur.anims {
			if a.anim == nil {
				continue
			}
			n++
			all = all && s.done[a.slot]
			any = any || s.done[a.slot]
		}
		if q == molang.QAnyAnimationFinished {
			return b2f(any)
		}
		return b2f(all && n > 0)
	}
	if in.Flag != nil {
		return b2f(in.Flag(q))
	}
	return 0
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Step advances the animation by dt seconds and works out the pose.
func (s *State) Step(dt float64) {
	r := s.rig
	if dt < 0 || math.IsNaN(dt) {
		dt = 0
	}
	dt = min(dt, 2)
	s.dt = dt
	s.env.Delta = dt
	if !s.started {
		s.started = true
		for _, x := range r.init {
			x.Eval(s.env)
		}
	} else {
		s.env.LifeTime += dt
	}
	s.swing += swingSpeed(s.In.GroundSpeed) * 20 * dt
	for _, x := range r.pre {
		x.Eval(s.env)
	}
	for _, i := range r.order {
		s.acc[i] = boneAcc{scale: [3]float64{1, 1, 1}}
	}
	for i := range r.animate {
		s.play(&r.animate[i], 1)
	}
	for _, i := range r.order {
		b := &r.Bones[i]
		a := &s.acc[i]
		l := Xform{R: rot([3]float64{b.Rot[0] + a.rot[0], b.Rot[1] + a.rot[1], b.Rot[2] + a.rot[2]}), S: a.scale}
		rp := l.R.apply([3]float64{l.S[0] * b.Pivot[0], l.S[1] * b.Pivot[1], l.S[2] * b.Pivot[2]})
		l.T = [3]float64{b.Pivot[0] - a.pos[0] - rp[0], b.Pivot[1] + a.pos[1] - rp[1], b.Pivot[2] + a.pos[2] - rp[2]}
		if b.Parent >= 0 {
			l = s.glob[b.Parent].then(l)
		}
		s.glob[i] = l
	}
}

// play applies an animation or runs a controller at weight w.
func (s *State) play(a *animRef, w float64) {
	if a.weight != nil {
		w *= a.weight.Eval(s.env)
	}
	if a.ctrl != nil {
		if w != 0 {
			s.control(a.ctrl, w)
		}
		return
	}
	if w == 0 {
		// Not playing: an animation played on a condition starts over when it plays again.
		s.times[a.slot], s.done[a.slot] = 0, false
		return
	}
	s.apply(a.anim, a.slot, w)
}

// control steps a controller's state machine and plays its state.
func (s *State) control(c *controller, w float64) {
	cs := &s.ctrl[c.idx]
	if !cs.started {
		cs.started, cs.cur, cs.prev = true, c.initial, c.initial
		for _, x := range c.states[cs.cur].entry {
			x.Eval(s.env)
		}
	}
	for hops := 0; hops < 4; hops++ {
		st := &c.states[cs.cur]
		s.cur = st
		next := -1
		for _, t := range st.trans {
			if t.cond.Eval(s.env) != 0 {
				next = t.to
				break
			}
		}
		s.cur = nil
		if next < 0 || next == cs.cur {
			break
		}
		for _, x := range st.exit {
			x.Eval(s.env)
		}
		cs.prev, cs.cur = cs.cur, next
		cs.blendT, cs.blendDur = 0, st.blend
		ns := &c.states[next]
		for _, a := range ns.anims {
			if a.anim != nil {
				s.times[a.slot], s.done[a.slot] = 0, false
			}
		}
		for _, x := range ns.entry {
			x.Eval(s.env)
		}
	}
	f := 1.0
	if cs.blendDur > 0 {
		f = cs.blendT / cs.blendDur
		cs.blendT += s.dt
		if f >= 1 {
			f, cs.blendDur = 1, 0
		} else {
			old := &c.states[cs.prev]
			for i := range old.anims {
				s.play(&old.anims[i], w*(1-f))
			}
		}
	}
	st := &c.states[cs.cur]
	for i := range st.anims {
		s.play(&st.anims[i], w*f)
	}
}

// apply adds an animation's pose at its clock, then advances the clock.
func (s *State) apply(an *animation, slot int, w float64) {
	t := s.times[slot]
	s.env.AnimTime = t
	if an.weight != nil {
		w *= an.weight.Eval(s.env)
	}
	if w != 0 && !(s.done[slot] && an.loop == loopOnce) {
		for i := range an.tracks {
			tr := &an.tracks[i]
			a := &s.acc[tr.bone]
			if an.override {
				*a = boneAcc{scale: [3]float64{1, 1, 1}}
			}
			if tr.rot != nil {
				v := tr.rot.sample(t, s.env)
				a.rot[0] += w * v[0]
				a.rot[1] += w * v[1]
				a.rot[2] += w * v[2]
			}
			if tr.pos != nil {
				v := tr.pos.sample(t, s.env)
				a.pos[0] += w * v[0]
				a.pos[1] += w * v[1]
				a.pos[2] += w * v[2]
			}
			if tr.scale != nil {
				v := tr.scale.sample(t, s.env)
				for k := 0; k < 3; k++ {
					a.scale[k] *= 1 + (v[k]-1)*w
				}
			}
		}
	}
	if an.update != nil {
		t = an.update.Eval(s.env)
	} else {
		t += s.dt
	}
	if an.length > 0 && t >= an.length {
		if an.loop == loopLoop {
			t = math.Mod(t, an.length)
		} else {
			t = an.length
			s.done[slot] = true
		}
	}
	if t < 0 || math.IsNaN(t) || math.IsInf(t, 0) {
		t = 0
	}
	s.times[slot] = t
}

// Scale is the entity file's scripts.scale for the entity now (1 when there is none or it
// cannot be evaluated).
func (s *State) Scale() float64 {
	if s.rig.scale == nil {
		return 1
	}
	v := s.rig.scale.Eval(s.env)
	if v <= 0 || v > 64 {
		return 1
	}
	return v
}

// Bone is a bone's transform in the current pose (IdentX for a bone that is not evaluated, and
// for -1, the model's root).
func (s *State) Bone(i int) Xform {
	if i < 0 || i >= len(s.glob) {
		return IdentX
	}
	return s.glob[i]
}

// Pose is the display pose of a part: bone (-1: the model's root) with its model shrunk by k.
func (s *State) Pose(bone int, k float64) PartPose {
	if bone < 0 || bone >= len(s.glob) {
		return partPose(IdentX, [3]float64{}, k)
	}
	return partPose(s.glob[bone], s.rig.Bones[bone].Pivot, k)
}

// Playing names what the controllers are in, for logs: "controller=state ...".
func (s *State) Playing() string {
	var b strings.Builder
	for _, c := range s.rig.ctrls {
		cs := s.ctrl[c.idx]
		if !cs.started {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		id := c.id
		if i := strings.LastIndexByte(id, '.'); i >= 0 {
			id = id[i+1:]
		}
		b.WriteString(id + "=" + c.states[cs.cur].name)
	}
	return b.String()
}

// Local is what the animations add to bone i in the current pose, in Bedrock animation terms:
// rotation (degrees, added to the bone's own), position and scale.
func (s *State) Local(i int) (rot, pos, scale [3]float64) {
	if i < 0 || i >= len(s.acc) || !s.rig.need[i] {
		return rot, pos, [3]float64{1, 1, 1}
	}
	return s.acc[i].rot, s.acc[i].pos, s.acc[i].scale
}

// swingSpeed is q.modified_move_speed for a ground speed in blocks per second: how far the legs
// swing, the game's walk animation speed (four times the blocks moved in a tick, at most 1).
// q.modified_distance_moved grows by it every tick, so vanilla-style walks (cos(distance *
// 38.17) * speed * 80) step once per 9.4 ticks at full swing.
func swingSpeed(groundSpeed float64) float64 {
	return max(0, min(1, groundSpeed/5))
}
