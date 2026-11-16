package javapack

import (
	"errors"
	"strconv"
	"strings"
)

// JSON UI binding expressions, the part layouts use to cut a form's text apart and to decide what
// shows:
//
//	'%.16s' * #form_text        the first 16 bytes
//	a - b                       a without b (how a prefix is cut off, and how "holds b" is tested:
//	                            (a - b) = a is false when a holds b)
//	a + b                       joined
//	a = b                       equal
//	not x, x and y, x or y
//
// Operators have no precedence here: layouts bracket everything, and what is left is read left
// to right. A variable the caller does not give makes the whole expression unknown (errUnknown).

var errUnknown = errors.New("unknown variable")

type uiValue struct {
	s      string
	b      bool
	isBool bool
}

func (v uiValue) truth() bool {
	if v.isBool {
		return v.b
	}
	return v.s != "" && v.s != "false"
}

func (v uiValue) str() string {
	if v.isBool {
		return strconv.FormatBool(v.b)
	}
	return v.s
}

type uiParser struct {
	src  string
	pos  int
	vars map[string]string
}

// evalUI evaluates a binding expression with the given variables ("#form_text" -> its value).
func evalUI(expr string, vars map[string]string) (uiValue, error) {
	p := &uiParser{src: expr, vars: vars}
	v, err := p.expr()
	if err != nil {
		return uiValue{}, err
	}
	p.space()
	if p.pos < len(p.src) {
		return uiValue{}, errors.New("unexpected " + p.src[p.pos:])
	}
	return v, nil
}

func (p *uiParser) space() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
		p.pos++
	}
}

func (p *uiParser) word() string {
	p.space()
	i := p.pos
	for i < len(p.src) && (p.src[i] >= 'a' && p.src[i] <= 'z') {
		i++
	}
	return p.src[p.pos:i]
}

func (p *uiParser) expr() (uiValue, error) {
	left, err := p.unary()
	if err != nil {
		return left, err
	}
	for {
		p.space()
		if p.pos >= len(p.src) || p.src[p.pos] == ')' {
			return left, nil
		}
		op := ""
		switch c := p.src[p.pos]; c {
		case '+', '-', '*', '=':
			op = string(c)
			p.pos++
		default:
			op = p.word()
			if op != "and" && op != "or" {
				return left, errors.New("unexpected " + p.src[p.pos:])
			}
			p.pos += len(op)
		}
		right, err := p.unary()
		if err != nil {
			return left, err
		}
		switch op {
		case "+":
			left = uiValue{s: left.str() + right.str()}
		case "-":
			left = uiValue{s: strings.Replace(left.str(), right.str(), "", 1)}
		case "*":
			left = uiValue{s: formatPrefix(left.str(), right.str())}
		case "=":
			left = uiValue{b: left.str() == right.str(), isBool: true}
		case "and":
			left = uiValue{b: left.truth() && right.truth(), isBool: true}
		case "or":
			left = uiValue{b: left.truth() || right.truth(), isBool: true}
		}
	}
}

// formatPrefix is '%.Ns' * s: the first N bytes of s. Any other format gives s.
func formatPrefix(format, s string) string {
	if strings.HasPrefix(format, "%.") && strings.HasSuffix(format, "s") {
		if n, err := strconv.Atoi(format[2 : len(format)-1]); err == nil && n >= 0 {
			if n < len(s) {
				return s[:n]
			}
			return s
		}
	}
	return s
}

func (p *uiParser) unary() (uiValue, error) {
	p.space()
	if p.pos >= len(p.src) {
		return uiValue{}, errors.New("unexpected end")
	}
	switch c := p.src[p.pos]; {
	case c == '(':
		p.pos++
		v, err := p.expr()
		if err != nil {
			return v, err
		}
		p.space()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return v, errors.New("missing )")
		}
		p.pos++
		return v, nil
	case c == '\'':
		end := strings.IndexByte(p.src[p.pos+1:], '\'')
		if end < 0 {
			return uiValue{}, errors.New("unclosed string")
		}
		s := p.src[p.pos+1 : p.pos+1+end]
		p.pos += end + 2
		return uiValue{s: s}, nil
	case c == '#' || c == '$':
		i := p.pos + 1
		for i < len(p.src) && (p.src[i] == '_' || p.src[i] >= 'a' && p.src[i] <= 'z' || p.src[i] >= 'A' && p.src[i] <= 'Z' || p.src[i] >= '0' && p.src[i] <= '9') {
			i++
		}
		name := p.src[p.pos:i]
		p.pos = i
		v, ok := p.vars[name]
		if !ok {
			return uiValue{}, errUnknown
		}
		return uiValue{s: v}, nil
	case c >= '0' && c <= '9':
		i := p.pos
		for i < len(p.src) && (p.src[i] >= '0' && p.src[i] <= '9' || p.src[i] == '.') {
			i++
		}
		s := p.src[p.pos:i]
		p.pos = i
		return uiValue{s: s}, nil
	}
	if w := p.word(); w == "not" {
		p.pos += len(w)
		v, err := p.unary()
		return uiValue{b: !v.truth(), isBool: true}, err
	} else if w == "true" || w == "false" {
		p.pos += len(w)
		return uiValue{b: w == "true", isBool: true}, nil
	}
	return uiValue{}, errors.New("unexpected " + p.src[p.pos:])
}
