// Package blocks is the Bedrock server's block palette as the original bridge uses it: every state the server
// numbers blocks by (vanilla 1.26.60 states plus the custom blocks StartGame announced), the Java
// state each one is shown as, and the light/collision data chunk lighting and spawn search need.
//
// Chunks are decoded straight into palette indices (see Registry): the index is the position in
// the client's sorted state list, which is also the server's runtime id unless the server numbers
// blocks by network hashes, in which case ServerToIndex translates.
package blocks

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"javabridge/core/internal/javamap"

	"github.com/df-mc/dragonfly/server/java/protocol/version"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// vanillaBlockStates is every vanilla block state of protocol 1.26.60 in order, as network NBT
// (Dragonfly's block_states.nbt). It must be updated with the Bedrock protocol version.
//
//go:embed block_states.nbt
var vanillaBlockStates []byte

// State is one block state of the palette.
type State struct {
	Name       string
	Properties map[string]any
	Hash       uint32 // network block hash
	Custom     bool   // a custom (non-vanilla) block of the server
}

var vanilla = sync.OnceValue(func() []State {
	dec := nbt.NewDecoder(bytes.NewBuffer(vanillaBlockStates))
	var out []State
	for {
		var s struct {
			Name       string         `nbt:"name"`
			Properties map[string]any `nbt:"states"`
			Version    int32          `nbt:"version"`
		}
		if err := dec.Decode(&s); err != nil {
			break
		}
		out = append(out, State{Name: s.Name, Properties: s.Properties, Hash: networkBlockHash(s.Name, s.Properties)})
	}
	return out
})

// CustomStandIn is the Bedrock block a server's custom block is shown as (Java has none).
const CustomStandIn = "minecraft:smooth_stone"

// Palette is the numbering of one server.
type Palette struct {
	States []State
	Hashed bool // the server numbers blocks by network hash (UseBlockNetworkIDHashes)
	Custom int  // custom block names announced
	// Unmapped is how many vanilla states have no Java mapping (shown as stone).
	Unmapped int
	// unknownIDs counts runtime ids the server sent that are not in the palette (shown as air).
	unknownIDs atomic.Int64

	byHash map[uint32]uint32 // hash -> index (always built: block updates may name hashes)
	Air    uint32            // index of minecraft:air

	// Per index: the Java 26.3 state, the same with water in it, and the block data.
	java, wet []uint32
	data      []uint16
	isAir     []bool
	water     []bool // a water state (Bedrock keeps waterlogging as water in layer 1)
	be        []string
	beState   []bool   // the Java state takes something from the block entity
	door      []bool   // a door half (Bedrock splits a door's state between its halves)
	vine      []uint8  // a vine Java splits into tip and body (vines.go)
	vineAlt   []uint32 // the vine's other Java form

	verMu sync.Mutex
	ver   map[*version.Version][3][]uint32 // java, wet, vineAlt
}

// NewPalette builds the palette a client builds from StartGame's custom block list.
func NewPalette(custom []protocol.BlockEntry, hashed bool) (*Palette, error) {
	van := vanilla()
	if len(van) == 0 {
		return nil, fmt.Errorf("no vanilla block states")
	}
	names := make(map[string]bool, 2048)
	for _, s := range van {
		names[s.Name] = true
	}
	states := make([]State, len(van), len(van)+64)
	copy(states, van)
	p := &Palette{Hashed: hashed}
	for _, b := range custom {
		if names[b.Name] {
			continue // a vanilla data-driven block: its states are already listed
		}
		for _, props := range customStateMaps(b) {
			states = append(states, State{Name: b.Name, Properties: props, Hash: networkBlockHash(b.Name, props), Custom: true})
		}
		p.Custom++
	}
	sort.SliceStable(states, func(i, j int) bool { return fnv164(states[i].Name) < fnv164(states[j].Name) })
	p.States = states
	p.build()
	return p, nil
}

func (p *Palette) build() {
	n := len(p.States)
	p.byHash = make(map[uint32]uint32, n)
	p.java, p.wet = make([]uint32, n), make([]uint32, n)
	p.data = make([]uint16, n)
	p.isAir, p.water = make([]bool, n), make([]bool, n)
	p.be = make([]string, n)
	p.beState = make([]bool, n)
	p.door = make([]bool, n)
	standIn, _ := javamap.NameDefault(CustomStandIn)
	for i, s := range p.States {
		if _, dup := p.byHash[s.Hash]; !dup {
			p.byHash[s.Hash] = uint32(i)
		}
		if s.Name == "minecraft:air" && len(s.Properties) == 0 {
			p.Air = uint32(i)
		}
		java, ok := javamap.LookupHash(s.Hash)
		if !ok {
			java, ok = javamap.NameDefault(s.Name)
		}
		if !ok {
			java = javamap.JavaStone
			if !s.Custom {
				p.Unmapped++
			}
		}
		d, known := lookupData(s.Hash)
		if s.Custom {
			java = standIn
			d, known = 15|1<<9|1<<10, true
		}
		if !known {
			d = 15 | 1<<9 | 1<<10
		}
		p.java[i] = uint32(java)
		p.wet[i] = uint32(java)
		if javamap.Waterloggable(java) {
			p.wet[i] = uint32(javamap.Waterlogged(java))
		}
		p.data[i] = d
		switch s.Name {
		case "minecraft:air", "minecraft:structure_void", "minecraft:light_block":
			p.isAir[i] = true
		case "minecraft:water", "minecraft:flowing_water":
			p.water[i] = true
		}
		if strings.HasPrefix(s.Name, "minecraft:light_block") {
			p.isAir[i] = true
		}
		p.be[i] = BlockEntityType(s.Name)
		p.beState[i] = !s.Custom && BlockEntityState(s.Name)
		_, upper := s.Properties["upper_block_bit"]
		p.door[i] = upper && strings.HasSuffix(s.Name, "door") && !strings.HasSuffix(s.Name, "trapdoor")
	}
	p.buildVines()
}

// UnknownIDs is how many runtime ids not in the palette were looked up so far.
func (p *Palette) UnknownIDs() int64 { return p.unknownIDs.Load() }

// IsDoor reports whether index i is a door half.
func (p *Palette) IsDoor(i uint32) bool { return int(i) < len(p.door) && p.door[i] }

// Lookup is the index of the state name[props], if the palette has it.
func (p *Palette) Lookup(name string, props map[string]any) (uint32, bool) {
	i, ok := p.byHash[networkBlockHash(name, props)]
	return i, ok
}

// ServerToIndex turns a runtime id the server sent into a palette index (air when unknown).
func (p *Palette) ServerToIndex(id uint32) uint32 {
	if p.Hashed {
		if i, ok := p.byHash[id]; ok {
			return i
		}
		p.unknownIDs.Add(1)
		return p.Air
	}
	if int(id) < len(p.States) {
		return id
	}
	p.unknownIDs.Add(1)
	return p.Air
}

// IndexToServer turns a palette index into the server's runtime id.
func (p *Palette) IndexToServer(i uint32) uint32 {
	if int(i) >= len(p.States) {
		i = p.Air
	}
	if p.Hashed {
		return p.States[i].Hash
	}
	return i
}

// Java is the Java 26.3 state of index i; wet with water in its second layer.
func (p *Palette) Java(i uint32, wet bool) uint32 {
	if int(i) >= len(p.java) {
		return 0
	}
	if wet {
		return p.wet[i]
	}
	return p.java[i]
}

// JavaFor returns the state tables for a client version (26.3 tables remapped for older ones).
func (p *Palette) JavaFor(v *version.Version) (java, wet []uint32) {
	if v == nil || v.Native() {
		return p.java, p.wet
	}
	t := p.verTables(v)
	return t[0], t[1]
}

// verTables are the java, wet and vineAlt tables remapped for an older client version.
func (p *Palette) verTables(v *version.Version) [3][]uint32 {
	p.verMu.Lock()
	defer p.verMu.Unlock()
	if t, ok := p.ver[v]; ok {
		return t
	}
	j, w, a := make([]uint32, len(p.java)), make([]uint32, len(p.wet)), make([]uint32, len(p.vineAlt))
	for i := range p.java {
		j[i] = uint32(v.BlockState(int32(p.java[i])))
		w[i] = uint32(v.BlockState(int32(p.wet[i])))
		a[i] = uint32(v.BlockState(int32(p.vineAlt[i])))
	}
	if p.ver == nil {
		p.ver = map[*version.Version][3][]uint32{}
	}
	t := [3][]uint32{j, w, a}
	p.ver[v] = t
	return t
}

// IsAir reports whether index i counts as air on Java (not counted in a section's block count).
func (p *Palette) IsAir(i uint32) bool { return int(i) >= len(p.isAir) || p.isAir[i] }

// IsWater reports whether index i is water (layer 1 water makes the layer 0 block waterlogged).
func (p *Palette) IsWater(i uint32) bool { return int(i) < len(p.water) && p.water[i] }

// Liquid reports whether index i is a liquid.
func (p *Palette) Liquid(i uint32) bool { return int(i) < len(p.data) && p.data[i]&(1<<8) != 0 }

// Solid reports whether index i has a collision box.
func (p *Palette) Solid(i uint32) bool { return int(i) < len(p.data) && p.data[i]&(1<<9) != 0 }

// Filter is the light index i filters (0-15).
func (p *Palette) Filter(i uint32) uint8 {
	if int(i) >= len(p.data) {
		return 15
	}
	return uint8(p.data[i] & 15)
}

// Emits is the light index i emits (0-15).
func (p *Palette) Emits(i uint32) uint8 {
	if int(i) >= len(p.data) {
		return 0
	}
	return uint8(p.data[i] >> 4 & 15)
}

// BlockEntity is the Java block entity type drawing index i ("" for none).
func (p *Palette) BlockEntity(i uint32) string {
	if int(i) >= len(p.be) {
		return ""
	}
	return p.be[i]
}

// Name is the Bedrock name of index i.
func (p *Palette) Name(i uint32) string {
	if int(i) >= len(p.States) {
		return "minecraft:air"
	}
	return p.States[i].Name
}

// lookupData finds the generated data of a vanilla state by hash.
func lookupData(h uint32) (uint16, bool) {
	key := uint64(h) << 16
	i := sort.Search(len(stateData), func(i int) bool { return stateData[i] >= key })
	if i < len(stateData) && stateData[i]>>16 == uint64(h) {
		return uint16(stateData[i]), true
	}
	return 0, false
}

// customStateMaps lists a custom block's states as property maps, in the order the client
// registers them: every combination of its properties' values (and its traits' states), the first
// property changing fastest.
func customStateMaps(b protocol.BlockEntry) []map[string]any {
	var ps []blockProp
	props, _ := b.Properties["properties"].([]any)
	for _, p := range props {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		values := anySlice(m["enum"])
		if name == "" || len(values) == 0 {
			continue
		}
		ps = append(ps, blockProp{name, values})
	}
	ps = append(ps, traitProps(b)...)
	total := 1
	for _, p := range ps {
		total *= len(p.values)
	}
	if total > 1<<16 {
		total = 1 << 16
	}
	out := make([]map[string]any, 0, total)
	for i := 0; i < total; i++ {
		cur := make(map[string]any, len(ps))
		idx := i
		for _, p := range ps {
			cur[p.name] = p.values[idx%len(p.values)]
			idx /= len(p.values)
		}
		out = append(out, cur)
	}
	return out
}

// javaBlockEntityTypes maps a Bedrock block name to the Java block entity type that draws it
// (javasession's block_entities.go): a Java client draws these blocks only through a block entity,
// which a chunk packet has to list. Suffixes (leading "*") cover the coloured and wooden families.
var javaBlockEntityTypes = []struct{ bedrock, java string }{
	{"*shulker_box", "minecraft:shulker_box"},
	{"minecraft:chest", "minecraft:chest"},
	{"minecraft:trapped_chest", "minecraft:trapped_chest"},
	{"minecraft:ender_chest", "minecraft:ender_chest"},
	{"*_chest", "minecraft:chest"}, // copper chests
	{"*_hanging_sign", "minecraft:hanging_sign"},
	{"*_sign", "minecraft:sign"},
	{"*_skull", "minecraft:skull"},
	{"*_head", "minecraft:skull"},
	{"minecraft:skull", "minecraft:skull"},
	{"*_banner", "minecraft:banner"},
	{"minecraft:bed", "minecraft:bed"},
	{"minecraft:bell", "minecraft:bell"},
	{"minecraft:enchanting_table", "minecraft:enchanting_table"},
	{"minecraft:decorated_pot", "minecraft:decorated_pot"},
	{"minecraft:conduit", "minecraft:conduit"},
	{"minecraft:end_portal", "minecraft:end_portal"},
	{"minecraft:end_gateway", "minecraft:end_gateway"},
	{"minecraft:lectern", "minecraft:lectern"},
	{"minecraft:mob_spawner", "minecraft:mob_spawner"},
	{"minecraft:trial_spawner", "minecraft:trial_spawner"},
	{"minecraft:vault", "minecraft:vault"},
	{"*copper_golem_statue", "minecraft:copper_golem_statue"},
	// Drawn from block entity data (items, the beam) rather than by it, still only with one.
	{"minecraft:beacon", "minecraft:beacon"},
	{"minecraft:campfire", "minecraft:campfire"},
	{"minecraft:soul_campfire", "minecraft:campfire"},
	{"minecraft:suspicious_sand", "minecraft:brushable_block"},
	{"minecraft:suspicious_gravel", "minecraft:brushable_block"},
	{"minecraft:jukebox", "minecraft:jukebox"},
	{"minecraft:chiseled_bookshelf", "minecraft:chiseled_bookshelf"},
	{"*_shelf", "minecraft:shelf"},
	{"*command_block", "minecraft:command_block"}, // the edit screen needs one (bridge/cmdblock.go)
}

// BlockEntityState reports whether the Java state of a Bedrock block takes something from its
// block entity (javamap's SourceBlockEntity dependencies): bed colour, banner base colour, skull
// rotation, shulker box facing, flower pot plant, lectern book, jukebox record, note block
// pitch, piston extension.
func BlockEntityState(bedrock string) bool {
	switch strings.TrimPrefix(bedrock, "minecraft:") {
	case "bed", "standing_banner", "wall_banner", "flower_pot", "lectern", "jukebox", "noteblock",
		"piston", "sticky_piston", "skull":
		return true
	}
	return strings.HasSuffix(bedrock, "shulker_box") || strings.HasSuffix(bedrock, "_skull") || strings.HasSuffix(bedrock, "_head")
}

// BlockEntityState reports whether index i's Java state takes something from its block entity.
func (p *Palette) BlockEntityState(i uint32) bool { return int(i) < len(p.beState) && p.beState[i] }

// BlockEntityType returns the Java block entity type for a Bedrock block name, or "".
func BlockEntityType(bedrock string) string {
	for _, t := range javaBlockEntityTypes {
		if suffix, ok := strings.CutPrefix(t.bedrock, "*"); ok {
			if strings.HasSuffix(bedrock, suffix) {
				return t.java
			}
		} else if bedrock == t.bedrock {
			return t.java
		}
	}
	return ""
}

// FullSolid reports whether index i has a collision box reaching the top of the block (a player
// cannot stand inside it).
func (p *Palette) FullSolid(i uint32) bool { return int(i) < len(p.data) && p.data[i]&(1<<10) != 0 }
