package main

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"delta/core/internal/blocks"

	jchunk "github.com/ezchr/go-mcjava/chunk"
	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

// Block entity and scoreboard checks: the bot logs block_entity_data (BE DATA), block events
// (BLOCK EVENT) and the scoreboard packets (OBJECTIVE, DISPLAY, SCORE, RESET SCORE) as a Java
// client reads them; step "bes X1 Y1 Z1 X2 Y2 Z2" lists the block entities of the columns it
// has in that box with their data and the block state there by name (BE lines), "blockname X Y Z"
// prints a block's Java state by name, "scores" prints the sidebar/list/below-name as a client
// would show them.

var beTypes = []string{"banner", "barrel", "beacon", "beehive", "bell", "blast_furnace", "brewing_stand",
	"brushable_block", "calibrated_sculk_sensor", "campfire", "chest", "chiseled_bookshelf", "command_block",
	"comparator", "conduit", "copper_golem_statue", "crafter", "creaking_heart", "daylight_detector",
	"decorated_pot", "dispenser", "dropper", "enchanting_table", "end_gateway", "end_portal", "ender_chest",
	"furnace", "hanging_sign", "hopper", "jigsaw", "jukebox", "lectern", "mob_spawner", "piston",
	"potent_sulfur", "sculk_catalyst", "sculk_sensor", "sculk_shrieker", "shelf", "shulker_box", "sign",
	"skull", "smoker", "structure_block", "test_block", "test_instance_block", "trapped_chest",
	"trial_spawner", "vault"}

func beTypeName(id int32) string {
	for _, n := range beTypes {
		if v777.BuiltinID("minecraft:block_entity_type", "minecraft:"+n) == id {
			return n
		}
	}
	return "#" + strconv.Itoa(int(id))
}

// stateName is a 26.3 Java state as name[prop=value,...].
func stateName(st uint32) string {
	jb := blocks.JavaBlockOf(st)
	if jb == nil {
		return "?" + strconv.Itoa(int(st))
	}
	var props []string
	for _, p := range jb.Props {
		v, _ := jb.Get(st, p.Name)
		props = append(props, p.Name+"="+v)
	}
	n := strings.TrimPrefix(jb.Name, "minecraft:")
	if len(props) > 0 {
		n += "[" + strings.Join(props, ",") + "]"
	}
	return n
}

// readNBT reads a network NBT value (type byte, nameless root) as SNBT-like text.
func readNBT(r *wire.Reader) string {
	t := r.Byte()
	if t == 0 {
		return "{}"
	}
	return nbtPayload(r, t, 0)
}

func nbtPayload(r *wire.Reader, t byte, depth int) string {
	if depth > 64 || r.Err != nil {
		return "!"
	}
	switch t {
	case text.TagByte:
		return strconv.Itoa(int(int8(r.Byte()))) + "b"
	case text.TagShort:
		return strconv.Itoa(int(r.Int16())) + "s"
	case text.TagInt:
		return strconv.Itoa(int(r.Int32()))
	case text.TagLong:
		return strconv.FormatInt(r.Int64(), 10) + "L"
	case text.TagFloat:
		return strconv.FormatFloat(float64(math.Float32frombits(uint32(r.Int32()))), 'g', -1, 32) + "f"
	case text.TagDouble:
		return strconv.FormatFloat(math.Float64frombits(uint64(r.Int64())), 'g', -1, 64) + "d"
	case 7: // byte array
		n := int(r.Int32())
		for i := 0; i < n && r.Err == nil; i++ {
			r.Byte()
		}
		return fmt.Sprintf("[B;%d]", n)
	case text.TagString:
		return strconv.Quote(text.ReadString(r))
	case text.TagList:
		et := r.Byte()
		n := int(r.Int32())
		var parts []string
		for i := 0; i < n && r.Err == nil; i++ {
			parts = append(parts, nbtPayload(r, et, depth+1))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case text.TagCompound:
		var parts []string
		for r.Err == nil {
			et := r.Byte()
			if et == 0 {
				break
			}
			k := text.ReadString(r)
			parts = append(parts, k+":"+nbtPayload(r, et, depth+1))
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ",") + "}"
	case 11:
		n := int(r.Int32())
		for i := 0; i < n && r.Err == nil; i++ {
			r.Int32()
		}
		return fmt.Sprintf("[I;%d]", n)
	case 12:
		n := int(r.Int32())
		for i := 0; i < n && r.Err == nil; i++ {
			r.Int64()
		}
		return fmt.Sprintf("[L;%d]", n)
	}
	r.Err = fmt.Errorf("nbt tag %d", t)
	return "!"
}

type sbScore struct {
	value  int32
	shown  string
	format string
}

// sbState is the scoreboard as the bot has it.
var sbState = struct {
	objectives map[string]string             // name -> display text
	display    map[int32]string              // slot -> objective
	scores     map[string]map[string]sbScore // objective -> holder -> score
}{map[string]string{}, map[int32]string{}, map[string]map[string]sbScore{}}

func readNumberFormat(r *wire.Reader) string {
	if !r.Bool() {
		return "default"
	}
	switch t := r.VarInt(); t {
	case 0:
		return "blank"
	case 1:
		return "styled " + readNBT(r)
	case 2:
		return "fixed " + readNBT(r)
	default:
		return "format " + strconv.Itoa(int(t))
	}
}

func init() {
	extraHandlers = append(extraHandlers, func(b *bot, id int32, r *wire.Reader) bool {
		switch id {
		case v777.ClientboundPlayBlockEntityData:
			x, y, z := r.Position()
			t := r.VarInt()
			log.Printf("BE DATA %d %d %d %s %s", x, y, z, beTypeName(t), readNBT(r))
		case v777.ClientboundPlayBlockEvent:
			x, y, z := r.Position()
			a, p := r.Byte(), r.Byte()
			log.Printf("BLOCK EVENT %d %d %d action %d param %d block %d", x, y, z, a, p, r.VarInt())
		case v777.ClientboundPlaySetObjective:
			name := r.String(32767)
			mode := r.Byte()
			if mode == 1 {
				delete(sbState.objectives, name)
				delete(sbState.scores, name)
				log.Printf("OBJECTIVE %s removed", name)
				return true
			}
			disp := readNBT(r)
			rt := r.VarInt()
			nf := readNumberFormat(r)
			sbState.objectives[name] = disp
			log.Printf("OBJECTIVE %s mode %d %s render %d format %s", name, mode, disp, rt, nf)
		case v777.ClientboundPlaySetDisplayObjective:
			slot := r.VarInt()
			name := r.String(32767)
			sbState.display[slot] = name
			log.Printf("DISPLAY slot %d = %q", slot, name)
		case v777.ClientboundPlaySetScore:
			holder, obj, val := r.String(32767), r.String(32767), r.VarInt()
			shown := ""
			if r.Bool() {
				shown = readNBT(r)
			}
			nf := readNumberFormat(r)
			if sbState.scores[obj] == nil {
				sbState.scores[obj] = map[string]sbScore{}
			}
			sbState.scores[obj][holder] = sbScore{val, shown, nf}
			log.Printf("SCORE %s %s = %d shown %s format %s", obj, holder, val, shown, nf)
		case v777.ClientboundPlayResetScore:
			holder := r.String(32767)
			obj := ""
			if r.Bool() {
				obj = r.String(32767)
			}
			delete(sbState.scores[obj], holder)
			log.Printf("RESET SCORE %s %s", obj, holder)
		default:
			return false
		}
		return true
	})
	extraSteps["bes"] = func(b *bot, a []string) time.Duration {
		if len(a) < 7 {
			return 0
		}
		v := make([]int, 6)
		for i := range v {
			v[i], _ = strconv.Atoi(a[i+1])
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		n := 0
		for key, col := range b.chunks {
			for _, be := range col.BlockEntities {
				x, z := int(key[0])*16+int(be.XZ>>4), int(key[1])*16+int(be.XZ&15)
				y := int(be.Y)
				if x < min(v[0], v[3]) || x > max(v[0], v[3]) || y < min(v[1], v[4]) || y > max(v[1], v[4]) || z < min(v[2], v[5]) || z > max(v[2], v[5]) {
					continue
				}
				data := "-"
				if be.NBT != nil {
					data = readNBT(wire.NewReader(be.NBT))
				}
				log.Printf("BE %d %d %d %s %s state %s", x, y, z, beTypeName(be.Type), data, stateName(b.stateAt(x, y, z)))
				n++
			}
		}
		log.Printf("BES %d", n)
		return 0
	}
	extraSteps["blockname"] = func(b *bot, a []string) time.Duration {
		if len(a) < 4 {
			return 0
		}
		x, _ := strconv.Atoi(a[1])
		y, _ := strconv.Atoi(a[2])
		z, _ := strconv.Atoi(a[3])
		b.mu.Lock()
		defer b.mu.Unlock()
		log.Printf("BLOCKNAME %d %d %d = %s", x, y, z, stateName(b.stateAt(x, y, z)))
		return 0
	}
	extraSteps["scores"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		defer b.mu.Unlock()
		for slot, obj := range sbState.display {
			if obj == "" {
				continue
			}
			type line struct {
				holder string
				s      sbScore
			}
			var lines []line
			for h, s := range sbState.scores[obj] {
				lines = append(lines, line{h, s})
			}
			sort.Slice(lines, func(i, j int) bool { return lines[i].s.value > lines[j].s.value })
			for _, l := range lines {
				log.Printf("SCORES slot %d %s: %s (%s) %d %s", slot, obj, l.holder, l.s.shown, l.s.value, l.s.format)
			}
		}
		return 0
	}
}

// stateAt is the Java state the bot has at a block (0: none).
func (b *bot) stateAt(x, y, z int) uint32 {
	col := b.chunks[[2]int32{int32(x >> 4), int32(z >> 4)}]
	if col == nil {
		return 0
	}
	if i := (y - b.minY) >> 4; i >= 0 && i < len(col.Sections) {
		return col.Sections[i].Blocks[jchunk.BlockIndex(x&15, y&15, z&15)]
	}
	return 0
}
