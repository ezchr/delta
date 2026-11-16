package main

// Entity checks (the original bridge milestone 2): the bot keeps the entities the bridge shows it,
// decodes every entity packet strictly (as the Java client would, so a malformed packet is an
// error in the log) and has steps to look at, attack and use them.
//
// Steps: ents (list them) | expectent TYPE [N] (at least N of a type, default 1) | attack TYPE
// [N] (hit the nearest N times, 0.6 s apart) | use TYPE (right click the nearest) | lookat TYPE.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	jitem "github.com/ezchr/go-mcjava/item"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

type botEntity struct {
	id         int32
	typ        string
	pos        [3]float64
	yaw, pitch float32
	head       float32
	data       int32
	moves      int
	syncs      int
	meta       map[byte]string
	equip      map[byte]string
	health     float32
	dead       bool
	passengers []int32
	name       string // custom name
	attrs      map[string]float64
	anim       *displayAnim // anim.go
}

var entityNames = func() map[int32]string {
	m := map[int32]string{}
	for n, id := range v777.Builtin["minecraft:entity_type"] {
		m[id] = strings.TrimPrefix(n, "minecraft:")
	}
	return m
}()

var itemNames = func() map[int32]string {
	m := map[int32]string{}
	for n, id := range v777.Builtin["minecraft:item"] {
		m[id] = strings.TrimPrefix(n, "minecraft:")
	}
	return m
}()

var particleNames = func() map[int32]string {
	m := map[int32]string{}
	for n, id := range v777.Builtin["minecraft:particle_type"] {
		m[id] = strings.TrimPrefix(n, "minecraft:")
	}
	return m
}()

var particleEntityEffect = v777.Builtin["minecraft:particle_type"]["minecraft:entity_effect"]

var attributeNames = func() map[int32]string {
	m := map[int32]string{}
	for n, id := range v777.Builtin["minecraft:attribute"] {
		m[id] = strings.TrimPrefix(n, "minecraft:")
	}
	return m
}()

var effectNames = func() map[int32]string {
	m := map[int32]string{}
	for n, id := range v777.Builtin["minecraft:mob_effect"] {
		m[id] = strings.TrimPrefix(n, "minecraft:")
	}
	return m
}()

// ents is the bot's entity state (guarded by b.mu).
type entState struct {
	byID    map[int32]*botEntity
	tab     map[[16]byte]string
	counts  map[string]int
	removed int
	errs    int
	dim     string
}

var es = entState{byID: map[int32]*botEntity{}, tab: map[[16]byte]string{}, counts: map[string]int{}}

func entErr(format string, a ...any) {
	es.errs++
	log.Printf("ENTITY ERROR "+format, a...)
}

// entityPacket looks at a packet for the entity checks (b.mu is held).
func (b *bot) entityPacket(id int32, body []byte) {
	r := wire.NewReader(body)
	switch id {
	case v777.ClientboundPlayLogin:
		clear(es.byID)
		r.Int32()
		r.Bool()
		for n := r.VarInt(); n > 0 && r.Err == nil; n-- {
			r.String(256)
		}
		r.VarInt()
		r.VarInt()
		r.VarInt()
		r.Bool()
		r.Bool()
		r.Bool()
		r.VarInt()
		es.dim = r.String(256)
	case v777.ClientboundPlayRespawn:
		// The Java client keeps its level (and entities) on a respawn into the same dimension.
		r.VarInt()
		dim := r.String(256)
		if dim != es.dim && len(es.byID) > 0 {
			log.Printf("ENT respawn into %s: client drops %d entities", dim, len(es.byID))
			clear(es.byID)
		}
		es.dim = dim
	case v777.ClientboundPlayAddEntity:
		eid := r.VarInt()
		r.UUID()
		typ := r.VarInt()
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		r.LpVec3()
		pitch, yaw, head := r.Angle(), r.Angle(), r.Angle()
		data := r.VarInt()
		if r.Err != nil || r.Len() != 0 {
			entErr("add_entity: %v, %d bytes left", r.Err, r.Len())
			return
		}
		e := &botEntity{id: eid, typ: entityNames[typ], pos: [3]float64{x, y, z}, yaw: yaw, pitch: pitch, head: head, data: data,
			meta: map[byte]string{}, equip: map[byte]string{}}
		if old, ok := es.byID[eid]; ok {
			log.Printf("ENT re-add %d (%s -> %s)", eid, old.typ, e.typ)
		}
		es.byID[eid] = e
		es.counts["add "+e.typ]++
		log.Printf("ENT add %d %s at %.2f %.2f %.2f yaw %.0f data %d", eid, e.typ, x, y, z, yaw, data)
	case v777.ClientboundPlayRemoveEntities:
		n := r.VarInt()
		var ids []string
		for i := int32(0); i < n && r.Err == nil; i++ {
			eid := r.VarInt()
			if e, ok := es.byID[eid]; ok {
				ids = append(ids, fmt.Sprintf("%d %s", eid, e.typ))
				delete(es.byID, eid)
				es.removed++
			}
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("remove_entities: %v", r.Err)
		}
		if len(ids) > 0 {
			log.Printf("ENT remove %s", strings.Join(ids, ", "))
		}
	case v777.ClientboundPlayMoveEntityPos, v777.ClientboundPlayMoveEntityPosRot:
		eid := r.VarInt()
		r.VarInt() // props
		dx, dy, dz := r.Int16(), r.Int16(), r.Int16()
		var yaw, pitch float32
		if id == v777.ClientboundPlayMoveEntityPosRot {
			yaw, pitch = r.Angle(), r.Angle()
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("move_entity: %v, %d left", r.Err, r.Len())
			return
		}
		e := es.byID[eid]
		if e == nil {
			entErr("move of unknown entity %d", eid)
			return
		}
		for i, d := range [3]int16{dx, dy, dz} {
			if d != 0 {
				e.pos[i] = float64(int64(math.Floor(e.pos[i]*4096+0.5))+int64(d)) / 4096
			}
		}
		if id == v777.ClientboundPlayMoveEntityPosRot {
			e.yaw, e.pitch = yaw, pitch
		}
		e.moves++
		es.counts["move"]++
	case v777.ClientboundPlayMoveEntityRot:
		eid := r.VarInt()
		r.Bool()
		yaw, pitch := r.Angle(), r.Angle()
		if r.Err != nil || r.Len() != 0 {
			entErr("move_entity_rot: %v", r.Err)
			return
		}
		if e := es.byID[eid]; e != nil {
			e.yaw, e.pitch = yaw, pitch
			e.moves++
			if e.typ == "item_display" {
				if e.anim == nil {
					e.anim = &displayAnim{distinct: map[string]bool{}}
				}
				e.anim.rots++
			}
		}
		es.counts["rot"]++
	case v777.ClientboundPlayEntityPositionSync:
		eid := r.VarInt()
		r.VarInt()
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		yaw, pitch := r.Float32(), r.Float32()
		r.Bool()
		if r.Err != nil || r.Len() != 0 {
			entErr("entity_position_sync: %v", r.Err)
			return
		}
		if e := es.byID[eid]; e != nil {
			e.pos, e.yaw, e.pitch = [3]float64{x, y, z}, yaw, pitch
			e.syncs++
		} else {
			entErr("sync of unknown entity %d", eid)
		}
		es.counts["sync"]++
	case v777.ClientboundPlayRotateHead:
		eid := r.VarInt()
		h := r.Angle()
		if e := es.byID[eid]; e != nil {
			e.head = h
		}
		es.counts["head"]++
	case v777.ClientboundPlaySetEntityMotion:
		eid := r.VarInt()
		r.LpVec3()
		if eid != 1 {
			es.counts["motion"]++
		}
	case v777.ClientboundPlaySetEntityData:
		eid := r.VarInt()
		e := es.byID[eid]
		var parts []string
		for r.Err == nil {
			idx := r.Byte()
			if idx == 0xff {
				break
			}
			ser := r.VarInt()
			v, ok := readData(r, ser)
			if !ok {
				entErr("entity data of %d: serializer %d at index %d not decodable", eid, ser, idx)
				return
			}
			parts = append(parts, fmt.Sprintf("%d=%s", idx, v))
			if e != nil {
				e.meta[idx] = v
				if idx == 9 && e.typ != "player" || idx == 9 {
					if f, err := strconv.ParseFloat(v, 32); err == nil {
						e.health = float32(f)
					}
				}
				if idx == 2 {
					e.name = v
				}
			}
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("set_entity_data %d: %v, %d left", eid, r.Err, r.Len())
			return
		}
		es.counts["data"]++
		if animData(e, parts) {
			es.counts["anim"]++
		} else if eid != 1 {
			typ := "?"
			if e != nil {
				typ = e.typ
			}
			log.Printf("ENT data %d %s: %s", eid, typ, strings.Join(parts, " "))
		}
	case v777.ClientboundPlaySetEquipment:
		eid := r.VarInt()
		var parts []string
		for r.Err == nil {
			slot := r.Byte()
			var st jitem.Stack
			st.Decode(r)
			name := "empty"
			if st.Count > 0 {
				name = fmt.Sprintf("%dx%s", st.Count, itemNames[st.ID])
			}
			parts = append(parts, fmt.Sprintf("%d=%s", slot&0x7f, name))
			if e := es.byID[eid]; e != nil {
				e.equip[slot&0x7f] = name
			}
			if slot&0x80 == 0 {
				break
			}
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("set_equipment %d: %v, %d left", eid, r.Err, r.Len())
			return
		}
		es.counts["equipment"]++
		log.Printf("ENT equipment %d: %s", eid, strings.Join(parts, " "))
	case v777.ClientboundPlayEntityEvent:
		eid := r.Int32()
		ev := r.Byte()
		es.counts[fmt.Sprintf("event %d", ev)]++
		log.Printf("ENT event %d on %d", ev, eid)
		if e := es.byID[eid]; e != nil && ev == 3 {
			e.dead = true
		}
	case v777.ClientboundPlayDamageEvent:
		eid := r.VarInt()
		r.VarInt()
		r.VarInt()
		r.VarInt()
		if r.Bool() {
			r.Float64()
			r.Float64()
			r.Float64()
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("damage_event: %v", r.Err)
		}
		es.counts["hurt"]++
		log.Printf("ENT hurt %d", eid)
	case v777.ClientboundPlaySwingAnimation:
		eid := r.VarInt()
		es.counts["swing"]++
		log.Printf("ENT swing %d", eid)
	case v777.ClientboundPlayAnimate:
		eid := r.VarInt()
		a := r.Byte()
		log.Printf("ENT animate %d action %d", eid, a)
	case v777.ClientboundPlaySetPassengers:
		eid := r.VarInt()
		n := r.VarInt()
		var ps []int32
		for i := int32(0); i < n && r.Err == nil; i++ {
			ps = append(ps, r.VarInt())
		}
		if e := es.byID[eid]; e != nil {
			e.passengers = ps
		}
		ridePassengers(eid, ps) // ride.go
		es.counts["passengers"]++
		log.Printf("ENT passengers of %d: %v", eid, ps)
	case v777.ClientboundPlaySetEntityLink:
		log.Printf("ENT leash %d -> %d", r.Int32(), r.Int32())
	case v777.ClientboundPlayUpdateAttributes:
		eid := r.VarInt()
		var parts []string
		for n := r.VarInt(); n > 0 && r.Err == nil; n-- {
			aid := r.VarInt()
			v := r.Float64()
			for m := r.VarInt(); m > 0 && r.Err == nil; m-- {
				r.UUID()
				r.Float64()
				r.Byte()
			}
			parts = append(parts, fmt.Sprintf("%s=%g", attributeNames[aid], v))
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("update_attributes %d: %v, %d left", eid, r.Err, r.Len())
			return
		}
		if eid != 1 {
			typ := "?"
			if e := es.byID[eid]; e != nil {
				typ = e.typ
				if e.attrs == nil {
					e.attrs = map[string]float64{}
				}
				for _, p := range parts {
					var k string
					var v float64
					if i := strings.IndexByte(p, '='); i > 0 {
						k = p[:i]
						v, _ = strconv.ParseFloat(p[i+1:], 64)
						e.attrs[k] = v
					}
				}
			}
			es.counts["attributes"]++
			log.Printf("ENT attributes %d %s: %s", eid, typ, strings.Join(parts, " "))
		}
	case v777.ClientboundPlaySetPlayerTeam:
		name := r.String(32767)
		method := r.Byte()
		var desc string
		if method == 0 || method == 2 {
			skipNBT(r, r.Byte())
			prefix := skipNBT(r, r.Byte())
			suffix := skipNBT(r, r.Byte())
			vis := r.VarInt()
			coll := r.VarInt()
			colour := int32(-1)
			if r.Bool() {
				colour = r.VarInt()
			}
			r.Byte()
			desc = fmt.Sprintf("prefix %s suffix %s visibility %d collision %d colour %d", prefix, suffix, vis, coll, colour)
		}
		if method == 0 || method == 3 || method == 4 {
			var members []string
			for n := r.VarInt(); n > 0 && r.Err == nil; n-- {
				members = append(members, r.String(32767))
			}
			desc += fmt.Sprintf(" members %v", members)
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("set_player_team %s: %v, %d left", name, r.Err, r.Len())
			return
		}
		es.counts["team"]++
		log.Printf("TEAM %s method %d %s", name, method, desc)
	case v777.ClientboundPlayTakeItemEntity:
		item, taker, n := r.VarInt(), r.VarInt(), r.VarInt()
		es.counts["pickup"]++
		log.Printf("ENT pickup item %d by %d (%d)", item, taker, n)
	case v777.ClientboundPlayUpdateMobEffect:
		eid := r.VarInt()
		eff := r.VarInt()
		amp := r.VarInt()
		dur := r.VarInt()
		fl := r.Byte()
		if r.Err != nil || r.Len() != 0 {
			entErr("update_mob_effect: %v", r.Err)
		}
		es.counts["effect"]++
		log.Printf("ENT effect on %d: %s amp %d dur %d flags %#x", eid, effectNames[eff], amp, dur, fl)
	case v777.ClientboundPlayRemoveMobEffect:
		eid := r.VarInt()
		eff := r.VarInt()
		log.Printf("ENT effect removed on %d: %s", eid, effectNames[eff])
	case v777.ClientboundPlayPlayerInfoUpdate:
		actions := r.Byte()
		n := r.VarInt()
		for i := int32(0); i < n && r.Err == nil; i++ {
			u := r.UUID()
			if actions&0x01 != 0 {
				name := r.String(16)
				var props []string
				for np := r.VarInt(); np > 0 && r.Err == nil; np-- {
					pn := r.String(64)
					pv := r.String(32767)
					signed := r.Bool()
					if signed {
						r.String(1024)
					}
					props = append(props, fmt.Sprintf("%s(%d bytes, signed %v, %s)", pn, len(pv), signed, textureURL(pv)))
				}
				es.tab[u] = name
				log.Printf("ENT profile %q uuid %x default skin %d properties %v", name, u, defaultSkin(u), props)
			}
			if actions&0x02 != 0 {
				if r.Bool() {
					entErr("chat session in tab")
					return
				}
			}
			var mode int32
			var listed bool
			if actions&0x04 != 0 {
				mode = r.VarInt()
			}
			if actions&0x08 != 0 {
				listed = r.Bool()
			}
			if actions&0x10 != 0 {
				r.VarInt()
			}
			if actions&0x20 != 0 {
				if r.Bool() {
					skipNBT(r, r.Byte())
				}
			}
			log.Printf("ENT tab %#x %q mode %d listed %v", actions, es.tab[u], mode, listed)
		}
		if r.Err != nil || r.Len() != 0 {
			entErr("player_info_update: %v, %d left", r.Err, r.Len())
		}
	case v777.ClientboundPlayBossEvent:
		r.UUID()
		action := r.VarInt()
		if action == 0 {
			title := skipNBT(r, r.Byte())
			log.Printf("ENT bossbar add %s health %.2f colour %d style %d flags %d", title, r.Float32(), r.VarInt(), r.VarInt(), r.Byte())
		} else if action == 2 {
			log.Printf("ENT bossbar health %.2f", r.Float32())
		} else {
			log.Printf("ENT bossbar action %d", action)
		}
	case v777.ClientboundPlayPlayerInfoRemove:
		n := r.VarInt()
		for i := int32(0); i < n && r.Err == nil; i++ {
			u := r.UUID()
			log.Printf("ENT tab remove %q", es.tab[u])
			delete(es.tab, u)
		}
	}
}

// readData reads one entity data value of the serializers the bridge sends.
func readData(r *wire.Reader, ser int32) (string, bool) {
	switch ser {
	case 0:
		return fmt.Sprintf("b%#x", r.Byte()), true
	case 1, 12, 20, 21, 25, 27, 34, 15:
		return strconv.Itoa(int(r.VarInt())), true
	case 22, 23, 24, 26, 28, 29, 30, 31, 32, 35, 36, 37, 38: // variants (registry ids), states
		return fmt.Sprintf("s%d:%d", ser, r.VarInt()), true
	case 5:
		return skipNBT(r, r.Byte()), true
	case 17: // particles: type id (+ ARGB colour for entity_effect)
		n := r.VarInt()
		var ps []string
		for i := int32(0); i < n && r.Err == nil; i++ {
			t := r.VarInt()
			if t == particleEntityEffect {
				ps = append(ps, fmt.Sprintf("effect#%08x", uint32(r.Int32())))
			} else if pn, ok := particleNames[t]; ok && (pn == "trial_omen" || pn == "raid_omen") {
				ps = append(ps, pn)
			} else {
				return "", false
			}
		}
		return "[" + strings.Join(ps, ",") + "]", r.Err == nil
	case 39:
		return fmt.Sprintf("v(%g,%g,%g)", r.Float32(), r.Float32(), r.Float32()), true
	case 40:
		return fmt.Sprintf("q(%g,%g,%g,%g)", r.Float32(), r.Float32(), r.Float32(), r.Float32()), true
	case 41: // a profile (a mannequin's): partial or full, then the skin patch
		if r.Bool() {
			r.UUID()
			r.String(16)
		} else {
			if r.Bool() {
				r.String(16)
			}
			if r.Bool() {
				r.UUID()
			}
		}
		for n := r.VarInt(); n > 0 && r.Err == nil; n-- {
			r.String(64)
			r.String(32767)
			if r.Bool() {
				r.String(1024)
			}
		}
		var tex [3]string
		for i := range tex {
			if r.Bool() {
				tex[i] = r.String(32767)
			}
		}
		model := "default"
		if r.Bool() {
			model = "wide"
			if r.Bool() {
				model = "slim"
			}
		}
		return fmt.Sprintf("profile(skin=%q,cape=%q,model=%s)", tex[0], tex[1], model), r.Err == nil
	case 2:
		return strconv.FormatInt(r.VarLong(), 10), true
	case 3:
		return strconv.FormatFloat(float64(r.Float32()), 'g', 4, 32), true
	case 6:
		if !r.Bool() {
			return "none", true
		}
		return skipNBT(r, r.Byte()), true
	case 7:
		var st jitem.Stack
		st.Decode(r)
		return fmt.Sprintf("%dx%s", st.Count, itemNames[st.ID]), r.Err == nil
	case 8:
		return strconv.FormatBool(r.Bool()), true
	case 9:
		return fmt.Sprintf("(%g,%g,%g)", r.Float32(), r.Float32(), r.Float32()), true
	case 10:
		x, y, z := r.Position()
		return fmt.Sprintf("%d,%d,%d", x, y, z), true
	case 11:
		if !r.Bool() {
			return "none", true
		}
		x, y, z := r.Position()
		return fmt.Sprintf("%d,%d,%d", x, y, z), true
	case 18:
		return fmt.Sprintf("villager(%d,%d,%d)", r.VarInt(), r.VarInt(), r.VarInt()), true
	}
	return "", false
}

// skipNBT skips a network NBT payload of a tag type, returning the strings in it.
func skipNBT(r *wire.Reader, t byte) string {
	var sb strings.Builder
	var payload func(t byte, depth int)
	payload = func(t byte, depth int) {
		if depth > 32 || r.Err != nil {
			return
		}
		switch t {
		case 1:
			r.Byte()
		case 2:
			r.Int16()
		case 3:
			r.Int32()
		case 4:
			r.Int64()
		case 5:
			r.Float32()
		case 6:
			r.Float64()
		case 7:
			n := r.Int32()
			for i := int32(0); i < n && r.Err == nil; i++ {
				r.Byte()
			}
		case 8:
			n := int(r.Uint16())
			s := make([]byte, 0, n)
			for i := 0; i < n && r.Err == nil; i++ {
				s = append(s, r.Byte())
			}
			sb.Write(s)
		case 9:
			et := r.Byte()
			n := r.Int32()
			for i := int32(0); i < n && r.Err == nil; i++ {
				payload(et, depth+1)
			}
		case 10:
			for r.Err == nil {
				ct := r.Byte()
				if ct == 0 {
					return
				}
				n := int(r.Uint16())
				for i := 0; i < n && r.Err == nil; i++ {
					r.Byte()
				}
				payload(ct, depth+1)
			}
		case 11:
			n := r.Int32()
			for i := int32(0); i < n && r.Err == nil; i++ {
				r.Int32()
			}
		case 12:
			n := r.Int32()
			for i := int32(0); i < n && r.Err == nil; i++ {
				r.Int64()
			}
		}
	}
	payload(t, 0)
	return strconv.Quote(sb.String())
}

// nearest is the nearest living entity of a type (b.mu held).
func (b *bot) nearest(typ string) *botEntity {
	var best *botEntity
	bd := math.MaxFloat64
	for _, e := range es.byID {
		if e.typ != typ || e.dead {
			continue
		}
		d := dist(e.pos, b.pos)
		if d < bd {
			best, bd = e, d
		}
	}
	return best
}

func dist(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}

// lookAt turns the bot to an entity's middle.
func (b *bot) lookAt(e *botEntity) {
	dx, dy, dz := e.pos[0]-b.pos[0], e.pos[1]+0.9-(b.pos[1]+1.62), e.pos[2]-b.pos[2]
	b.yaw = float32(-math.Atan2(dx, dz) * 180 / math.Pi)
	b.pitch = float32(-math.Atan2(dy, math.Sqrt(dx*dx+dz*dz)) * 180 / math.Pi)
}

// entityStep runs an entity step; it reports how long the step lasts (false: not an entity step).
func (b *bot) entityStep(cur []string) (time.Duration, bool) {
	arg := func(i int) string {
		if i < len(cur) {
			return cur[i]
		}
		return ""
	}
	switch cur[0] {
	case "sneak": // sneak SECONDS
		v, _ := strconv.ParseFloat(arg(1), 64)
		b.mu.Lock()
		b.keys = 0x20
		b.mu.Unlock()
		return time.Duration(v * float64(time.Second)), true
	case "ents":
		b.mu.Lock()
		b.listEntities()
		b.mu.Unlock()
		return 0, true
	case "animstat":
		b.mu.Lock()
		b.animStat()
		b.mu.Unlock()
		return 0, true
	case "expectent":
		n := 1
		if v, err := strconv.Atoi(arg(2)); err == nil {
			n = v
		}
		b.mu.Lock()
		c := 0
		for _, e := range es.byID {
			if e.typ == arg(1) {
				c++
			}
		}
		b.mu.Unlock()
		log.Printf("EXPECTENT %s >= %d: %v (%d)", arg(1), n, c >= n, c)
		return 0, true
	case "lookat":
		b.mu.Lock()
		if e := b.nearest(arg(1)); e != nil {
			b.lookAt(e)
			log.Printf("looking at %d %s (%.1f away)", e.id, e.typ, dist(e.pos, b.pos))
		}
		b.mu.Unlock()
		return 300 * time.Millisecond, true
	case "attack", "use":
		n := 1
		if v, err := strconv.Atoi(arg(2)); err == nil {
			n = v
		}
		go func() {
			for i := 0; i < n; i++ {
				b.mu.Lock()
				e := b.nearest(arg(1))
				if e == nil {
					b.mu.Unlock()
					log.Printf("%s: no %s", cur[0], arg(1))
					return
				}
				b.lookAt(e)
				log.Printf("%s %d %s (%.1f away, health %s)", strings.ToUpper(cur[0]), e.id, e.typ, dist(e.pos, b.pos), e.meta[9])
				id := e.id
				b.mu.Unlock()
				w := &wire.Writer{}
				w.VarInt(id)
				if cur[0] == "attack" {
					b.write(v777.ServerboundPlayAttack, w)
				} else {
					w.VarInt(0)
					y := 0.9
					if strings.Contains(arg(1), "boat") || strings.Contains(arg(1), "minecart") {
						y = 0.3
					}
					w.LpVec3(0, y, 0)
					w.Bool(false)
					b.write(v777.ServerboundPlayInteract, w)
				}
				time.Sleep(600 * time.Millisecond)
			}
		}()
		return time.Duration(n)*600*time.Millisecond + 300*time.Millisecond, true
	}
	return 0, false
}

// listEntities logs the entities the bot has (b.mu held).
func (b *bot) listEntities() {
	ids := make([]int, 0, len(es.byID))
	for id := range es.byID {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		e := es.byID[int32(id)]
		var meta []string
		for k, v := range e.meta {
			meta = append(meta, fmt.Sprintf("%d=%s", k, v))
		}
		sort.Strings(meta)
		var eq []string
		for k, v := range e.equip {
			if v != "empty" {
				eq = append(eq, fmt.Sprintf("%d=%s", k, v))
			}
		}
		sort.Strings(eq)
		log.Printf("ENTS %d %s at %.2f %.2f %.2f yaw %.0f head %.0f (%.1f away) moves %d syncs %d dead %v passengers %v equip [%s] data [%s]",
			e.id, e.typ, e.pos[0], e.pos[1], e.pos[2], e.yaw, e.head, dist(e.pos, b.pos), e.moves, e.syncs, e.dead, e.passengers,
			strings.Join(eq, " "), strings.Join(meta, " "))
	}
	log.Printf("ENTS %d entities, %d tab entries", len(es.byID), len(es.tab))
}

// entitySummary is the end-of-run line (b.mu held).
func (b *bot) entitySummary() {
	keys := make([]string, 0, len(es.counts))
	for k := range es.counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, es.counts[k]))
	}
	log.Printf("ENTITY SUMMARY errors %d, now %d entities, removed %d, %s", es.errs, len(es.byID), es.removed, strings.Join(parts, " "))
}

// defaultSkin is the Java client's default skin index for a UUID (DefaultPlayerSkin: 0 slim alex,
// 15 wide steve).
func defaultSkin(u [16]byte) int {
	var msb, lsb uint64
	for i := 0; i < 8; i++ {
		msb = msb<<8 | uint64(u[i])
		lsb = lsb<<8 | uint64(u[8+i])
	}
	hilo := msb ^ lsb
	h := int32(hilo>>32) ^ int32(hilo)
	return int(((h % 18) + 18) % 18)
}

// textureURL is the skin URL of a textures property value.
func textureURL(v string) string {
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "not base64"
	}
	var t struct {
		Textures struct {
			Skin struct {
				URL string `json:"url"`
			} `json:"SKIN"`
		} `json:"textures"`
	}
	if json.Unmarshal(b, &t) != nil {
		return "not json"
	}
	return t.Textures.Skin.URL
}
