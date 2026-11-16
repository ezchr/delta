package anim

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"

	"delta/core/internal/molang"
)

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func nearV(a, b [3]float64, eps float64) bool {
	return near(a[0], b[0], eps) && near(a[1], b[1], eps) && near(a[2], b[2], eps)
}

func testChannel(t *testing.T, src string) *channel {
	t.Helper()
	c := &compiler{syms: molang.NewSymbols(), notes: map[string]bool{}}
	return c.channel(json.RawMessage(src))
}

func TestKeyframes(t *testing.T) {
	env := molang.NewEnv(nil, 1)
	// Linear, with a jump (pre and post) at 1.
	ch := testChannel(t, `{"0.0": [0, 0, 0], "1.0": {"pre": [10, 0, 0], "post": [20, 0, 0]}, "2.0": [40, 2, "1+1"]}`)
	for _, c := range []struct {
		t    float64
		want [3]float64
	}{
		{-1, [3]float64{0, 0, 0}}, {0, [3]float64{0, 0, 0}}, {0.5, [3]float64{5, 0, 0}}, {0.999999, [3]float64{10, 0, 0}},
		{1, [3]float64{20, 0, 0}}, {1.5, [3]float64{30, 1, 1}}, {2, [3]float64{40, 2, 2}}, {9, [3]float64{40, 2, 2}},
	} {
		if got := ch.sample(c.t, env); !nearV(got, c.want, 1e-4) {
			t.Errorf("linear at %v: %v, want %v", c.t, got, c.want)
		}
	}
	// A smooth channel passes through its keys and stays between sensible bounds.
	ch = testChannel(t, `{"0.0": {"post": [0, 0, 0], "lerp_mode": "catmullrom"}, "1.0": {"post": [10, 0, 0], "lerp_mode": "catmullrom"},
		"2.0": {"post": [0, 0, 0], "lerp_mode": "catmullrom"}, "3.0": {"post": [10, 0, 0], "lerp_mode": "catmullrom"}}`)
	for i, want := range []float64{0, 10, 0, 10} {
		if got := ch.sample(float64(i), env); !near(got[0], want, 1e-6) {
			t.Errorf("smooth at key %d: %v, want %v", i, got[0], want)
		}
	}
	if mid, lin := ch.sample(1.25, env)[0], 7.5; mid <= lin || mid > 10.5 {
		t.Errorf("smooth at 1.25: %v, want above the straight line (%v) near the peak", mid, lin)
	}
	// One value, an expression of the animation's time, a scalar for all axes.
	env.AnimTime = 1
	if got := testChannel(t, `["math.sin(q.anim_time*90)*3", 2, 0]`).sample(0, env); !nearV(got, [3]float64{3, 2, 0}, 1e-9) {
		t.Errorf("expression channel: %v", got)
	}
	if got := testChannel(t, `1.5`).sample(0, env); !nearV(got, [3]float64{1.5, 1.5, 1.5}, 1e-9) {
		t.Errorf("scalar channel: %v", got)
	}
	// What cannot be evaluated is no channel at all: the bone keeps still.
	for _, bad := range []string{`["q.no_such_query * 3", 0, 0]`, `["1 +* 2", 0, 0]`, `{"0.0": [1, 2]}`, `{"x": [1, 2, 3]}`, `"loop(3, {v.x = 1;})"`,
		`[]`, `{}`, `null`, `{"0.0": {"lerp_mode": "linear"}}`, `[1, 2, {"a": 1}]`} {
		if ch := testChannel(t, bad); ch != nil {
			t.Errorf("channel %s compiled", bad)
		}
	}
}

// testLib is a library with the given animations and controllers.
func testLib(anims, ctrls string) *Library {
	l := NewLibrary()
	l.AddFile("animations/a.json", []byte(`{"format_version": "1.8.0", "animations": {`+anims+`}}`))
	l.AddFile("animation_controllers/c.json", []byte(`{"format_version": "1.10.0", "animation_controllers": {`+ctrls+`}}`))
	return l
}

func raws(list ...string) []json.RawMessage {
	var out []json.RawMessage
	for _, s := range list {
		out = append(out, json.RawMessage(s))
	}
	return out
}

func TestPoseMath(t *testing.T) {
	// A body turned about its pivot with a head on it: the head's cube corner goes where the
	// two rotations and the animation's position put it.
	src := &Source{
		Bones: []BoneDef{
			{Name: "body", Pivot: [3]float64{0, 8, 0}},
			{Name: "head", Parent: "body", Pivot: [3]float64{0, 16, -2}, Rotation: [3]float64{10, 0, 0}},
		},
		Animations: map[string]string{"a": "animation.t.a"},
		Animate:    raws(`"a"`),
	}
	lib := testLib(`"animation.t.a": {"loop": true, "bones": {
		"body": {"rotation": [0, 90, 0], "position": [1, 2, 3]},
		"head": {"rotation": [20, 0, 0], "scale": 2}}}`, ``)
	rig := Compile(src, lib, nil)
	if len(rig.Notes) != 0 {
		t.Fatalf("notes: %v", rig.Notes)
	}
	st := rig.NewState(1)
	st.Step(0)
	// By hand, in Java model space: head local = scale 2 about its pivot (0, 16, -2), then
	// Rx(-30) about it; body = Ry(-90) about (0, 8, 0), then moved by (-1, 2, 3).
	p := [3]float64{1, 18, -4}
	q := [3]float64{2 * (p[0] - 0), 2 * (p[1] - 16), 2 * (p[2] + 2)}
	s, c := math.Sincos(-30 * math.Pi / 180)
	q = [3]float64{q[0], c*q[1] - s*q[2] + 16, s*q[1] + c*q[2] - 2}
	q[1] -= 8
	q = [3]float64{-q[2], q[1], q[0]} // Ry(-90): x' = -z, z' = x
	want := [3]float64{q[0] - 1, q[1] + 8 + 2, q[2] + 3}
	if got := st.Bone(1).Apply(p); !nearV(got, want, 1e-9) {
		t.Fatalf("head corner at %v, want %v", got, want)
	}
	if u := st.Bone(1).Uniform(); !near(u, 2, 1e-9) {
		t.Fatalf("head scale %v", u)
	}
}

// TestPartPose checks the display transformation against the transform it stands for: a model
// point m = 8 + k (q - pivot) shown at T + L (S * Ry180 (m - 8) / 16) is Ry180 G(q) / 16.
func TestPartPose(t *testing.T) {
	rnd := rand.New(rand.NewSource(3))
	for i := 0; i < 200; i++ {
		g := Xform{R: rot([3]float64{rnd.Float64()*360 - 180, rnd.Float64()*360 - 180, rnd.Float64()*360 - 180}),
			S: [3]float64{0.5 + rnd.Float64(), 0.5 + rnd.Float64(), 0.5 + rnd.Float64()},
			T: [3]float64{rnd.Float64()*40 - 20, rnd.Float64() * 40, rnd.Float64()*40 - 20}}
		piv := [3]float64{rnd.Float64()*20 - 10, rnd.Float64() * 30, rnd.Float64()*20 - 10}
		k := 0.2 + 0.8*rnd.Float64()
		ps := partPose(g, piv, k)
		l := QuatMat([4]float64{float64(ps.Q[0]), float64(ps.Q[1]), float64(ps.Q[2]), float64(ps.Q[3])})
		q := [3]float64{piv[0] + rnd.Float64()*10 - 5, piv[1] + rnd.Float64()*10 - 5, piv[2] + rnd.Float64()*10 - 5}
		var m, v [3]float64
		for a := 0; a < 3; a++ {
			m[a] = 8 + k*(q[a]-piv[a])
		}
		v = [3]float64{-(m[0] - 8) / 16 * float64(ps.S[0]), (m[1] - 8) / 16 * float64(ps.S[1]), -(m[2] - 8) / 16 * float64(ps.S[2])}
		v = l.apply(v)
		got := [3]float64{v[0] + float64(ps.T[0]), v[1] + float64(ps.T[1]), v[2] + float64(ps.T[2])}
		w := g.Apply(q)
		want := [3]float64{-w[0] / 16, w[1] / 16, -w[2] / 16}
		if !nearV(got, want, 1e-4) {
			t.Fatalf("case %d: display puts the point at %v, the bone at %v", i, got, want)
		}
		if n := ps.Q[0]*ps.Q[0] + ps.Q[1]*ps.Q[1] + ps.Q[2]*ps.Q[2] + ps.Q[3]*ps.Q[3]; n < 0.9999 || n > 1.0001 {
			t.Fatalf("case %d: quaternion of length %v", i, n)
		}
	}
	// Rel undoes then: inverse(b) after n, applied after b, is n.
	b := Xform{R: rot([3]float64{10, 20, 30}), S: [3]float64{2, 2, 2}, T: [3]float64{1, 2, 3}}
	n := b.then(Xform{R: rot([3]float64{-40, 5, 70}), S: [3]float64{1, 1, 1}, T: [3]float64{4, 5, 6}})
	r, ok := Rel(b, n)
	p := [3]float64{3, -2, 5}
	if !ok || !nearV(b.Apply(r.Apply(p)), n.Apply(p), 1e-9) {
		t.Fatalf("Rel: %v, want %v", b.Apply(r.Apply(p)), n.Apply(p))
	}
	if _, ok := Rel(Xform{R: Ident}, n); ok {
		t.Fatal("Rel of a transform scaled to nothing")
	}
}

const walkerCtrl = `"controller.animation.t.move": {"initial_state": "idle", "states": {
	"idle": {"animations": ["idle"], "transitions": [{"walk": "q.ground_speed > 0.5"}, {"air": "!q.is_on_ground"}], "blend_transition": 0.2},
	"walk": {"animations": [{"walk": "1 - (v.attack_w ?? 0)"}], "transitions": [{"idle": "q.ground_speed <= 0.5"}], "blend_transition": 0.2,
		"on_entry": ["v.walks = (v.walks ?? 0) + 1;"]},
	"air": {"animations": ["fall"], "transitions": [{"idle": "q.is_on_ground && q.all_animations_finished"}]}}}`

const walkerAnims = `"animation.t.idle": {"loop": true, "bones": {"head": {"rotation": ["math.sin(q.anim_time*90)*10", 0, 0]}}},
	"animation.t.walk": {"loop": true, "animation_length": 1, "bones": {
		"leg_left": {"rotation": {"0.0": [30, 0, 0], "0.5": [-30, 0, 0], "1.0": [30, 0, 0]}},
		"leg_right": {"rotation": {"0.0": [-30, 0, 0], "0.5": [30, 0, 0], "1.0": [-30, 0, 0]}}}},
	"animation.t.fall": {"animation_length": 0.5, "bones": {"body": {"rotation": {"0.0": [0, 0, 0], "0.5": [0, 0, 45]}}}},
	"animation.t.wave": {"loop": "hold_on_last_frame", "animation_length": 0.4, "bones": {"arm": {"rotation": {"0.0": [0, 0, 0], "0.4": [0, 0, 90]}}}},
	"animation.t.broken": {"loop": true, "bones": {"head": {"rotation": ["q.is_item_name_any('x') * 90", 0, 0], "position": [0, 1, 0]},
		"nobody": {"rotation": [1, 2, 3]}}}`

func walker() *Source {
	return &Source{
		Bones: []BoneDef{
			{Name: "root"},
			{Name: "body", Parent: "root", Pivot: [3]float64{0, 8, 0}},
			{Name: "head", Parent: "body", Pivot: [3]float64{0, 16, 0}},
			{Name: "hat", Parent: "head", Pivot: [3]float64{0, 20, 0}},
			{Name: "arm", Parent: "body", Pivot: [3]float64{4, 14, 0}},
			{Name: "leg_left", Parent: "body", Pivot: [3]float64{2, 8, 0}},
			{Name: "leg_right", Parent: "body", Pivot: [3]float64{-2, 8, 0}},
		},
		Animations: map[string]string{"move": "controller.animation.t.move", "idle": "animation.t.idle", "walk": "animation.t.walk",
			"fall": "animation.t.fall", "wave": "animation.t.wave", "broken": "animation.t.broken", "vanilla": "animation.humanoid.look_at"},
		Animate:      raws(`"move"`, `{"wave": "q.property('t:waving')"}`, `"broken"`, `"vanilla"`, `{"idle": "q.no_such_thing"}`),
		Initialize:   []string{"v.seed = math.random(0, 1);"},
		PreAnimation: []string{"v.attack_w = 0;"},
		Scale:        "q.property('t:size') * 2",
	}
}

func TestController(t *testing.T) {
	rig := Compile(walker(), testLib(walkerAnims, walkerCtrl), nil)
	if !rig.Animated() {
		t.Fatal("rig is not animated")
	}
	notes := strings.Join(rig.Notes, "; ")
	for _, want := range []string{"is_item_name_any", "no_such_thing", "animation.humanoid.look_at"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes %q do not mention %s", notes, want)
		}
	}
	props := map[string]float64{"t:size": 0.75}
	st := rig.NewState(7)
	st.In = Inputs{OnGround: true, Prop: func(n string) (float64, bool) { v, ok := props[n]; return v, ok }}
	step := func(n int) {
		for i := 0; i < n; i++ {
			st.Step(0.05)
		}
	}
	rotOf := func(name string) [3]float64 {
		for i, b := range rig.Bones {
			if b.Name == name {
				r, _, _ := st.Local(i)
				return r
			}
		}
		t.Fatalf("no bone %s", name)
		return [3]float64{}
	}
	step(1)
	if got := st.Playing(); got != "move=idle" {
		t.Fatalf("playing %q at the start", got)
	}
	if s := st.Scale(); !near(s, 1.5, 1e-9) {
		t.Fatalf("scripts.scale %v", s)
	}
	step(20) // one second of idle: sin(90 degrees) * 10
	if r := rotOf("head"); !near(r[0], 10, 0.01) {
		t.Fatalf("idle head rotation %v after a second", r)
	}
	if _, p, _ := st.Local(2); rotOf("leg_left") != ([3]float64{}) || !nearV(p, [3]float64{0, 1, 0}, 1e-9) {
		// The head's position of the broken animation applies (its rotation does not).
		t.Fatalf("leg %v, head offset %v", rotOf("leg_left"), p)
	}
	// Walking: the controller changes state, blends for 0.2 s, then the legs swing in turn.
	st.In.GroundSpeed = 2
	step(1)
	if got := st.Playing(); got != "move=walk" {
		t.Fatalf("playing %q while moving", got)
	}
	step(8)
	l, r := rotOf("leg_left"), rotOf("leg_right")
	if math.Abs(l[0]) < 1 || !near(l[0], -r[0], 1e-6) {
		t.Fatalf("legs %v %v while walking", l, r)
	}
	if h := rotOf("head"); h != ([3]float64{}) {
		t.Fatalf("idle still moves the head after the blend: %v", h)
	}
	// A conditional animation that holds its last frame, and starts over when asked again.
	props["t:waving"] = 1
	step(20)
	if a := rotOf("arm"); !near(a[2], 90, 1e-4) {
		t.Fatalf("arm %v after the wave", a)
	}
	props["t:waving"] = 0
	step(1)
	props["t:waving"] = 1
	step(2)
	if a := rotOf("arm"); a[2] <= 0 || a[2] > 40 {
		t.Fatalf("arm %v right after the wave started again", a)
	}
	// In the air: a one-shot animation; back on the ground the state waits for it to finish.
	st.In.GroundSpeed, st.In.OnGround = 0, false
	step(3)
	if got := st.Playing(); got != "move=air" {
		t.Fatalf("playing %q in the air (via idle)", got)
	}
	st.In.OnGround = true
	step(2)
	if got := st.Playing(); got != "move=air" {
		t.Fatalf("left the air state before its animation finished: %q", got)
	}
	step(12)
	if got := st.Playing(); got != "move=idle" {
		t.Fatalf("playing %q after landing", got)
	}
	if b := rotOf("body"); b != ([3]float64{}) {
		t.Fatalf("body %v after the fall animation ended", b)
	}
}

func TestPlan(t *testing.T) {
	src := walker()
	// Twenty feathers on the body that all move a little, more than the budget has room for.
	anims := walkerAnims + `, "animation.t.feathers": {"loop": true, "bones": {`
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("feather%d", i)
		src.Bones = append(src.Bones, BoneDef{Name: name, Parent: "body", Pivot: [3]float64{float64(i) - 10, 12, 3}})
		if i > 0 {
			anims += ","
		}
		anims += fmt.Sprintf(`"%s": {"rotation": ["math.sin(q.anim_time*200+%d)*%d", 0, 0]}`, name, i*20, i+1)
	}
	anims += `}}`
	src.Animations["feathers"] = "animation.t.feathers"
	src.Animate = append(src.Animate, json.RawMessage(`"feathers"`))
	rig := Compile(src, testLib(anims, walkerCtrl), nil)
	boxes := make([]Box, len(rig.Bones))
	for i, b := range rig.Bones {
		if b.Name == "root" {
			continue // no cubes
		}
		boxes[i] = Box{Lo: [3]float64{b.Pivot[0] - 1, b.Pivot[1] - 4, b.Pivot[2] - 1}, Hi: [3]float64{b.Pivot[0] + 1, b.Pivot[1], b.Pivot[2] + 1}, OK: true}
	}
	index := func(name string) int {
		for i, b := range rig.Bones {
			if b.Name == name {
				return i
			}
		}
		return -1
	}
	check := func(maxParts int) map[int][]int {
		t.Helper()
		plan := MakePlan(rig, boxes, maxParts)
		if len(plan.Parts) > maxParts {
			t.Fatalf("%d parts for a budget of %d", len(plan.Parts), maxParts)
		}
		seen := map[int]bool{}
		parts := map[int][]int{}
		for _, p := range plan.Parts {
			parts[p.Bone] = p.Members
			for _, m := range p.Members {
				if seen[m] {
					t.Fatalf("bone %s is on two parts", rig.Bones[m].Name)
				}
				seen[m] = true
			}
		}
		for i := range rig.Bones {
			if boxes[i].OK != seen[i] {
				t.Fatalf("bone %s: has cubes %v, drawn %v", rig.Bones[i].Name, boxes[i].OK, seen[i])
			}
		}
		return parts
	}
	// Room for everything: every bone that moves in a sampled pose has its part. The hat never
	// moves on the head; the body and the arm only move in animations no sampled situation
	// plays (a one-shot fall, a wave on a property), so they are on the model's root.
	all := check(100)
	for _, name := range []string{"head", "leg_left", "leg_right", "feather0", "feather19"} {
		if _, ok := all[index(name)]; !ok {
			t.Errorf("no part for %s with room for all", name)
		}
	}
	if _, ok := all[index("hat")]; ok {
		t.Error("the hat, which never moves, has a part of its own")
	}
	if m := all[-1]; len(m) != 2 {
		t.Errorf("the root part draws %v, want body and arm", m)
	}
	if m := all[index("head")]; len(m) != 2 {
		t.Errorf("the head's part draws %v, want head and hat", m)
	}
	// A tight budget: the small movers go first, a left leg never without the right one.
	tight := check(8)
	_, l := tight[index("leg_left")]
	_, r := tight[index("leg_right")]
	if l != r {
		t.Errorf("leg_left has a part: %v, leg_right: %v", l, r)
	}
	if _, ok := tight[index("feather19")]; !ok {
		t.Error("the feather that moves most lost its part")
	}
	if _, ok := tight[index("feather0")]; ok {
		t.Error("the feather that moves least kept its part")
	}
	// Nothing animated: one part, the model's root.
	still := *src
	still.Animate = nil
	plan := MakePlan(Compile(&still, testLib(anims, walkerCtrl), nil), boxes, 24)
	if len(plan.Parts) != 1 || plan.Parts[0].Bone != -1 {
		t.Fatalf("an unanimated model has parts %+v", plan.Parts)
	}
}

func TestLibrary(t *testing.T) {
	l := testLib(walkerAnims, walkerCtrl)
	l.AddFile("animations/unused.json", []byte(`{"animations": {"animation.t.never": {"loop": true}}}`))
	if !l.Has("animation.t.walk") || !l.Has("controller.animation.t.move") || l.Has("animation.t.walk.bones") || l.Has("loop") {
		t.Fatal("ids found wrongly")
	}
	rig := Compile(walker(), l, nil)
	if n := l.Compact(); n == 0 {
		t.Fatal("nothing kept")
	}
	if l.Has("animation.t.never") {
		t.Fatal("a file nothing used was kept")
	}
	// A rig compiled from the compacted library is the same.
	again := Compile(walker(), l, nil)
	if again.slots != rig.slots || len(again.ctrls) != len(rig.ctrls) || !again.Animated() {
		t.Fatalf("rig from the compacted library differs: %d/%d slots", again.slots, rig.slots)
	}
	// A model compiles for its parts only, lazily.
	m := NewModel([]Part{{Bone: 2, K: 1}, {Bone: -1, K: 1}}, walker(), l)
	if r := m.Rig(); !r.need[2] || !r.need[1] || r.need[5] || !m.Moves() {
		t.Fatalf("model rig evaluates %v", r.need)
	}
}

// TestGarbage feeds broken animation data: nothing may panic, and the rig still steps.
func TestGarbage(t *testing.T) {
	for i, anims := range []string{
		`"animation.t.idle": 5`,
		`"animation.t.idle": {"bones": []}`,
		`"animation.t.idle": {"loop": {}, "animation_length": "x", "bones": {"head": 3}}`,
		`"animation.t.idle": {"loop": true, "bones": {"head": {"rotation": {"0.0": {"post": {"a": 1}}}, "position": "((((", "scale": [0, 0, 0]}}}`,
		`"animation.t.idle": {"anim_time_update": "1/0", "blend_weight": "q.x", "bones": {"head": {"rotation": {"nan": [1, 2, 3]}, "scale": "0/0"}}}`,
	} {
		for _, ctrl := range []string{walkerCtrl, `"controller.animation.t.move": {"states": {"default": {"animations": [1, {"a": 2, "b": 3}, null], "transitions": [{"gone": "1"}, 7]}}}`,
			`"controller.animation.t.move": []`} {
			rig := Compile(walker(), testLib(anims, ctrl), nil)
			st := rig.NewState(uint64(i))
			for k := 0; k < 30; k++ {
				st.Step(0.1)
			}
			for b := range rig.Bones {
				ps := st.Pose(b, 1)
				for _, v := range append(append(ps.T[:], ps.S[:]...), ps.Q[:]...) {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Fatalf("case %d: pose %+v", i, ps)
					}
				}
			}
		}
	}
	// A bone that is its own ancestor.
	src := walker()
	src.Bones[0].Parent = "hat"
	Compile(src, testLib(walkerAnims, walkerCtrl), nil).NewState(1).Step(0.1)
}
