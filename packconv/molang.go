package packconv

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// A small Molang evaluator for block permutation conditions:
// q.block_state('name') / query.block_property('name') compared with numbers, strings and
// booleans, with ! && || == != < <= > >= + - * / ?: and parentheses. Anything else is an error
// (the caller then leaves the permutation out and reports it).

type mval struct {
	s     string
	n     float64
	isStr bool
}

func (v mval) truth() bool {
	if v.isStr {
		return v.s != ""
	}
	return v.n != 0
}

func (v mval) eq(o mval) bool {
	if v.isStr || o.isStr {
		return v.str() == o.str()
	}
	return math.Abs(v.n-o.n) < 1e-9
}

func (v mval) str() string {
	if v.isStr {
		return v.s
	}
	return strconv.FormatFloat(v.n, 'f', -1, 64)
}

type molang struct {
	src    string
	pos    int
	states map[string]any
}

// evalCondition evaluates a permutation condition with the given block states.
func evalCondition(expr string, states map[string]any) (bool, error) {
	m := &molang{src: expr, states: states}
	v, err := m.ternary()
	if err != nil {
		return false, err
	}
	m.space()
	if m.pos < len(m.src) && m.src[m.pos] == ';' {
		m.pos++
		m.space()
	}
	if m.pos != len(m.src) {
		return false, fmt.Errorf("unexpected %q", m.src[m.pos:])
	}
	return v.truth(), nil
}

func (m *molang) space() {
	for m.pos < len(m.src) && strings.ContainsRune(" \t\r\n", rune(m.src[m.pos])) {
		m.pos++
	}
}

func (m *molang) eat(tok string) bool {
	m.space()
	if strings.HasPrefix(m.src[m.pos:], tok) {
		m.pos += len(tok)
		return true
	}
	return false
}

func (m *molang) ternary() (mval, error) {
	c, err := m.or()
	if err != nil {
		return c, err
	}
	if m.eat("?") {
		a, err := m.ternary()
		if err != nil {
			return a, err
		}
		if !m.eat(":") {
			return a, fmt.Errorf("missing :")
		}
		b, err := m.ternary()
		if err != nil {
			return b, err
		}
		if c.truth() {
			return a, nil
		}
		return b, nil
	}
	return c, nil
}

func boolVal(b bool) mval {
	if b {
		return mval{n: 1}
	}
	return mval{}
}

func (m *molang) or() (mval, error) {
	a, err := m.and()
	for err == nil && m.eat("||") {
		var b mval
		b, err = m.and()
		a = boolVal(a.truth() || b.truth())
	}
	return a, err
}

func (m *molang) and() (mval, error) {
	a, err := m.cmp()
	for err == nil && m.eat("&&") {
		var b mval
		b, err = m.cmp()
		a = boolVal(a.truth() && b.truth())
	}
	return a, err
}

func (m *molang) cmp() (mval, error) {
	a, err := m.add()
	if err != nil {
		return a, err
	}
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if m.eat(op) {
			b, err := m.add()
			if err != nil {
				return b, err
			}
			switch op {
			case "==":
				return boolVal(a.eq(b)), nil
			case "!=":
				return boolVal(!a.eq(b)), nil
			case "<=":
				return boolVal(a.n <= b.n), nil
			case ">=":
				return boolVal(a.n >= b.n), nil
			case "<":
				return boolVal(a.n < b.n), nil
			case ">":
				return boolVal(a.n > b.n), nil
			}
		}
	}
	return a, nil
}

func (m *molang) add() (mval, error) {
	a, err := m.mul()
	for err == nil {
		switch {
		case m.eat("+"):
			var b mval
			b, err = m.mul()
			a = mval{n: a.n + b.n}
		case m.eat("-"):
			var b mval
			b, err = m.mul()
			a = mval{n: a.n - b.n}
		default:
			return a, nil
		}
	}
	return a, err
}

func (m *molang) mul() (mval, error) {
	a, err := m.unary()
	for err == nil {
		switch {
		case m.eat("*"):
			var b mval
			b, err = m.unary()
			a = mval{n: a.n * b.n}
		case m.eat("/"):
			var b mval
			b, err = m.unary()
			if b.n != 0 {
				a = mval{n: a.n / b.n}
			}
		default:
			return a, nil
		}
	}
	return a, err
}

func (m *molang) unary() (mval, error) {
	if m.eat("!") {
		v, err := m.unary()
		return boolVal(!v.truth()), err
	}
	if m.eat("-") {
		v, err := m.unary()
		return mval{n: -v.n}, err
	}
	return m.atom()
}

func (m *molang) atom() (mval, error) {
	m.space()
	if m.pos >= len(m.src) {
		return mval{}, fmt.Errorf("unexpected end")
	}
	c := m.src[m.pos]
	switch {
	case c == '(':
		m.pos++
		v, err := m.ternary()
		if err != nil {
			return v, err
		}
		if !m.eat(")") {
			return v, fmt.Errorf("missing )")
		}
		return v, nil
	case c == '\'' || c == '"':
		end := strings.IndexByte(m.src[m.pos+1:], c)
		if end < 0 {
			return mval{}, fmt.Errorf("unterminated string")
		}
		s := m.src[m.pos+1 : m.pos+1+end]
		m.pos += end + 2
		return mval{s: s, isStr: true}, nil
	case c >= '0' && c <= '9' || c == '.':
		start := m.pos
		for m.pos < len(m.src) && (m.src[m.pos] >= '0' && m.src[m.pos] <= '9' || m.src[m.pos] == '.') {
			m.pos++
		}
		if m.pos < len(m.src) && m.src[m.pos] == 'f' {
			m.pos++
			f, err := strconv.ParseFloat(m.src[start:m.pos-1], 64)
			return mval{n: f}, err
		}
		f, err := strconv.ParseFloat(m.src[start:m.pos], 64)
		return mval{n: f}, err
	}
	start := m.pos
	for m.pos < len(m.src) && (isIdentChar(m.src[m.pos]) || m.src[m.pos] == '.') {
		m.pos++
	}
	name := strings.ToLower(m.src[start:m.pos])
	switch name {
	case "true":
		return mval{n: 1}, nil
	case "false":
		return mval{}, nil
	case "q.block_state", "query.block_state", "q.block_property", "query.block_property":
		if !m.eat("(") {
			return mval{}, fmt.Errorf("%s without (", name)
		}
		arg, err := m.ternary()
		if err != nil {
			return arg, err
		}
		if !m.eat(")") {
			return arg, fmt.Errorf("missing )")
		}
		v, ok := m.states[arg.str()]
		if !ok {
			return mval{}, fmt.Errorf("unknown block state %q", arg.str())
		}
		return toMval(v), nil
	}
	if name == "" {
		return mval{}, fmt.Errorf("unexpected %q", string(c))
	}
	return mval{}, fmt.Errorf("unsupported %q", name)
}

func isIdentChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func toMval(v any) mval {
	switch t := v.(type) {
	case string:
		return mval{s: t, isStr: true}
	case bool:
		return boolVal(t)
	}
	return mval{n: num(v)}
}
