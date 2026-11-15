package bridge

import (
	"math/rand/v2"
	"strconv"
	"strings"

	"javabridge/core/internal/blocks"
	"javabridge/core/internal/sounds"

	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Bedrock particles -> Java level_particles / level_event.
//
// LevelEvent particle events become the Java level event that draws the same thing where Java has
// one (2001 block destroy, which also plays the break sound on both editions; potion splash, wax,
// scrape, spark, bone meal growth...), else level_particles with the Java particle. LevelEvent
// 0x4000|type (Bedrock's legacy particle types) and SpawnParticleEffect (named particles) use the
// reversed Geyser tables (internal/sounds). The player's own digging and breaking the Java client
// draws itself; cracking particles of other players' digging Java does not draw at all (only the
// crack overlay, interact.go). Entity effect particles come from entity data (entities_props.go),
// hearts/taming/villager particles from entity events (entities_events.go).

// particle is a Java particle type with its options.
type particle struct {
	name  string  // Java particle type without namespace
	state uint32  // block particles: 26.3 block state
	color int32   // ARGB: dust, entity_effect, effect, instant_effect, tinted_leaves, flash
	scale float32 // dust (0: 1)
	power float32 // effect, instant_effect, dragon_breath (0: 1)
}

// writeParticle writes a particle type and its options; false if it cannot be sent (unknown to
// the client, or options the bridge cannot fill in).
func (p *Player) writeParticle(w *wire.Writer, pt particle) bool {
	id := p.ver().BuiltinID("minecraft:particle_type", "minecraft:"+pt.name)
	if id < 0 {
		return false
	}
	w.VarInt(id)
	one := func(v float32) float32 {
		if v == 0 {
			return 1
		}
		return v
	}
	switch pt.name {
	case "block", "block_marker", "falling_dust", "dust_pillar", "block_crumble":
		w.VarInt(p.ver().BlockState(int32(pt.state)))
	case "dust":
		w.Int32(pt.color)
		w.Float32(one(pt.scale))
	case "dust_color_transition":
		w.Int32(pt.color)
		w.Int32(pt.color)
		w.Float32(one(pt.scale))
	case "entity_effect", "tinted_leaves", "flash":
		w.Int32(pt.color)
	case "effect", "instant_effect":
		w.Int32(pt.color)
		w.Float32(one(pt.power))
	case "dragon_breath":
		w.Float32(one(pt.power))
	case "sculk_charge":
		w.Float32(0)
	case "shriek":
		w.VarInt(0)
	case "item", "vibration", "trail", "geyser", "geyser_base", "geyser_poof", "geyser_plume":
		return false
	}
	return true
}

// sendParticle sends level_particles: count particles around pos (gaussian spread dist, speed
// speed); count 0 is one particle with velocity dist*speed (note colours, directed smoke).
func (p *Player) sendParticle(pt particle, pos, dist mgl64.Vec3, speed float32, count int32) {
	if !valid(pos[0]) || !valid(pos[1]) || !valid(pos[2]) {
		return
	}
	w := p.pk()
	if p.ver().Native() {
		if !p.writeParticle(w, pt) {
			return
		}
		w.Bool(false) // override limiter
		w.Bool(false) // always show
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		w.Float32(float32(dist[0]))
		w.Float32(float32(dist[1]))
		w.Float32(float32(dist[2]))
		w.Float32(speed)
		w.Float32(speed)
		w.Float32(speed)
		w.VarInt(count)
		w.VarInt(0) // RandomizationType.DEFAULT
	} else {
		// 26.2: the options last, one max speed, an int count.
		var opt wire.Writer
		if !p.writeParticle(&opt, pt) {
			return
		}
		w.Bool(false)
		w.Bool(false)
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		w.Float32(float32(dist[0]))
		w.Float32(float32(dist[1]))
		w.Float32(float32(dist[2]))
		w.Float32(speed)
		w.Int32(count)
		w.Raw(opt.B)
	}
	p.send(v777.ClientboundPlayLevelParticles, w)
}

func init() {
	OnBedrock(packet.IDLevelEvent, (*Player).levelEventParticles)
	OnBedrock(packet.IDSpawnParticleEffect, (*Player).spawnParticleEffect)
	OnBedrock(packet.IDLevelEventGeneric, (*Player).levelEventGeneric)
	OnBedrock(packet.IDActorEvent, func(p *Player, pk *packet.ActorEvent) {
		if e, ok := p.ents.shown[pk.EntityRuntimeID]; ok && e.spawned && pk.EventType == packet.ActorEventSilverfishMergeAnimation {
			p.sendEntityEvent(e.id, 20) // Mob spawn animation (poof)
		}
	})
}

// javaStateOfRID is the 26.3 Java state of a server block runtime id (0, false: none or air).
func (p *Player) javaStateOfRID(rid int32) (uint32, bool) {
	name, st, ok := p.javaBlockOfRID(rid)
	return st, ok && name != "air"
}

// javaDirection is the Java Direction (3D data value) of Bedrock's shoot particle data
// ((dx+1) + (dz+1)*3).
func javaDirection(data int32) int32 {
	dx, dz := data%3-1, data/3%3-1
	switch {
	case dx < 0:
		return 4 // west
	case dx > 0:
		return 5 // east
	case dz < 0:
		return 2 // north
	case dz > 0:
		return 3 // south
	}
	return 1 // up
}

// levelEventParticleJava are particle level events with a Java level event of their own and no data.
var levelEventParticleJava = map[int32]int32{
	packet.LevelEventParticlesEyeOfEnderDeath: 2003,
	packet.LevelEventParticlesMobBlockSpawn:   2004,
	packet.LevelEventParticlesEvaporate:       2009,
	packet.LevelEventParticlesEvaporateWater:  2009,
	packet.LevelEventWaxOn:                    3003,
	packet.LevelEventWaxOff:                   3004,
	packet.LevelEventScrape:                   3005,
}

// levelEventParticleSimple are particle level events drawn with one Java particle type: count,
// spread, speed, y offset.
var levelEventParticleSimple = map[int32]struct {
	name   string
	count  int32
	spread mgl64.Vec3
	speed  float32
	dy     float64
}{
	packet.LevelEventParticleDeathSmoke:           {"poof", 20, mgl64.Vec3{0.4, 0.5, 0.4}, 0.02, 0.5},
	packet.LevelEventParticlesCritical:            {"crit", 10, mgl64.Vec3{0.3, 0.5, 0.3}, 0.2, 0},
	packet.LevelEventParticlesTeleport:            {"portal", 32, mgl64.Vec3{0.4, 0.9, 0.4}, 0.3, 0.5},
	packet.LevelEventParticlesTeleportTrail:       {"portal", 1, mgl64.Vec3{}, 0, 0},
	packet.LevelEventParticlesBubble:              {"bubble", 8, mgl64.Vec3{0.3, 0.3, 0.3}, 0.05, 0},
	packet.LevelEventParticlesFizzEffect:          {"large_smoke", 8, mgl64.Vec3{0.25, 0.25, 0.25}, 0.01, 0.5},
	packet.LevelEventSculkCatalystBloom:           {"sculk_soul", 5, mgl64.Vec3{0.3, 0.1, 0.3}, 0.02, 1.15},
	packet.LevelEventSculkChargePop:               {"sculk_charge_pop", 4, mgl64.Vec3{0.3, 0.3, 0.3}, 0.02, 0.5},
	packet.LevelEventSonicExplosion:               {"sonic_boom", 1, mgl64.Vec3{}, 0, 0},
	packet.LevelEventDustPlume:                    {"dust_plume", 7, mgl64.Vec3{0.25, 0.1, 0.25}, 0.02, 0.5},
	packet.LevelEventParticlesBreezeWindExplosion: {"gust_emitter_small", 1, mgl64.Vec3{}, 0, 0},
	packet.LevelEventParticlesWindExplosion:       {"gust_emitter_large", 1, mgl64.Vec3{}, 0, 0},
}

func (p *Player) levelEventParticles(pk *packet.LevelEvent) {
	if p.rem == nil {
		return
	}
	t := pk.EventType
	pos := vec64(pk.Position)
	bp := blockPosOf(pos)
	switch {
	case t == packet.LevelEventParticlesDestroyBlock:
		if p.ownDig(bp) {
			return // the client broke it and drew it (MultiPlayerGameMode.destroyBlock)
		}
		if st, ok := p.javaStateOfRID(pk.EventData); ok {
			p.sendLevelEvent(2001, bp, p.ver().BlockState(int32(st)), false)
		}
	case t == packet.LevelEventParticlesDestroyBlockNoSound:
		if st, ok := p.javaStateOfRID(pk.EventData); ok {
			p.sendLevelEvent(2014, bp, p.ver().BlockState(int32(st)), false)
		}
	case t == packet.LevelEventParticlesExplosion:
		name := "explosion"
		if pk.EventData >= 2 { // radius
			name = "explosion_emitter"
		}
		p.sendParticle(particle{name: name}, pos, mgl64.Vec3{}, 1, 0)
	case t == packet.LevelEventParticlesPotionSplash:
		p.sendLevelEvent(2002, bp, pk.EventData&0xffffff, false)
	case t == packet.LevelEventParticleCropGrowth:
		n := pk.EventData
		if n <= 0 {
			n = 15
		}
		p.sendLevelEvent(2011, bp, n, false) // PARTICLES_BEE_GROWTH: the particles without a sound
	case t == packet.LevelEventParticleTurtleEgg:
		p.sendLevelEvent(2012, bp, max(pk.EventData, 1), false)
	case t == packet.LevelEventParticleSoundGuardianGhost:
		if pos.Sub(p.mv.pos).Len() < 16 {
			p.sendGameEvent(10, 1) // GUARDIAN_ELDER_EFFECT: the ghost and the curse sound
		}
	case t == packet.LevelEventParticlesShoot:
		p.sendLevelEvent(2000, bp, javaDirection(pk.EventData), false)
	case t == packet.LevelEventParticlesShootWhiteSmoke:
		p.sendLevelEvent(2010, bp, javaDirection(pk.EventData), false)
	case t == packet.LevelEventParticlesElectricSpark:
		p.sendLevelEvent(3002, bp, -1, false)
	case t == packet.LevelEventParticlesDestroyArmorStand:
		if jb := blocks.JavaBlockNamed("minecraft:oak_planks"); jb != nil {
			p.sendParticle(particle{name: "block", state: jb.Default}, pos.Add(mgl64.Vec3{0, 1.33, 0}),
				mgl64.Vec3{0.125, 0.5, 0.125}, 0.05, 10)
		}
	case t == packet.LevelEventParticleSculkShriek:
		for i := 0; i < 10; i++ {
			p.sendShriek(bp, i*5)
		}
	case t == packet.LevelEventAnimationSpawnCobweb:
		p.sendLevelEvent(3018, bp, 0, false)
	case t == packet.LevelEventParticleSmashAttackGroundDust:
		p.sendLevelEvent(2013, bp, 750, false)
	case t&packet.LevelEventParticleLegacyEvent != 0 && t < packet.LevelEventParticleLegacyEvent<<1:
		p.legacyParticle(t&^packet.LevelEventParticleLegacyEvent, pos, pk.EventData)
	default:
		if id, ok := levelEventParticleJava[t]; ok {
			p.sendLevelEvent(id, bp, 0, false)
		} else if s, ok := levelEventParticleSimple[t]; ok {
			p.sendParticle(particle{name: s.name}, pos.Add(mgl64.Vec3{0, s.dy, 0}), s.spread, s.speed, s.count)
		}
	}
}

// sendShriek sends one sculk shriek particle (delay in ticks) above a shrieker.
func (p *Player) sendShriek(bp cube.Pos, delay int) {
	w := p.pk()
	id := p.ver().BuiltinID("minecraft:particle_type", "minecraft:shriek")
	if id < 0 {
		return
	}
	pos := mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5}
	opt := func(w *wire.Writer) { w.VarInt(id); w.VarInt(int32(delay)) }
	if p.ver().Native() {
		opt(w)
		w.Bool(false)
		w.Bool(false)
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		for i := 0; i < 6; i++ {
			w.Float32(0)
		}
		w.VarInt(1)
		w.VarInt(0)
	} else {
		w.Bool(false)
		w.Bool(false)
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		for i := 0; i < 4; i++ {
			w.Float32(0)
		}
		w.Int32(1)
		opt(w)
	}
	p.send(v777.ClientboundPlayLevelParticles, w)
}

// legacyParticle shows a Bedrock legacy particle type (AddParticle level event).
func (p *Player) legacyParticle(id int32, pos mgl64.Vec3, data int32) {
	name, ok := sounds.LegacyParticle(id)
	if !ok {
		p.unknownSound("particle type", strconv.Itoa(int(id)))
		return
	}
	pt := particle{name: name}
	switch name {
	case "block", "block_marker", "dust_pillar", "block_crumble":
		st, ok := p.javaStateOfRID(data)
		if !ok {
			return
		}
		pt.state = st
	case "falling_dust":
		// Bedrock gives the dust's colour; Java the block: the block below.
		bn, st, ok := p.javaBlockAt(blockPosOf(pos).Side(cube.FaceDown))
		if !ok || bn == "air" {
			return
		}
		pt.state = st
	case "dust", "dust_color_transition":
		pt.color = data | -0x1000000
	case "entity_effect", "effect", "instant_effect", "tinted_leaves":
		pt.color = data
		if pt.color&-0x1000000 == 0 {
			pt.color |= -0x1000000
		}
	case "note":
		p.sendParticle(pt, pos, mgl64.Vec3{float64(data%25) / 24, 0, 0}, 1, 0)
		return
	case "item":
		return // needs the item stack
	}
	p.sendParticle(pt, pos, mgl64.Vec3{}, 0, 1)
}

func (p *Player) spawnParticleEffect(pk *packet.SpawnParticleEffect) {
	if p.rem == nil {
		return
	}
	name, ok := sounds.NamedParticle(pk.ParticleName)
	if !ok {
		n := trimNS(pk.ParticleName)
		if v777.BuiltinID("minecraft:particle_type", "minecraft:"+n) < 0 {
			p.unknownSound("particle", pk.ParticleName)
			return
		}
		name = n
	}
	pos := vec64(pk.Position)
	if pk.EntityUniqueID != -1 {
		if e := p.spawnedByUnique(pk.EntityUniqueID); e != nil {
			pos = pos.Add(e.pos)
		} else if pk.EntityUniqueID == p.rem.gd.EntityUniqueID {
			pos = pos.Add(p.mv.pos)
		}
	}
	pt := particle{name: name, color: -1}
	switch name {
	case "dust":
		pt.color = -0x10000 // red, Bedrock's default redstone dust
	case "block", "block_marker", "falling_dust", "dust_pillar", "block_crumble":
		bn, st, ok := p.javaBlockAt(blockPosOf(pos).Side(cube.FaceDown))
		if !ok || bn == "air" {
			return
		}
		pt.state = st
	}
	// Emitters are bursts on Bedrock; Java particles are single.
	if strings.HasSuffix(pk.ParticleName, "_emitter") && !strings.Contains(name, "emitter") && !strings.HasPrefix(name, "explosion") {
		p.sendParticle(pt, pos, mgl64.Vec3{0.3, 0.3, 0.3}, 0.05, 8)
		return
	}
	p.sendParticle(pt, pos, mgl64.Vec3{}, 0, 1)
}

// levelEventGeneric shows a block explosion's blocks (2026: the destroyed blocks' positions) with
// the small poofs Java's explosion block particles are.
func (p *Player) levelEventGeneric(pk *packet.LevelEventGeneric) {
	if pk.EventID != packet.LevelEventParticlesBlockExplosion || p.rem == nil {
		return
	}
	buf := make([]byte, 0, len(pk.SerialisedEventData)+3)
	buf = append(buf, 0x0a, 0x00)
	buf = append(buf, pk.SerialisedEventData...)
	buf = append(buf, 0x00)
	var m map[string]any
	if nbt.UnmarshalEncoding(buf, &m, nbt.NetworkLittleEndian) != nil {
		return
	}
	n, _ := m["size"].(int32)
	f := func(k string) (float64, bool) { v, ok := m[k].(float32); return float64(v), ok }
	for i := int32(0); i < min(n, 64); i++ {
		if rand.IntN(3) != 0 {
			continue // Java shows some of them
		}
		x, ok1 := f("pos" + strconv.Itoa(int(i)) + "x")
		y, ok2 := f("pos" + strconv.Itoa(int(i)) + "y")
		z, ok3 := f("pos" + strconv.Itoa(int(i)) + "z")
		if !ok1 || !ok2 || !ok3 {
			break
		}
		name := "poof"
		if rand.IntN(2) == 0 {
			name = "smoke"
		}
		p.sendParticle(particle{name: name}, mgl64.Vec3{x + 0.5, y + 0.5, z + 0.5}, mgl64.Vec3{0.3, 0.3, 0.3}, 0.05, 1)
	}
}
