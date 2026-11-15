package blocks

import (
	"sort"
	"strings"
	"sync"
)

// Java block states by name and properties (Java 26.3 numbering), for the states a Bedrock
// block's state does not give: a bed's colour, a banner's base colour, a skull's rotation, a
// flower pot's plant... come from the block entity, and the bridge changes the state the table
// gave (javastates_gen.go, from tools/genjavastates).

type javaBlockDef struct {
	name       string
	first, def uint32
	props      string
}

// JavaBlock is one Java block's states.
type JavaBlock struct {
	Name           string
	First, Default uint32
	Props          []JavaProp
	n              uint32
}

// JavaProp is a property of a Java block; Stride is how far apart its values' states are.
type JavaProp struct {
	Name   string
	Values []string
	Stride uint32
}

var (
	javaOnce   sync.Once
	javaBlocks []*JavaBlock
	javaByName map[string]*JavaBlock
)

func loadJavaBlocks() {
	javaBlocks = make([]*JavaBlock, 0, len(javaBlockDefs))
	javaByName = make(map[string]*JavaBlock, len(javaBlockDefs))
	for _, d := range javaBlockDefs {
		b := &JavaBlock{Name: d.name, First: d.first, Default: d.def, n: 1}
		if d.props != "" {
			for _, p := range strings.Split(d.props, ";") {
				name, vals, _ := strings.Cut(p, "=")
				b.Props = append(b.Props, JavaProp{Name: name, Values: strings.Split(vals, ",")})
			}
		}
		for i := len(b.Props) - 1; i >= 0; i-- {
			b.Props[i].Stride = b.n
			b.n *= uint32(len(b.Props[i].Values))
		}
		javaBlocks = append(javaBlocks, b)
		javaByName[b.Name] = b
	}
}

// JavaBlockOf returns the Java block a 26.3 state belongs to (nil: none).
func JavaBlockOf(state uint32) *JavaBlock {
	javaOnce.Do(loadJavaBlocks)
	i := sort.Search(len(javaBlocks), func(i int) bool { return javaBlocks[i].First > state }) - 1
	if i < 0 || state >= javaBlocks[i].First+javaBlocks[i].n {
		return nil
	}
	return javaBlocks[i]
}

// JavaBlockNamed returns a Java block by name ("minecraft:red_bed"), nil if there is none.
func JavaBlockNamed(name string) *JavaBlock {
	javaOnce.Do(loadJavaBlocks)
	return javaByName[name]
}

func (b *JavaBlock) prop(name string) *JavaProp {
	for i := range b.Props {
		if b.Props[i].Name == name {
			return &b.Props[i]
		}
	}
	return nil
}

// Get returns the value of property name in state (a state of b).
func (b *JavaBlock) Get(state uint32, name string) (string, bool) {
	p := b.prop(name)
	if p == nil || state < b.First {
		return "", false
	}
	return p.Values[(state-b.First)/p.Stride%uint32(len(p.Values))], true
}

// With returns state (a state of b) with property name set to value; ok is false (and state is
// returned) when b has no such property or value.
func (b *JavaBlock) With(state uint32, name, value string) (uint32, bool) {
	p := b.prop(name)
	if p == nil || state < b.First {
		return state, false
	}
	for i, v := range p.Values {
		if v == value {
			cur := (state - b.First) / p.Stride % uint32(len(p.Values))
			return state - cur*p.Stride + uint32(i)*p.Stride, true
		}
	}
	return state, false
}

// Has reports whether b has property name.
func (b *JavaBlock) Has(name string) bool { return b.prop(name) != nil }

// JavaSwap returns the state of the Java block name with the property values of state (another
// block's state): a red bed's state as a blue bed's. Properties name lacks are dropped, ones
// state lacks keep name's default.
func JavaSwap(state uint32, name string) (uint32, bool) {
	from, to := JavaBlockOf(state), JavaBlockNamed(name)
	if from == nil || to == nil {
		return state, false
	}
	out := to.Default
	for _, p := range from.Props {
		if v, ok := from.Get(state, p.Name); ok {
			out, _ = to.With(out, p.Name, v)
		}
	}
	return out, true
}
