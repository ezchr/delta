package anim

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"delta/core/internal/molang"
)

// Loop modes of an animation.
const (
	loopOnce = iota // plays once, then no longer moves anything
	loopLoop
	loopHold // plays once and keeps its last frame
)

// animation is one compiled animation: the tracks of the bones the rig evaluates.
type animation struct {
	id       string
	loop     int
	length   float64 // seconds; 0: no end (expressions of q.anim_time only)
	update   *molang.Expr
	weight   *molang.Expr
	override bool
	tracks   []track
}

type track struct {
	bone            int
	rot, pos, scale *channel
}

// vec is a channel value: three numbers, or expressions where ex is set.
type vec struct {
	c  [3]float32
	ex *[3]*molang.Expr
}

func (v *vec) eval(env *molang.Env) [3]float64 {
	if v.ex == nil {
		return [3]float64{float64(v.c[0]), float64(v.c[1]), float64(v.c[2])}
	}
	var o [3]float64
	for i := 0; i < 3; i++ {
		if x := v.ex[i]; x != nil {
			o[i] = x.Eval(env)
		} else {
			o[i] = float64(v.c[i])
		}
	}
	return o
}

type key struct {
	t      float32
	smooth bool // lerp_mode catmullrom
	post   vec
	pre    *vec // the value arriving at the key, when it differs from post
}

func (k *key) in(env *molang.Env) [3]float64 {
	if k.pre != nil {
		return k.pre.eval(env)
	}
	return k.post.eval(env)
}

// channel is one of a bone's rotation, position or scale in an animation: a value, or keyframes
// in time order.
type channel struct {
	keys []key
}

// sample is the channel's value at time t.
func (c *channel) sample(t float64, env *molang.Env) [3]float64 {
	n := len(c.keys)
	if n == 1 || t <= float64(c.keys[0].t) {
		if n > 1 {
			return c.keys[0].in(env)
		}
		return c.keys[0].post.eval(env)
	}
	if t >= float64(c.keys[n-1].t) {
		return c.keys[n-1].post.eval(env)
	}
	i := sort.Search(n, func(i int) bool { return float64(c.keys[i].t) > t }) - 1
	a, b := &c.keys[i], &c.keys[i+1]
	span := float64(b.t - a.t)
	if span <= 0 {
		return b.post.eval(env)
	}
	f := (t - float64(a.t)) / span
	p1, p2 := a.post.eval(env), b.in(env)
	var o [3]float64
	if !a.smooth && !b.smooth {
		for k := 0; k < 3; k++ {
			o[k] = p1[k] + (p2[k]-p1[k])*f
		}
		return o
	}
	// Catmull-Rom through the neighbouring keys (the ends count twice), as Blockbench plays it.
	p0, p3 := p1, p2
	if i > 0 {
		p0 = c.keys[i-1].post.eval(env)
	}
	if i+2 < n {
		p3 = c.keys[i+2].in(env)
	}
	f2, f3 := f*f, f*f*f
	for k := 0; k < 3; k++ {
		o[k] = 0.5 * (2*p1[k] + (p2[k]-p0[k])*f + (2*p0[k]-5*p1[k]+4*p2[k]-p3[k])*f2 + (3*p1[k]-p0[k]-3*p2[k]+p3[k])*f3)
	}
	return o
}

// compiler compiles one rig's animations.
type compiler struct {
	syms  *molang.Symbols
	notes map[string]bool // what could not be used, for the report
}

func (c *compiler) note(s string) {
	if len(c.notes) < 64 {
		c.notes[s] = true
	}
}

// expr compiles source; nil when it cannot be evaluated (a syntax the compiler does not have,
// or a query it does not know).
func (c *compiler) expr(src string) *molang.Expr {
	x, err := molang.Compile(src, c.syms)
	if err != nil {
		c.note(err.Error())
		return nil
	}
	if !x.Known() {
		c.note("unknown " + strings.Join(x.Unknown, ", "))
		return nil
	}
	return x
}

// scalar compiles a JSON number or string.
func (c *compiler) scalar(raw json.RawMessage) (v float64, x *molang.Expr, ok bool) {
	raw = trim(raw)
	if len(raw) == 0 {
		return 0, nil, false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, nil, false
		}
		x = c.expr(s)
		if x == nil {
			return 0, nil, false
		}
		if x.Const {
			return x.Val, nil, true
		}
		return 0, x, true
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		if string(raw) == "true" {
			return 1, nil, true
		}
		if string(raw) == "false" {
			return 0, nil, true
		}
		return 0, nil, false
	}
	return f, nil, true
}

func trim(raw json.RawMessage) json.RawMessage {
	for len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\n' || raw[0] == '\t' || raw[0] == '\r') {
		raw = raw[1:]
	}
	return raw
}

// vec compiles a channel value: [x, y, z], or one value for all three.
func (c *compiler) vec(raw json.RawMessage) (v vec, ok bool) {
	raw = trim(raw)
	if len(raw) == 0 {
		return v, false
	}
	set := func(i int, f float64, x *molang.Expr) {
		v.c[i] = float32(f)
		if x != nil {
			if v.ex == nil {
				v.ex = new([3]*molang.Expr)
			}
			v.ex[i] = x
		}
	}
	if raw[0] != '[' {
		f, x, ok := c.scalar(raw)
		if !ok {
			return v, false
		}
		for i := 0; i < 3; i++ {
			set(i, f, x)
		}
		return v, true
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil || len(list) == 0 {
		return v, false
	}
	if len(list) == 1 {
		list = []json.RawMessage{list[0], list[0], list[0]}
	}
	if len(list) < 3 {
		return v, false
	}
	for i := 0; i < 3; i++ {
		f, x, ok := c.scalar(list[i])
		if !ok {
			return v, false
		}
		set(i, f, x)
	}
	return v, true
}

// channel compiles a rotation, position or scale. nil: absent, or not evaluable (the bone then
// keeps still on that channel).
func (c *compiler) channel(raw json.RawMessage) *channel {
	raw = trim(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] != '{' {
		v, ok := c.vec(raw)
		if !ok {
			return nil
		}
		return &channel{keys: []key{{post: v}}}
	}
	var frames map[string]json.RawMessage
	if json.Unmarshal(raw, &frames) != nil || len(frames) == 0 {
		return nil
	}
	if _, ok := frames["post"]; ok {
		// One keyframe written as an object.
		frames = map[string]json.RawMessage{"0": raw}
	}
	ch := &channel{keys: make([]key, 0, len(frames))}
	for ts, fr := range frames {
		t, err := strconv.ParseFloat(strings.TrimSpace(ts), 64)
		if err != nil || !(t >= 0) || t > 1e6 {
			return nil
		}
		k := key{t: float32(t)}
		fr = trim(fr)
		if len(fr) > 0 && fr[0] == '{' {
			var o struct {
				Pre  json.RawMessage `json:"pre"`
				Post json.RawMessage `json:"post"`
				Lerp string          `json:"lerp_mode"`
			}
			if json.Unmarshal(fr, &o) != nil {
				return nil
			}
			k.smooth = o.Lerp == "catmullrom"
			if o.Post == nil {
				o.Post, o.Pre = o.Pre, nil
			}
			post, ok := c.vec(o.Post)
			if !ok {
				return nil
			}
			k.post = post
			if o.Pre != nil {
				pre, ok := c.vec(o.Pre)
				if !ok {
					return nil
				}
				k.pre = &pre
			}
		} else {
			v, ok := c.vec(fr)
			if !ok {
				return nil
			}
			k.post = v
		}
		ch.keys = append(ch.keys, k)
	}
	sort.Slice(ch.keys, func(i, j int) bool { return ch.keys[i].t < ch.keys[j].t })
	return ch
}

// animation compiles an animation for the bones in need (index by lower-case name; nil entries
// in need are bones the rig does not evaluate).
func (c *compiler) animation(id string, raw json.RawMessage, bones map[string]int, need []bool) *animation {
	var a struct {
		Loop     json.RawMessage `json:"loop"`
		Length   float64         `json:"animation_length"`
		Update   json.RawMessage `json:"anim_time_update"`
		Weight   json.RawMessage `json:"blend_weight"`
		Override bool            `json:"override_previous_animation"`
		Bones    map[string]struct {
			Rot   json.RawMessage `json:"rotation"`
			Pos   json.RawMessage `json:"position"`
			Scale json.RawMessage `json:"scale"`
		} `json:"bones"`
	}
	if json.Unmarshal(raw, &a) != nil {
		c.note("animation " + id + " is not readable")
		return nil
	}
	an := &animation{id: id, length: a.Length, override: a.Override}
	switch strings.Trim(string(trim(a.Loop)), `"`) {
	case "true":
		an.loop = loopLoop
	case "hold_on_last_frame":
		an.loop = loopHold
	}
	if len(a.Update) > 0 {
		if f, x, ok := c.scalar(a.Update); ok && x != nil {
			an.update = x
		} else if ok {
			an.update = molang.Num(f)
		}
	}
	if len(a.Weight) > 0 {
		if f, x, ok := c.scalar(a.Weight); ok && x != nil {
			an.weight = x
		} else if ok {
			an.weight = molang.Num(f)
		}
	}
	names := make([]string, 0, len(a.Bones))
	for n := range a.Bones {
		names = append(names, n)
	}
	sort.Strings(names)
	last := 0.0
	for _, n := range names {
		b := a.Bones[n]
		i, ok := bones[strings.ToLower(n)]
		tr := track{bone: i}
		if ok && need[i] {
			tr.rot, tr.pos, tr.scale = c.channel(b.Rot), c.channel(b.Pos), c.channel(b.Scale)
			for _, ch := range []*channel{tr.rot, tr.pos, tr.scale} {
				if ch != nil {
					last = max(last, float64(ch.keys[len(ch.keys)-1].t))
				}
			}
			if tr.rot != nil || tr.pos != nil || tr.scale != nil {
				an.tracks = append(an.tracks, tr)
			}
			continue
		}
		// Not evaluated: only its last keyframe time counts, for the animation's length.
		if a.Length <= 0 {
			for _, r := range []json.RawMessage{b.Rot, b.Pos, b.Scale} {
				last = max(last, lastKeyTime(r))
			}
		}
	}
	if an.length <= 0 {
		an.length = last
	}
	return an
}

// lastKeyTime is the last keyframe time of a channel given as keyframes (0 otherwise).
func lastKeyTime(raw json.RawMessage) float64 {
	raw = trim(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return 0
	}
	var frames map[string]json.RawMessage
	if json.Unmarshal(raw, &frames) != nil {
		return 0
	}
	last := 0.0
	for ts := range frames {
		if t, err := strconv.ParseFloat(strings.TrimSpace(ts), 64); err == nil && t >= 0 && t <= 1e6 {
			last = max(last, t)
		}
	}
	return last
}

// controller is a compiled animation controller.
type controller struct {
	id      string
	idx     int // index of its state in State.ctrl
	initial int
	states  []cstate
}

type cstate struct {
	name        string
	anims       []animRef
	trans       []transition
	blend       float64 // seconds to blend into the next state
	entry, exit []*molang.Expr
}

type transition struct {
	to   int
	cond *molang.Expr
}

// animRef plays an animation or a controller with a weight.
type animRef struct {
	weight *molang.Expr // nil: 1
	anim   *animation
	ctrl   *controller
	slot   int // the animation's clock in State.times
}

// named is a JSON list entry that is a name or {name: expression}.
func named(raw json.RawMessage) (name, expr string, ok bool) {
	raw = trim(raw)
	if len(raw) == 0 {
		return "", "", false
	}
	if raw[0] == '"' {
		return name, "", json.Unmarshal(raw, &name) == nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) != 1 {
		return "", "", false
	}
	for k, v := range m {
		name = k
		v = trim(v)
		if len(v) > 0 && v[0] == '"' {
			if json.Unmarshal(v, &expr) != nil {
				return "", "", false
			}
		} else {
			expr = string(v)
		}
	}
	return name, expr, true
}

type rawController struct {
	Initial string `json:"initial_state"`
	States  map[string]struct {
		Animations  []json.RawMessage `json:"animations"`
		Transitions []json.RawMessage `json:"transitions"`
		Blend       json.RawMessage   `json:"blend_transition"`
		OnEntry     []string          `json:"on_entry"`
		OnExit      []string          `json:"on_exit"`
	} `json:"states"`
}

// statements compiles script lines; a line that cannot be evaluated is left out.
func (c *compiler) statements(lines []string) []*molang.Expr {
	var out []*molang.Expr
	for _, l := range lines {
		x, err := molang.Compile(l, c.syms)
		if err != nil {
			c.note(err.Error())
			continue
		}
		// A script line that uses an unknown query still sets its variables (the query counts
		// as 0): leaving the line out would leave them unset, which is worse.
		for _, u := range x.Unknown {
			c.note("unknown " + u)
		}
		out = append(out, x)
	}
	return out
}
