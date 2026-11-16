package javapack

import (
	"math"
	"strconv"
	"strings"
)

// evalMolang evaluates the constant part of a Molang expression an attachable animation uses for a
// held item's pose: numbers, arithmetic, comparisons, !, && and ||, ternaries and the
// is_first_person query (c./context./q./query./v./variable.). Anything else (other queries,
// variables, math functions but a few) counts as 0; ok is false then.
func evalMolang(expr string, firstPerson bool) (v float64, ok bool) {
	p := &molang{s: strings.ToLower(expr), fp: firstPerson, ok: true}
	v = p.ternary()
	p.space()
	if p.i < len(p.s) {
		p.ok = false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, p.ok
}

type molang struct {
	s  string
	i  int
	fp bool
	ok bool
}

func (p *molang) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *molang) eat(tok string) bool {
	p.space()
	if strings.HasPrefix(p.s[p.i:], tok) {
		p.i += len(tok)
		return true
	}
	return false
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (p *molang) ternary() float64 {
	c := p.or()
	if p.eat("??") {
		_ = p.ternary()
		return c
	}
	if p.eat("?") {
		a := p.ternary()
		if !p.eat(":") {
			p.ok = false
			return a
		}
		b := p.ternary()
		if c != 0 {
			return a
		}
		return b
	}
	return c
}

func (p *molang) or() float64 {
	v := p.and()
	for p.eat("||") {
		w := p.and()
		v = b2f(v != 0 || w != 0)
	}
	return v
}

func (p *molang) and() float64 {
	v := p.cmp()
	for p.eat("&&") {
		w := p.cmp()
		v = b2f(v != 0 && w != 0)
	}
	return v
}

func (p *molang) cmp() float64 {
	v := p.sum()
	for {
		switch {
		case p.eat("=="):
			v = b2f(v == p.sum())
		case p.eat("!="):
			v = b2f(v != p.sum())
		case p.eat("<="):
			v = b2f(v <= p.sum())
		case p.eat(">="):
			v = b2f(v >= p.sum())
		case p.eat("<"):
			v = b2f(v < p.sum())
		case p.eat(">"):
			v = b2f(v > p.sum())
		default:
			return v
		}
	}
}

func (p *molang) sum() float64 {
	v := p.prod()
	for {
		switch {
		case p.eat("+"):
			v += p.prod()
		case p.eat("-"):
			v -= p.prod()
		default:
			return v
		}
	}
}

func (p *molang) prod() float64 {
	v := p.unary()
	for {
		switch {
		case p.eat("*"):
			v *= p.unary()
		case p.eat("/"):
			d := p.unary()
			if d == 0 {
				p.ok = false
				v = 0
			} else {
				v /= d
			}
		default:
			return v
		}
	}
}

func (p *molang) unary() float64 {
	switch {
	case p.eat("-"):
		return -p.unary()
	case p.eat("+"):
		return p.unary()
	case p.eat("!"):
		return b2f(p.unary() == 0)
	}
	return p.atom()
}

func (p *molang) atom() float64 {
	p.space()
	if p.i >= len(p.s) {
		p.ok = false
		return 0
	}
	if p.eat("(") {
		v := p.ternary()
		if !p.eat(")") {
			p.ok = false
		}
		return v
	}
	c := p.s[p.i]
	if c >= '0' && c <= '9' || c == '.' {
		j := p.i
		for j < len(p.s) && (p.s[j] >= '0' && p.s[j] <= '9' || p.s[j] == '.') {
			j++
		}
		v, err := strconv.ParseFloat(p.s[p.i:j], 64)
		p.i = j
		if j < len(p.s) && p.s[j] == 'f' {
			p.i++
		}
		if err != nil {
			p.ok = false
		}
		return v
	}
	j := p.i
	for j < len(p.s) && (p.s[j] >= 'a' && p.s[j] <= 'z' || p.s[j] >= '0' && p.s[j] <= '9' || p.s[j] == '_' || p.s[j] == '.') {
		j++
	}
	name := p.s[p.i:j]
	p.i = j
	if name == "" {
		p.ok = false
		p.i++
		return 0
	}
	var args []float64
	if p.eat("(") {
		if !p.eat(")") {
			for {
				args = append(args, p.ternary())
				if p.eat(")") {
					break
				}
				if !p.eat(",") {
					p.ok = false
					return 0
				}
			}
		}
	}
	if _, q, ok := strings.Cut(name, "."); ok {
		switch {
		case q == "is_first_person":
			return b2f(p.fp)
		case name == "math.pi":
			return math.Pi
		}
		if strings.HasPrefix(name, "math.") {
			deg := math.Pi / 180
			arg := func(i int) float64 {
				if i < len(args) {
					return args[i]
				}
				p.ok = false
				return 0
			}
			switch q {
			case "sin":
				return math.Sin(arg(0) * deg)
			case "cos":
				return math.Cos(arg(0) * deg)
			case "abs":
				return math.Abs(arg(0))
			case "min":
				return math.Min(arg(0), arg(1))
			case "max":
				return math.Max(arg(0), arg(1))
			case "clamp":
				return math.Max(arg(1), math.Min(arg(2), arg(0)))
			case "floor":
				return math.Floor(arg(0))
			case "ceil":
				return math.Ceil(arg(0))
			case "round":
				return math.Round(arg(0))
			case "sqrt":
				return math.Sqrt(arg(0))
			}
		}
	}
	if name == "true" {
		return 1
	}
	if name == "false" {
		return 0
	}
	p.ok = false
	return 0
}
