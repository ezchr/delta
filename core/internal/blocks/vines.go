package blocks

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ezchr/go-mcjava/version"
)

// Vines: Bedrock keeps weeping vines, twisting vines, kelp and cave vines as one block each (cave
// vines also split by berries), Java splits each into a tip and a body block (weeping_vines and
// weeping_vines_plant, ...). A block is the body when the block next to it in the growth direction
// is the same vine: below it for weeping and cave vines, above it for twisting vines and kelp.
// The palette maps every state to one of the two (the tip, except cave_vines_body_with_berries);
// vineAlt holds the other one, and the bridge picks between them from the neighbour.
//
// None of the Java tip and body blocks has a waterlogged property (kelp is always in water), so a
// wet state is the dry one.

// vine flags per palette index: the kind (low bits) and two flags.
const (
	vineKindMask = 0x0f
	vineUp       = 1 << 4 // grows up: the body is the block below a vine block
	vineBody     = 1 << 5 // the palette's Java state is the body (vineAlt is the tip)
)

// vineKinds maps the Bedrock vine blocks to their kind, growth direction and Java tip/body names.
var vineKinds = map[string]struct {
	kind      uint8
	up        bool
	tip, body string
}{
	"minecraft:weeping_vines":                {1, false, "minecraft:weeping_vines", "minecraft:weeping_vines_plant"},
	"minecraft:twisting_vines":               {2, true, "minecraft:twisting_vines", "minecraft:twisting_vines_plant"},
	"minecraft:kelp":                         {3, true, "minecraft:kelp", "minecraft:kelp_plant"},
	"minecraft:cave_vines":                   {4, false, "minecraft:cave_vines", "minecraft:cave_vines_plant"},
	"minecraft:cave_vines_head_with_berries": {4, false, "minecraft:cave_vines", "minecraft:cave_vines_plant"},
	"minecraft:cave_vines_body_with_berries": {4, false, "minecraft:cave_vines", "minecraft:cave_vines_plant"},
}

// buildVines fills the vine flags and alternative Java states (after p.java is built).
func (p *Palette) buildVines() {
	n := len(p.States)
	p.vine, p.vineAlt = make([]uint8, n), make([]uint32, n)
	copy(p.vineAlt, p.java)
	for i, s := range p.States {
		k, ok := vineKinds[s.Name]
		if !ok || s.Custom {
			continue
		}
		java := p.java[i]
		b := JavaBlockOf(java)
		if b == nil {
			continue
		}
		var alt uint32
		flags := k.kind
		switch b.Name {
		case k.tip:
			if alt, ok = JavaSwap(java, k.body); !ok {
				continue
			}
		case k.body:
			if alt, ok = JavaSwap(java, k.tip); !ok {
				continue
			}
			// The body has no age: the tip takes the Bedrock block's.
			if age, found := bedrockAge(s.Properties); found {
				alt, _ = JavaBlockNamed(k.tip).With(alt, "age", strconv.Itoa(age))
			}
			flags |= vineBody
		default:
			continue
		}
		if k.up {
			flags |= vineUp
		}
		p.vine[i], p.vineAlt[i] = flags, alt
	}
}

// bedrockAge is the value of a state's *_age property (kelp_age, growing_plant_age...).
func bedrockAge(props map[string]any) (int, bool) {
	for k, v := range props {
		if !strings.HasSuffix(k, "_age") {
			continue
		}
		switch v := v.(type) {
		case int32:
			return int(v), true
		case uint8:
			return int(v), true
		default:
			if a, err := strconv.Atoi(fmt.Sprint(v)); err == nil {
				return a, true
			}
		}
	}
	return 0, false
}

// IsVine reports whether index i is a vine Java splits into a tip and a body.
func (p *Palette) IsVine(i uint32) bool { return int(i) < len(p.vine) && p.vine[i] != 0 }

// VineUp reports whether vine i grows up (its body form has the same vine above it), not down.
func (p *Palette) VineUp(i uint32) bool { return int(i) < len(p.vine) && p.vine[i]&vineUp != 0 }

// VineBody reports whether the palette's Java state of vine i is its body form.
func (p *Palette) VineBody(i uint32) bool { return int(i) < len(p.vine) && p.vine[i]&vineBody != 0 }

// SameVine reports whether index o is the same vine as vine i (any cave vines count as one).
func (p *Palette) SameVine(i, o uint32) bool {
	return int(o) < len(p.vine) && p.vine[o] != 0 && p.vine[o]&vineKindMask == p.vine[i]&vineKindMask
}

// VineAltFor is the table of the other Java form (body for a tip, tip for a body) of every vine
// index for a client version; other indices hold their usual state.
func (p *Palette) VineAltFor(v *version.Version) []uint32 {
	if v == nil || v.Native() {
		return p.vineAlt
	}
	return p.verTables(v)[2]
}
