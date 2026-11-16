// Package molang compiles the part of Molang that entity animations use into closures: numbers,
// strings, arithmetic, comparisons, ternaries, ??, math functions, queries (answered by a Host),
// variables and temporaries with assignments, and statements separated by ';' with return.
// Loops, arrows, structs with braces and resource references are not supported: Compile returns
// an error for those. A query the package does not know compiles to 0 and is listed in
// Expr.Unknown, so the caller can decide to leave that expression out.
package molang

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// Query is a query the package knows by name. A Host answers them.
type Query int

// The queries (their Molang names are in queryNames).
const (
	QNone Query = iota
	QAnimTime
	QLifeTime
	QDeltaTime
	QIsOnGround
	QIsInWater
	QIsInWaterOrRain
	QIsMoving
	QGroundSpeed
	QVerticalSpeed
	QModifiedMoveSpeed
	QModifiedDistanceMoved
	QWalkDistance
	QIsSleeping
	QIsRiding
	QHasRider
	QIsSneaking
	QIsSprinting
	QIsSwimming
	QIsGliding
	QIsBaby
	QIsSitting
	QIsAngry
	QIsTamed
	QIsSheared
	QIsCharged
	QIsOnFire
	QIsInvisible
	QIsAlive
	QIsStanding
	QVariant
	QMarkVariant
	QSkinID
	QTargetXRotation
	QTargetYRotation
	QBodyXRotation
	QBodyYRotation
	QHealth
	QMaxHealth
	QTimeOfDay
	QProperty
	QHasProperty
	QAllAnimationsFinished
	QAnyAnimationFinished
	QIsDelayedAttacking
	QIsAttacking
	QIsLeashed
	QIsSaddled
	QIsChested
	QIsIgnited
	QIsStunned
	QIsRoaring
	QIsResting
	QIsOrphaned
	QIsPowered
	QIsInLove
	QIsBribed
	QIsTransforming
	QIsCasting
	QIsStackable
	QIsInterested
	QIsEating
	QIsLayingDown
	QIsShaking
	QIsCelebrating
	QIsAdmiring
	QIsEmoting
	QIsDigging
	QIsRolling
	QIsFirstPerson
	QIsWallClimbing
	QCanFly
	QCanSwim
	QCanWalk
	QCanClimb
	QIsLevitating
	QIsJumping
	QIsInContactWithWater
	QIsSpectator
)

var queryNames = map[string]Query{
	"anim_time": QAnimTime, "life_time": QLifeTime, "delta_time": QDeltaTime, "is_on_ground": QIsOnGround,
	"is_in_water": QIsInWater, "is_in_water_or_rain": QIsInWaterOrRain, "is_moving": QIsMoving,
	"ground_speed": QGroundSpeed, "vertical_speed": QVerticalSpeed, "modified_move_speed": QModifiedMoveSpeed,
	"modified_distance_moved": QModifiedDistanceMoved, "walk_distance": QWalkDistance, "is_sleeping": QIsSleeping,
	"is_riding": QIsRiding, "has_rider": QHasRider, "is_sneaking": QIsSneaking, "is_sprinting": QIsSprinting,
	"is_swimming": QIsSwimming, "is_gliding": QIsGliding, "is_baby": QIsBaby, "is_sitting": QIsSitting,
	"is_angry": QIsAngry, "is_tamed": QIsTamed, "is_sheared": QIsSheared, "is_charged": QIsCharged,
	"is_on_fire": QIsOnFire, "is_invisible": QIsInvisible, "is_alive": QIsAlive, "is_standing": QIsStanding,
	"variant": QVariant, "mark_variant": QMarkVariant, "skin_id": QSkinID, "target_x_rotation": QTargetXRotation,
	"target_y_rotation": QTargetYRotation, "body_x_rotation": QBodyXRotation, "body_y_rotation": QBodyYRotation,
	"health": QHealth, "max_health": QMaxHealth, "time_of_day": QTimeOfDay, "property": QProperty,
	"has_property": QHasProperty, "all_animations_finished": QAllAnimationsFinished,
	"any_animation_finished": QAnyAnimationFinished, "is_delayed_attacking": QIsDelayedAttacking,
	"is_attacking": QIsAttacking, "is_leashed": QIsLeashed, "is_saddled": QIsSaddled,
	"is_chested": QIsChested, "is_ignited": QIsIgnited, "is_stunned": QIsStunned, "is_roaring": QIsRoaring,
	"is_resting": QIsResting, "is_orphaned": QIsOrphaned, "is_powered": QIsPowered, "is_in_love": QIsInLove,
	"is_bribed": QIsBribed, "is_transforming": QIsTransforming, "is_casting": QIsCasting,
	"is_stackable": QIsStackable, "is_interested": QIsInterested, "is_eating": QIsEating,
	"is_laying_down": QIsLayingDown, "is_shaking": QIsShaking, "is_celebrating": QIsCelebrating,
	"is_admiring": QIsAdmiring, "is_emoting": QIsEmoting, "is_digging": QIsDigging, "is_rolling": QIsRolling,
	"is_first_person": QIsFirstPerson, "is_wall_climbing": QIsWallClimbing,
	"can_fly": QCanFly, "can_swim": QCanSwim, "can_walk": QCanWalk, "can_climb": QCanClimb,
	"is_levitating": QIsLevitating, "is_jumping": QIsJumping,
	"is_in_contact_with_water": QIsInContactWithWater, "is_spectator": QIsSpectator,
}

// Host answers queries. args are the call's arguments (strings as Intern values).
type Host interface {
	Query(q Query, args []float64) float64
}

// Symbols gives the variables of the expressions compiled with it their slots: expressions that
// share variables (an entity's scripts, controllers and animations) share one Symbols.
type Symbols struct {
	mu    sync.Mutex
	vars  map[string]int
	temps map[string]int
}

// NewSymbols returns an empty table.
func NewSymbols() *Symbols { return &Symbols{vars: map[string]int{}, temps: map[string]int{}} }

func (s *Symbols) slot(m map[string]int, name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := m[name]
	if !ok {
		i = len(m)
		m[name] = i
	}
	return i
}

// Var is the slot of a variable ("pose" for v.pose), made if new.
func (s *Symbols) Var(name string) int { return s.slot(s.vars, strings.ToLower(name)) }

// Env is the state expressions run in: one per entity.
type Env struct {
	Host     Host
	AnimTime float64 // q.anim_time
	LifeTime float64 // q.life_time
	Delta    float64 // q.delta_time
	vars     []float64
	set      []bool
	temps    []float64
	rnd      uint64
	ret      bool
	retVal   float64
}

// NewEnv returns an environment whose random numbers start from seed.
func NewEnv(h Host, seed uint64) *Env {
	return &Env{Host: h, rnd: seed*2685821657736338717 + 1442695040888963407}
}

// Get is a variable's value and whether it was ever set.
func (e *Env) Get(slot int) (float64, bool) {
	if slot < len(e.vars) {
		return e.vars[slot], e.set[slot]
	}
	return 0, false
}

// Set gives a variable a value.
func (e *Env) Set(slot int, v float64) {
	for slot >= len(e.vars) {
		e.vars = append(e.vars, 0)
		e.set = append(e.set, false)
	}
	e.vars[slot], e.set[slot] = v, true
}

func (e *Env) random() float64 {
	e.rnd ^= e.rnd << 13
	e.rnd ^= e.rnd >> 7
	e.rnd ^= e.rnd << 17
	return float64(e.rnd>>11) / (1 << 53)
}

// Strings are numbers to expressions: a string literal and a string a Host returns (an enum
// property) are the same number when they are equal.
var (
	internMu sync.RWMutex
	interned = map[string]float64{}
	strs     []string
)

const (
	internBase = 9.0e15 // far from any number an animation computes
	maxStrings = 1 << 16
)

// Intern is the number of a string.
func Intern(s string) float64 {
	internMu.RLock()
	v, ok := interned[s]
	internMu.RUnlock()
	if ok {
		return v
	}
	internMu.Lock()
	defer internMu.Unlock()
	if v, ok := interned[s]; ok {
		return v
	}
	if len(strs) >= maxStrings {
		return internBase - 1 // no room: equal to no other string
	}
	v = internBase + float64(len(strs))
	strs = append(strs, s)
	interned[s] = v
	return v
}

// StringOf is the string a number stands for.
func StringOf(v float64) (string, bool) {
	if v < internBase || v != math.Floor(v) {
		return "", false
	}
	internMu.RLock()
	defer internMu.RUnlock()
	if i := int(v - internBase); i < len(strs) {
		return strs[i], true
	}
	return "", false
}

type fn func(e *Env) float64

// Expr is a compiled expression or statement list.
type Expr struct {
	f       fn
	Const   bool     // Val is its value whatever the Env
	Val     float64  // the value of a constant expression
	Unknown []string // queries, context values and functions it uses that the package does not know
	Random  bool     // uses math.random and the like
}

// Num is a constant expression.
func Num(v float64) *Expr { return &Expr{Const: true, Val: v, f: func(*Env) float64 { return v }} }

// Eval runs the expression. A nil expression is 0. The result is never NaN or infinite.
func (x *Expr) Eval(e *Env) float64 {
	if x == nil {
		return 0
	}
	if x.Const {
		return x.Val
	}
	v := x.f(e)
	if e.ret {
		v, e.ret = e.retVal, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// Known reports whether the expression uses nothing the package does not know.
func (x *Expr) Known() bool { return x == nil || len(x.Unknown) == 0 }

type node struct {
	f     fn
	konst bool
	val   float64
	slot  int // slot + 1 of a plain variable reference (for ?? and assignment)
	temp  bool
}

func constNode(v float64) node { return node{f: func(*Env) float64 { return v }, konst: true, val: v} }

type parser struct {
	src     string
	i       int
	syms    *Symbols
	unknown map[string]bool
	random  bool
	depth   int
}

const maxSource = 16 << 10

// Compile compiles source. syms may be nil for expressions without variables to share.
func Compile(src string, syms *Symbols) (x *Expr, err error) {
	if len(src) > maxSource {
		return nil, fmt.Errorf("molang: expression of %d bytes", len(src))
	}
	if syms == nil {
		syms = NewSymbols()
	}
	p := &parser{src: src, syms: syms, unknown: map[string]bool{}}
	defer func() {
		if r := recover(); r != nil {
			x, err = nil, fmt.Errorf("molang: %v in %q", r, clip(src))
		}
	}()
	n := p.program()
	x = &Expr{f: n.f, Const: n.konst, Val: n.val, Random: p.random}
	if x.Const && (math.IsNaN(x.Val) || math.IsInf(x.Val, 0)) {
		x.Val = 0
	}
	for k := range p.unknown {
		x.Unknown = append(x.Unknown, k)
	}
	return x, nil
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func (p *parser) fail(format string, a ...any) { panic(fmt.Sprintf(format, a...)) }

func (p *parser) space() {
	for p.i < len(p.src) {
		switch p.src[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) eat(tok string) bool {
	p.space()
	if strings.HasPrefix(p.src[p.i:], tok) {
		p.i += len(tok)
		return true
	}
	return false
}

func (p *parser) peek(tok string) bool {
	p.space()
	return strings.HasPrefix(p.src[p.i:], tok)
}

func (p *parser) program() node {
	var stmts []node
	simple := true
	for {
		p.space()
		if p.i >= len(p.src) {
			break
		}
		if p.eat(";") {
			simple = false
			continue
		}
		n, isExpr := p.statement()
		simple = simple && isExpr
		stmts = append(stmts, n)
		p.space()
		if p.i >= len(p.src) {
			break
		}
		if !p.eat(";") {
			p.fail("unexpected %q", clip(p.src[p.i:]))
		}
	}
	switch {
	case len(stmts) == 0:
		return constNode(0)
	case len(stmts) == 1 && simple:
		return stmts[0]
	}
	fs := make([]fn, len(stmts))
	for i, s := range stmts {
		fs[i] = s.f
	}
	return node{f: func(e *Env) float64 {
		for _, f := range fs {
			f(e)
			if e.ret {
				break
			}
		}
		return 0 // a statement list has the value of its return (Eval takes it)
	}}
}

// statement is one statement; isExpr is false for an assignment or a return.
func (p *parser) statement() (n node, isExpr bool) {
	p.space()
	if p.word("return") {
		v := p.expr()
		vf := v.f
		return node{f: func(e *Env) float64 {
			x := vf(e)
			e.retVal, e.ret = x, true
			return x
		}}, false
	}
	start := p.i
	n = p.expr()
	p.space()
	if p.i < len(p.src) && p.src[p.i] == '=' && !strings.HasPrefix(p.src[p.i:], "==") {
		if n.slot == 0 {
			p.fail("assignment to %q", clip(p.src[start:p.i]))
		}
		p.i++
		v := p.expr()
		slot, vf := n.slot-1, v.f
		if n.temp {
			return node{f: func(e *Env) float64 {
				x := vf(e)
				for slot >= len(e.temps) {
					e.temps = append(e.temps, 0)
				}
				e.temps[slot] = x
				return x
			}}, false
		}
		return node{f: func(e *Env) float64 {
			x := vf(e)
			e.Set(slot, x)
			return x
		}}, false
	}
	return n, true
}

// word eats a keyword that is not the start of a longer name.
func (p *parser) word(w string) bool {
	p.space()
	if !strings.HasPrefix(strings.ToLower(p.src[p.i:min(len(p.src), p.i+len(w))]), w) {
		return false
	}
	if j := p.i + len(w); j < len(p.src) && isName(p.src[j]) {
		return false
	}
	p.i += len(w)
	return true
}

func isName(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.'
}

func (p *parser) expr() node {
	p.depth++
	if p.depth > 200 {
		p.fail("expression nested too deep")
	}
	defer func() { p.depth-- }()
	c := p.nullish()
	if !p.peek("?") || p.peek("??") {
		return c
	}
	p.eat("?")
	a := p.expr()
	if !p.eat(":") {
		// The binary conditional: a ? b is b when a holds, else 0.
		if c.konst {
			if c.val != 0 {
				a.slot = 0
				return a
			}
			return constNode(0)
		}
		cf, af := c.f, a.f
		return node{f: func(e *Env) float64 {
			if cf(e) != 0 {
				return af(e)
			}
			return 0
		}}
	}
	b := p.expr()
	if c.konst {
		if c.val != 0 {
			a.slot = 0
			return a
		}
		b.slot = 0
		return b
	}
	cf, af, bf := c.f, a.f, b.f
	return node{f: func(e *Env) float64 {
		if cf(e) != 0 {
			return af(e)
		}
		return bf(e)
	}}
}

func (p *parser) nullish() node {
	l := p.or()
	for p.eat("??") {
		r := p.or()
		if l.slot == 0 || l.temp {
			l.slot = 0
			continue // anything but a variable has a value
		}
		slot, lf, rf := l.slot-1, l.f, r.f
		l = node{f: func(e *Env) float64 {
			if slot < len(e.set) && e.set[slot] {
				return lf(e)
			}
			return rf(e)
		}}
	}
	return l
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func binary(a, b node, op func(x, y float64) float64) node {
	if a.konst && b.konst {
		return constNode(op(a.val, b.val))
	}
	af, bf := a.f, b.f
	return node{f: func(e *Env) float64 { return op(af(e), bf(e)) }}
}

func (p *parser) or() node {
	l := p.and()
	for p.peek("||") {
		p.eat("||")
		r := p.and()
		lf, rf := l.f, r.f
		if l.konst && r.konst {
			l = constNode(b2f(l.val != 0 || r.val != 0))
			continue
		}
		l = node{f: func(e *Env) float64 { return b2f(lf(e) != 0 || rf(e) != 0) }}
	}
	return l
}

func (p *parser) and() node {
	l := p.cmp()
	for p.peek("&&") {
		p.eat("&&")
		r := p.cmp()
		lf, rf := l.f, r.f
		if l.konst && r.konst {
			l = constNode(b2f(l.val != 0 && r.val != 0))
			continue
		}
		l = node{f: func(e *Env) float64 { return b2f(lf(e) != 0 && rf(e) != 0) }}
	}
	return l
}

func (p *parser) cmp() node {
	l := p.sum()
	for {
		switch {
		case p.eat("=="):
			l = binary(l, p.sum(), func(x, y float64) float64 { return b2f(x == y) })
		case p.eat("!="):
			l = binary(l, p.sum(), func(x, y float64) float64 { return b2f(x != y) })
		case p.eat("<="):
			l = binary(l, p.sum(), func(x, y float64) float64 { return b2f(x <= y) })
		case p.eat(">="):
			l = binary(l, p.sum(), func(x, y float64) float64 { return b2f(x >= y) })
		case p.eat("<"):
			l = binary(l, p.sum(), func(x, y float64) float64 { return b2f(x < y) })
		case p.eat(">"):
			l = binary(l, p.sum(), func(x, y float64) float64 { return b2f(x > y) })
		default:
			return l
		}
	}
}

func (p *parser) sum() node {
	l := p.prod()
	for {
		switch {
		case p.eat("+"):
			l = binary(l, p.prod(), func(x, y float64) float64 { return x + y })
		case p.peek("->"):
			p.fail("arrow operator")
		case p.eat("-"):
			l = binary(l, p.prod(), func(x, y float64) float64 { return x - y })
		default:
			return l
		}
	}
}

func (p *parser) prod() node {
	l := p.unary()
	for {
		switch {
		case p.eat("*"):
			l = binary(l, p.unary(), func(x, y float64) float64 { return x * y })
		case p.eat("/"):
			l = binary(l, p.unary(), func(x, y float64) float64 {
				if y == 0 {
					return 0
				}
				return x / y
			})
		default:
			return l
		}
	}
}

func (p *parser) unary() node {
	switch {
	case p.eat("-"):
		v := p.unary()
		if v.konst {
			return constNode(-v.val)
		}
		f := v.f
		return node{f: func(e *Env) float64 { return -f(e) }}
	case p.eat("+"):
		v := p.unary()
		v.slot = 0
		return v
	case p.eat("!"):
		v := p.unary()
		if v.konst {
			return constNode(b2f(v.val == 0))
		}
		f := v.f
		return node{f: func(e *Env) float64 { return b2f(f(e) == 0) }}
	}
	return p.atom()
}

func (p *parser) atom() node {
	p.space()
	if p.i >= len(p.src) {
		p.fail("unexpected end")
	}
	c := p.src[p.i]
	switch {
	case c == '(':
		p.i++
		v := p.expr()
		if !p.eat(")") {
			p.fail("missing )")
		}
		v.slot = 0
		return v
	case c == '\'':
		j := strings.IndexByte(p.src[p.i+1:], '\'')
		if j < 0 {
			p.fail("unterminated string")
		}
		s := p.src[p.i+1 : p.i+1+j]
		p.i += j + 2
		return constNode(Intern(s))
	case c >= '0' && c <= '9' || c == '.':
		j := p.i
		for j < len(p.src) && (p.src[j] >= '0' && p.src[j] <= '9' || p.src[j] == '.') {
			j++
		}
		if j < len(p.src) && (p.src[j] == 'e' || p.src[j] == 'E') {
			k := j + 1
			if k < len(p.src) && (p.src[k] == '+' || p.src[k] == '-') {
				k++
			}
			if k < len(p.src) && p.src[k] >= '0' && p.src[k] <= '9' {
				for k < len(p.src) && p.src[k] >= '0' && p.src[k] <= '9' {
					k++
				}
				j = k
			}
		}
		v, err := strconv.ParseFloat(p.src[p.i:j], 64)
		if err != nil {
			p.fail("number %q", p.src[p.i:j])
		}
		p.i = j
		if j < len(p.src) && (p.src[j] == 'f' || p.src[j] == 'F') {
			p.i++
		}
		return constNode(v)
	}
	j := p.i
	for j < len(p.src) && isName(p.src[j]) {
		j++
	}
	if j == p.i {
		p.fail("unexpected %q", clip(p.src[p.i:]))
	}
	name := strings.ToLower(p.src[p.i:j])
	p.i = j
	var args []node
	if p.eat("(") {
		if !p.eat(")") {
			for {
				args = append(args, p.expr())
				if p.eat(")") {
					break
				}
				if !p.eat(",") {
					p.fail("missing , or )")
				}
			}
		}
	}
	if p.peek("[") || p.peek("{") {
		p.fail("unsupported syntax after %s", name)
	}
	prefix, rest, dotted := strings.Cut(name, ".")
	if !dotted {
		switch name {
		case "true":
			return constNode(1)
		case "false", "this":
			return constNode(0)
		}
		p.fail("unknown word %q", name)
	}
	switch prefix {
	case "q", "query":
		return p.query(rest, args)
	case "v", "variable":
		slot := p.syms.slot(p.syms.vars, rest)
		return node{slot: slot + 1, f: func(e *Env) float64 {
			if slot < len(e.vars) {
				return e.vars[slot]
			}
			return 0
		}}
	case "t", "temp":
		slot := p.syms.slot(p.syms.temps, rest)
		return node{slot: slot + 1, temp: true, f: func(e *Env) float64 {
			if slot < len(e.temps) {
				return e.temps[slot]
			}
			return 0
		}}
	case "c", "context":
		if rest == "is_first_person" {
			return constNode(0)
		}
		p.unknown["c."+rest] = true
		return constNode(0)
	case "math":
		return p.math(rest, args)
	}
	p.fail("unsupported name %q", name)
	return node{}
}

func (p *parser) query(name string, args []node) node {
	q, ok := queryNames[name]
	if !ok {
		p.unknown["q."+name] = true
		return constNode(0)
	}
	switch q {
	case QAnimTime:
		return node{f: func(e *Env) float64 { return e.AnimTime }}
	case QLifeTime:
		return node{f: func(e *Env) float64 { return e.LifeTime }}
	case QDeltaTime:
		return node{f: func(e *Env) float64 { return e.Delta }}
	case QIsFirstPerson, QIsSpectator:
		return constNode(0)
	}
	if len(args) == 0 {
		return node{f: func(e *Env) float64 {
			if e.Host == nil {
				return 0
			}
			return e.Host.Query(q, nil)
		}}
	}
	fs := make([]fn, len(args))
	allConst := true
	vals := make([]float64, len(args))
	for i, a := range args {
		fs[i] = a.f
		allConst = allConst && a.konst
		vals[i] = a.val
	}
	if allConst {
		return node{f: func(e *Env) float64 {
			if e.Host == nil {
				return 0
			}
			return e.Host.Query(q, vals)
		}}
	}
	return node{f: func(e *Env) float64 {
		if e.Host == nil {
			return 0
		}
		var buf [4]float64
		v := buf[:0]
		for _, f := range fs {
			v = append(v, f(e))
		}
		return e.Host.Query(q, v)
	}}
}

const deg = math.Pi / 180

func (p *parser) math(name string, args []node) node {
	if name == "pi" {
		return constNode(math.Pi)
	}
	need := func(n int) {
		if len(args) < n {
			p.fail("math.%s needs %d arguments", name, n)
		}
	}
	f1 := func(op func(x float64) float64) node {
		need(1)
		if args[0].konst {
			return constNode(op(args[0].val))
		}
		f := args[0].f
		return node{f: func(e *Env) float64 { return op(f(e)) }}
	}
	f2 := func(op func(x, y float64) float64) node {
		need(2)
		return binary(args[0], args[1], op)
	}
	f3 := func(op func(x, y, z float64) float64) node {
		need(3)
		a, b, c := args[0].f, args[1].f, args[2].f
		if args[0].konst && args[1].konst && args[2].konst {
			return constNode(op(args[0].val, args[1].val, args[2].val))
		}
		return node{f: func(e *Env) float64 { return op(a(e), b(e), c(e)) }}
	}
	switch name {
	case "sin":
		return f1(func(x float64) float64 { return math.Sin(x * deg) })
	case "cos":
		return f1(func(x float64) float64 { return math.Cos(x * deg) })
	case "asin":
		return f1(func(x float64) float64 { return math.Asin(max(-1, min(1, x))) / deg })
	case "acos":
		return f1(func(x float64) float64 { return math.Acos(max(-1, min(1, x))) / deg })
	case "atan":
		return f1(func(x float64) float64 { return math.Atan(x) / deg })
	case "atan2":
		return f2(func(y, x float64) float64 { return math.Atan2(y, x) / deg })
	case "abs":
		return f1(math.Abs)
	case "floor":
		return f1(math.Floor)
	case "ceil":
		return f1(math.Ceil)
	case "round":
		return f1(math.Round)
	case "trunc":
		return f1(math.Trunc)
	case "sqrt":
		return f1(func(x float64) float64 { return math.Sqrt(max(0, x)) })
	case "exp":
		return f1(math.Exp)
	case "ln":
		return f1(func(x float64) float64 {
			if x <= 0 {
				return 0
			}
			return math.Log(x)
		})
	case "sign":
		return f1(func(x float64) float64 {
			switch {
			case x > 0:
				return 1
			case x < 0:
				return -1
			}
			return 0
		})
	case "pow":
		return f2(math.Pow)
	case "min":
		return f2(math.Min)
	case "max":
		return f2(math.Max)
	case "mod":
		return f2(func(x, y float64) float64 {
			if y == 0 {
				return 0
			}
			return math.Mod(x, y)
		})
	case "copy_sign":
		return f2(math.Copysign)
	case "min_angle":
		return f1(func(x float64) float64 {
			x = math.Mod(x, 360)
			if x >= 180 {
				x -= 360
			} else if x < -180 {
				x += 360
			}
			return x
		})
	case "clamp":
		return f3(func(x, lo, hi float64) float64 { return max(lo, min(hi, x)) })
	case "lerp":
		return f3(func(a, b, t float64) float64 { return a + (b-a)*t })
	case "inverse_lerp":
		return f3(func(a, b, v float64) float64 {
			if a == b {
				return 0
			}
			return (v - a) / (b - a)
		})
	case "lerprotate":
		return f3(func(a, b, t float64) float64 {
			d := math.Mod(b-a, 360)
			if d > 180 {
				d -= 360
			} else if d < -180 {
				d += 360
			}
			return a + d*t
		})
	case "hermite_blend":
		return f1(func(t float64) float64 { return 3*t*t - 2*t*t*t })
	case "random":
		need(2)
		p.random = true
		a, b := args[0].f, args[1].f
		return node{f: func(e *Env) float64 {
			lo, hi := a(e), b(e)
			return lo + (hi-lo)*e.random()
		}}
	case "random_integer":
		need(2)
		p.random = true
		a, b := args[0].f, args[1].f
		return node{f: func(e *Env) float64 {
			lo, hi := math.Ceil(a(e)), math.Floor(b(e))
			if hi <= lo {
				return lo
			}
			return lo + math.Floor(e.random()*(hi-lo+1))
		}}
	case "die_roll", "die_roll_integer":
		need(3)
		p.random = true
		n, a, b := args[0].f, args[1].f, args[2].f
		integer := name == "die_roll_integer"
		return node{f: func(e *Env) float64 {
			lo, hi, sum := a(e), b(e), 0.0
			for i := 0; i < int(max(0, min(64, n(e)))); i++ {
				v := lo + (hi-lo)*e.random()
				if integer {
					v = math.Round(v)
				}
				sum += v
			}
			return sum
		}}
	}
	p.unknown["math."+name] = true
	return constNode(0)
}
