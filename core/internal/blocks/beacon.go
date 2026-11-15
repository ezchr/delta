// Block palette helpers copied from the original bridge's palette.go:
// the network block hash, custom block states and their traits.

package blocks

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// networkBlockHash is the client's network block hash of a state: FNV-1a (32 bit) of the state as
// little-endian NBT with its properties sorted, as Dragonfly's world.networkBlockHash.
func networkBlockHash(name string, properties map[string]any) uint32 {
	if name == "minecraft:unknown" {
		return 0xfffffffe
	}
	keys := make([]string, 0, len(properties))
	for k := range properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var data []byte
	str := func(s string) {
		data = append(data, byte(len(s)), byte(len(s)>>8))
		data = append(data, s...)
	}
	data = append(data, 10, 0, 0, 8)
	str("name")
	str(name)
	data = append(data, 10)
	str("states")
	for _, k := range keys {
		switch v := properties[k].(type) {
		case string:
			data = append(data, 8)
			str(k)
			str(v)
		case uint8:
			data = append(data, 1)
			str(k)
			data = append(data, v)
		case int8:
			data = append(data, 1)
			str(k)
			data = append(data, byte(v))
		case bool:
			data = append(data, 1)
			str(k)
			if v {
				data = append(data, 1)
			} else {
				data = append(data, 0)
			}
		case int16:
			data = append(data, 2)
			str(k)
			data = append(data, byte(v), byte(v>>8))
		case uint16:
			data = append(data, 2)
			str(k)
			data = append(data, byte(v), byte(v>>8))
		case int32:
			data = append(data, 3)
			str(k)
			data = append(data, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		case uint32:
			data = append(data, 3)
			str(k)
			data = append(data, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		}
	}
	data = append(data, 0, 0)
	h := uint32(2166136261)
	for _, b := range data {
		h ^= uint32(b)
		h *= 16777619
	}
	return h
}
// customBlockStates lists a custom block's states in the order the client registers them: every
// combination of its properties' values, the FIRST property changing fastest (as Geyser generates
// them, matching the client). Property values come as whatever NBT list type the server used
// blockProp is one block state and its values, in the order the client lists them.
type blockProp struct {
	name   string
	values []any
}

// traitStates are the states each block trait can enable, in the order the client adds them.
var traitStates = map[string][]blockProp{
	"minecraft:placement_position": {
		{"minecraft:block_face", []any{"down", "up", "north", "south", "west", "east"}},
		{"minecraft:vertical_half", []any{"bottom", "top"}},
	},
	"minecraft:placement_direction": {
		{"minecraft:cardinal_direction", []any{"south", "west", "north", "east"}},
		{"minecraft:facing_direction", []any{"down", "up", "north", "south", "west", "east"}},
	},
}

// traitProps lists the states a custom block's traits enable ("traits" in its StartGame entry).
func traitProps(b protocol.BlockEntry) []blockProp {
	var out []blockProp
	for _, t := range anySlice(b.Properties["traits"]) {
		m, _ := t.(map[string]any)
		name, _ := m["name"].(string)
		enabled, _ := m["enabled_states"].(map[string]any)
		for _, st := range traitStates[name] {
			short := st.name[len("minecraft:"):]
			if on, ok := enabled[short]; ok && fmt.Sprint(on) != "0" && fmt.Sprint(on) != "false" {
				out = append(out, st)
			}
		}
	}
	return out
}

// anySlice returns the elements of any slice (an NBT list decodes to []any, []int32, []string...).
func anySlice(v any) []any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out
}

// fnv164 is the FNV-1 64-bit hash (multiply, then xor) the palette is sorted by.
func fnv164(s string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(s); i++ {
		h *= 1099511628211
		h ^= uint64(s[i])
	}
	return h
}

