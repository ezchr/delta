package bridge

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	"delta/core/internal/blocks"
	"delta/core/internal/javamap"
	"delta/core/internal/particles"

	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Bedrock particles -> Java level_particles / level_event.
//
// LevelEvent particle events become the Java level event that draws the same thing where Java has
// one and it plays no sound of its own (2001 block destroy plays the break sound on both editions),
// else level_particles drawn the way the Java client's own level event code draws them. Java 26.2
// lacks the 2014-2020 level events and plays sounds with a few others (wax on, potion splash), so it
// gets the particles. LevelEvent 0x4000|type and ParticleGenericSpawn (Bedrock's particle types)
// and SpawnParticleEffect (named particles) use internal/particles: Geyser's tables reversed plus
// fallbacks. The player's own digging and breaking the Java client draws itself; others' cracking
// particles come from the server's crack events. Entity effect particles come from entity data
// (entities_props.go), hearts/taming/villager particles from entity events (entities_events.go).

// particle is a Java particle type with its options.
type particle struct {
	name   string  // Java particle type without namespace
	state  uint32  // block particles: 26.3 block state
	item   int32   // item particles: 26.3 item id
	color  int32   // ARGB: dust, entity_effect, effect, instant_effect, tinted_leaves, flash
	color2 int32   // dust_color_transition: the colour it fades to (0: color)
	scale  float32 // dust (0: 1)
	power  float32 // effect, instant_effect, dragon_breath (0: 1)
	// vibration: the destination block and the travel time.
	dest  cube.Pos
	ticks int32
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
		to := pt.color2
		if to == 0 {
			to = pt.color
		}
		w.Int32(pt.color)
		w.Int32(to)
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
	case "item":
		// ItemStackTemplate: the item, count, an empty component patch.
		id := p.ver().Builtin("minecraft:item", pt.item)
		if pt.item <= 0 || id < 0 {
			return false
		}
		w.VarInt(id)
		w.VarInt(1)
		w.VarInt(0)
		w.VarInt(0)
	case "vibration":
		if pt.ticks <= 0 {
			return false
		}
		w.VarInt(0) // minecraft:block position source
		w.Position(pt.dest[0], pt.dest[1], pt.dest[2])
		w.VarInt(pt.ticks)
	case "trail", "geyser", "geyser_base", "geyser_poof", "geyser_plume":
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
	OnBedrock(packet.IDActorEvent, (*Player).actorEventParticles)
}

// actorEventParticles draws the particles of entity events the Java client draws no other way.
func (p *Player) actorEventParticles(pk *packet.ActorEvent) {
	e, ok := p.ents.shown[pk.EntityRuntimeID]
	if !ok || !e.spawned {
		return
	}
	switch pk.EventType {
	case packet.ActorEventSilverfishMergeAnimation:
		p.sendEntityEvent(e.id, 20) // Mob spawn animation (poof)
	case packet.ActorEventSquidFleeing:
		// Squid.spawnInk: the server sends the ink burst on Java; the Bedrock client draws it.
		name := "squid_ink"
		if e.bedrock == "minecraft:glow_squid" {
			name = "glow_squid_ink"
		}
		p.sendParticle(particle{name: name}, e.pos.Add(mgl64.Vec3{0, 0.5, 0}), mgl64.Vec3{0.3, 0.3, 0.3}, 0.3, 30)
	}
}

// javaStateOfRID is the 26.3 Java state of a server block runtime id (0, false: none or air).
func (p *Player) javaStateOfRID(rid int32) (uint32, bool) {
	name, st, ok := p.javaBlockOfRID(rid)
	return st, ok && name != "air"
}

// crackData splits a Bedrock ParticlesCrackBlock level event's data (block runtime id + face<<24)
// into the face and the block's Java state. With network id hashes the sum is no hash any more:
// each face is tried.
func (p *Player) crackData(data int32) (face int32, st uint32, ok bool) {
	f := int32(uint32(data) >> 24 & 7)
	if f < 6 {
		if st, ok := p.javaStateOfRID(data - f<<24); ok {
			return f, st, true
		}
	}
	for f := int32(0); f < 6; f++ {
		if st, ok := p.javaStateOfRID(data - f<<24); ok {
			return f, st, true
		}
	}
	return 0, 0, false
}

// faceNormals are the Java Direction normals by ordinal (down, up, north, south, west, east), the
// order Bedrock faces have too.
var faceNormals = [6]mgl64.Vec3{{0, -1, 0}, {0, 1, 0}, {0, 0, -1}, {0, 0, 1}, {-1, 0, 0}, {1, 0, 0}}

// crackParticles shows the particles of a block being dug at one face. 26.3 draws them with its
// own code (PARTICLES_DESTROY_PROGRESS); 26.2 gets one block particle on the face, as
// ParticleEngine.crack spawns per tick.
func (p *Player) crackParticles(bp cube.Pos, face int32, st uint32) {
	if face < 0 || face > 5 {
		face = 1
	}
	if p.ver().Native() {
		p.sendLevelEvent(2019, bp, face, false)
		return
	}
	n := faceNormals[face]
	at := mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5}.Add(n.Mul(0.55))
	spread := mgl64.Vec3{0.3, 0.3, 0.3}
	for i := range 3 {
		if n[i] != 0 {
			spread[i] = 0
		}
	}
	p.sendParticle(particle{name: "block", state: st}, at, spread, 0, 1)
}

// destroyParticles shows a block's break particles without the break sound.
func (p *Player) destroyParticles(bp cube.Pos, st uint32) {
	if p.ver().Native() {
		p.sendLevelEvent(2014, bp, p.ver().BlockState(int32(st)), false)
		return
	}
	// 26.2 has no PARTICLES_DESTROY_BLOCK: addDestroyBlockEffect's 4x4x4 particles flying apart.
	p.sendParticle(particle{name: "block", state: st}, mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5},
		mgl64.Vec3{0.25, 0.25, 0.25}, 0.15, 48)
}

// blockFaceParticles draws n particles on each face of a block (ParticleUtils.spawnParticlesOnBlockFaces).
func (p *Player) blockFaceParticles(pt particle, bp cube.Pos, n int32) {
	c := mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5}
	for _, f := range faceNormals {
		spread := mgl64.Vec3{0.25, 0.25, 0.25}
		for i := range 3 {
			if f[i] != 0 {
				spread[i] = 0
			}
		}
		p.sendParticle(pt, c.Add(f.Mul(0.55)), spread, 0, n)
	}
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

// potionColour is the Java potion splash data (RGB) of Bedrock's (ARGB, the effects' average
// colour).
func potionColour(data int32) int32 { return data & 0xffffff }

// levelEventParticleJava are particle level events with a Java level event of their own, no data
// and no sound.
var levelEventParticleJava = map[int32]int32{
	packet.LevelEventParticlesEyeOfEnderDeath: 2003,
	packet.LevelEventParticlesMobBlockSpawn:   2004,
	packet.LevelEventParticlesEvaporate:       2009,
	packet.LevelEventParticlesEvaporateWater:  2009,
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
	packet.LevelEventSculkChargePop:               {"sculk_charge_pop", 20, mgl64.Vec3{0.25, 0.25, 0.25}, 0.07, 0.5},
	packet.LevelEventSonicExplosion:               {"sonic_boom", 1, mgl64.Vec3{}, 0, 0},
	packet.LevelEventDustPlume:                    {"dust_plume", 7, mgl64.Vec3{0.25, 0.1, 0.25}, 0.02, 0.5},
	packet.LevelEventParticlesBreezeWindExplosion: {"gust_emitter_small", 1, mgl64.Vec3{}, 0, 0},
	packet.LevelEventParticlesWindExplosion:       {"gust_emitter_large", 1, mgl64.Vec3{}, 0, 0},
	// Ravager.roar: poofs around it.
	packet.LevelEventParticlesKnockbackRoar: {"poof", 40, mgl64.Vec3{1, 0.6, 1}, 0.2, 0},
}

func (p *Player) levelEventParticles(pk *packet.LevelEvent) {
	if p.rem == nil {
		return
	}
	t := pk.EventType
	pos := vec64(pk.Position)
	bp := blockPosOf(pos)
	centre := mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5}
	switch {
	case t == packet.LevelEventParticlesDestroyBlock:
		if p.ownDig(bp) {
			return // the client broke it and drew it (MultiPlayerGameMode.destroyBlock)
		}
		if st, ok := p.javaStateOfRID(pk.EventData); ok {
			p.sendLevelEvent(2001, bp, p.ver().BlockState(int32(st)), false)
		}
	case t == packet.LevelEventParticlesDestroyBlockNoSound, t == packet.LevelEventParticlesCropEaten,
		t == packet.LevelEventParticleDestroyEgg:
		st, ok := p.javaStateOfRID(pk.EventData)
		if !ok {
			var name string
			name, st, ok = p.javaBlockAt(bp)
			ok = ok && name != "air"
		}
		if ok {
			p.destroyParticles(bp, st)
		}
	case t == packet.LevelEventParticlesBreakingEgg:
		if name, st, ok := p.javaBlockAt(bp); ok && name != "air" {
			p.sendParticle(particle{name: "block", state: st}, centre.Sub(mgl64.Vec3{0, 0.3, 0}), mgl64.Vec3{0.2, 0.1, 0.2}, 0.05, 6)
		}
	case t == packet.LevelEventParticlesCrackBlock:
		if p.ownDig(bp) {
			return // ParticleEngine.crack while the client digs
		}
		if face, st, ok := p.crackData(pk.EventData); ok {
			p.crackParticles(bp, face, st)
		}
	case t >= packet.LevelEventParticlesCrackBlockDown && t <= packet.LevelEventParticlesCrackBlockEast:
		if p.ownDig(bp) {
			return
		}
		if st, ok := p.javaStateOfRID(pk.EventData); ok {
			p.crackParticles(bp, t-packet.LevelEventParticlesCrackBlockDown, st)
		}
	case t == packet.LevelEventParticlesExplosion:
		name := "explosion"
		if pk.EventData >= 2 { // radius
			name = "explosion_emitter"
		}
		p.sendParticle(particle{name: name}, pos, mgl64.Vec3{}, 1, 0)
	case t == packet.LevelEventParticlesPotionSplash:
		p.potionSplash(bp, potionColour(pk.EventData))
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
	case t == packet.LevelEventWaxOn:
		if p.ver().Native() {
			p.sendLevelEvent(3003, bp, 0, false)
		} else {
			p.blockFaceParticles(particle{name: "wax_on"}, bp, 1) // 26.2's 3003 plays the sound too
		}
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
	case t == packet.LevelEventParticlesDripstoneDrip:
		p.sendLevelEvent(1504, bp, 0, false) // PointedDripstoneBlock.spawnDripParticle
	case t == packet.LevelEventParticlesDragonEgg:
		p.dragonEggParticles(bp, pk.EventData)
	case t == packet.LevelEventParticlesTrialSpawnerDetection, t == packet.LevelEventParticlesTrialSpawnerDetectionCharged:
		// TrialSpawner.addDetectPlayerParticles (the Java events 3013/3019 also play the sound the
		// server sends itself). The data is the number of players seen.
		name := "trial_spawner_detection"
		if t == packet.LevelEventParticlesTrialSpawnerDetectionCharged {
			name += "_ominous"
		}
		p.sendParticle(particle{name: name}, centre, mgl64.Vec3{0.35, 0.25, 0.35}, 0, 30+min(max(pk.EventData, 0), 10)*5)
	case t == packet.LevelEventParticlesTrialSpawnerSpawning, t == packet.LevelEventParticlesTrialSpawnerEjecting:
		// TrialSpawner.addSpawnParticles / addEjectItemParticles (the Java events play sounds).
		flame := "flame"
		if t == packet.LevelEventParticlesTrialSpawnerEjecting {
			flame = "small_flame"
		} else if pk.EventData != 0 {
			flame = "soul_fire_flame"
		}
		p.sendParticle(particle{name: "smoke"}, centre, mgl64.Vec3{0.5, 0.5, 0.5}, 0, 20)
		p.sendParticle(particle{name: flame}, centre, mgl64.Vec3{0.5, 0.5, 0.5}, 0, 20)
	case t == packet.LevelEventParticlesTrialSpawnerBecomeCharged:
		p.sendParticle(particle{name: "trial_spawner_detection_ominous"}, centre, mgl64.Vec3{0.35, 0.25, 0.35}, 0, 30)
		p.sendParticle(particle{name: "trial_omen"}, centre, mgl64.Vec3{0.5, 0.5, 0.5}, 0.02, 20)
		p.sendParticle(particle{name: "soul_fire_flame"}, centre, mgl64.Vec3{0.5, 0.5, 0.5}, 0.02, 20)
	case t == packet.LevelEventAnimationVaultActivate, t == packet.LevelEventAnimationVaultDeactivate:
		flame := "small_flame"
		if pk.EventData != 0 {
			flame = "soul_fire_flame"
		}
		p.sendParticle(particle{name: flame}, centre, mgl64.Vec3{0.3, 0.3, 0.3}, 0.01, 20)
	case t == packet.LevelEventAnimationVaultEjectItem:
		p.sendParticle(particle{name: "small_flame"}, centre, mgl64.Vec3{0.06, 0.06, 0.06}, 0.02, 20)
		p.sendParticle(particle{name: "smoke"}, centre, mgl64.Vec3{0.06, 0.06, 0.06}, 0.02, 20)
	case t == packet.LevelEventParticleGenericSpawn:
		// The particle type in the low 16 bits, its data (an item id) in the high ones.
		p.legacyParticle(pk.EventData&0xffff, pos, pk.EventData>>16<<16)
	case t&packet.LevelEventParticleLegacyEvent != 0 && t < packet.LevelEventParticleLegacyEvent<<1:
		p.legacyParticle(t&^packet.LevelEventParticleLegacyEvent, pos, pk.EventData)
	default:
		if id, ok := levelEventParticleJava[t]; ok {
			p.sendLevelEvent(id, bp, 0, false)
		} else if s, ok := levelEventParticleSimple[t]; ok {
			p.sendParticle(particle{name: s.name}, pos.Add(mgl64.Vec3{0, s.dy, 0}), s.spread, s.speed, s.count)
		} else if jp, ok := particles.LevelEvent(t); ok {
			p.sendParticle(p.tableParticle(jp, pos, 0), pos, mgl64.Vec3{}, 0, 1)
		}
	}
}

// potionSplash shows a splash potion breaking: the bottle shards and the effect colour ring.
func (p *Player) potionSplash(bp cube.Pos, rgb int32) {
	if p.ver().Native() {
		p.sendLevelEvent(2002, bp, rgb, false) // 26.3: particles only (the sound is event 1053)
		return
	}
	// 26.2's 2002 plays the break sound too, which the server sends as its own sound.
	at := mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]), float64(bp[2]) + 0.5}
	if id, ok := v777.Builtin["minecraft:item"]["minecraft:splash_potion"]; ok {
		p.sendParticle(particle{name: "item", item: id}, at, mgl64.Vec3{}, 0.15, 8)
	}
	p.sendParticle(particle{name: "effect", color: rgb | -0x1000000}, at.Add(mgl64.Vec3{0, 0.3, 0}), mgl64.Vec3{0.3, 0, 0.3}, 0.8, 100)
}

// dragonEggParticles shows a dragon egg teleporting: portal particles along the way. Bedrock packs
// the distance to where it went as |dz| | |dy|<<8 | |dx|<<16 | signs<<24 (x, y, z bits 0-2: the
// difference from the destination to the start is negative).
func (p *Player) dragonEggParticles(from cube.Pos, data int32) {
	d := [3]int{int(data >> 16 & 0xff), int(data >> 8 & 0xff), int(data & 0xff)}
	for i := range d {
		if data>>24>>i&1 != 0 {
			d[i] = -d[i]
		}
	}
	a := mgl64.Vec3{float64(from[0]) + 0.5, float64(from[1]) + 0.5, float64(from[2]) + 0.5}
	b := a.Sub(mgl64.Vec3{float64(d[0]), float64(d[1]), float64(d[2])})
	const steps = 16
	for i := 0; i <= steps; i++ {
		at := a.Add(b.Sub(a).Mul(float64(i) / steps))
		p.sendParticle(particle{name: "portal"}, at, mgl64.Vec3{0.3, 0.3, 0.3}, 0.1, 8)
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

// itemOfData is the 26.3 Java item of Bedrock item particle data (item id << 16 | aux).
func (p *Player) itemOfData(data int32) (int32, bool) {
	name := p.itemName(data >> 16)
	if name == "" {
		return 0, false
	}
	id, ok := javamap.ItemID(name, int16(data&0xffff))
	return id, ok && id > 0
}

// tableParticle fills in the options of a table particle: the fixed block, item or colour, else
// what the world and the packet's data give (colour: ARGB from the packet, 0 if none).
func (p *Player) tableParticle(jp particles.Particle, pos mgl64.Vec3, colour int32) particle {
	pt := particle{name: jp.Java, color: int32(jp.Colour), color2: int32(jp.Colour2)}
	if colour != 0 {
		pt.color = colour
	}
	if pt.color == 0 {
		pt.color = -1
	}
	if jp.Block != "" {
		if jb := blocks.JavaBlockNamed("minecraft:" + jp.Block); jb != nil {
			pt.state = jb.Default
		}
	}
	if jp.Item != "" {
		pt.item = v777.Builtin["minecraft:item"]["minecraft:"+jp.Item]
	}
	return pt
}

// blockFor finds the block of a block particle that names none: the block at pos, else below
// (block dust, digging, sliding), or above for falling dust (it falls from the block's underside).
func (p *Player) blockFor(name string, pos mgl64.Vec3) (uint32, bool) {
	bp := blockPosOf(pos)
	try := []cube.Pos{bp, bp.Side(cube.FaceDown)}
	if name == "falling_dust" {
		try = []cube.Pos{bp, bp.Side(cube.FaceUp)}
	}
	for _, at := range try {
		if bn, st, ok := p.javaBlockAt(at); ok && bn != "air" && bn != "cave_air" && bn != "void_air" {
			return st, true
		}
	}
	return 0, false
}

// legacyParticle shows a Bedrock particle type (AddParticle level event, ParticleGenericSpawn).
func (p *Player) legacyParticle(id int32, pos mgl64.Vec3, data int32) {
	jp, ok := particles.Legacy(id)
	if !ok {
		p.unknownSound("particle type", strconv.Itoa(int(id)))
		return
	}
	pt := p.tableParticle(jp, pos, 0)
	switch pt.name {
	case "block", "block_marker", "dust_pillar", "block_crumble":
		if jp.Block != "" {
			break
		}
		st, ok := p.javaStateOfRID(data)
		if !ok {
			if st, ok = p.blockFor(pt.name, pos); !ok {
				return
			}
		}
		pt.state = st
	case "falling_dust":
		// Bedrock gives the dust's colour; Java the block: the block it falls from.
		st, ok := p.blockFor(pt.name, pos)
		if !ok {
			pt = particle{name: "dust", color: data | -0x1000000}
			break
		}
		pt.state = st
	case "dust", "dust_color_transition":
		if data != 0 {
			pt.color = data | -0x1000000
		}
	case "entity_effect", "effect", "instant_effect", "tinted_leaves":
		if data != 0 {
			pt.color = data
			if pt.color&-0x1000000 == 0 {
				pt.color |= -0x1000000
			}
		}
	case "note":
		p.sendParticle(pt, pos, mgl64.Vec3{float64(data%25) / 24, 0, 0}, 1, 0)
		return
	case "item":
		if jp.Item != "" {
			break
		}
		it, ok := p.itemOfData(data)
		if !ok {
			return
		}
		pt.item = it
		p.sendParticle(pt, pos, mgl64.Vec3{0.1, 0.1, 0.1}, 0.08, 1)
		return
	}
	p.sendParticle(pt, pos, mgl64.Vec3{}, 0, 1)
}

// molangColour is the variable.color a particle effect was spawned with (ARGB), 0 if none.
func molangColour(raw []byte) int32 {
	if len(raw) == 0 {
		return 0
	}
	var vars []struct {
		Name  string `json:"name"`
		Value struct {
			Type  string `json:"type"`
			Value json.RawMessage
		} `json:"value"`
	}
	if json.Unmarshal(raw, &vars) != nil {
		return 0
	}
	for _, v := range vars {
		if v.Name != "variable.color" && v.Name != "variable.colour" {
			continue
		}
		var members []struct {
			Name  string `json:"name"`
			Value struct {
				Value float64 `json:"value"`
			} `json:"value"`
		}
		if json.Unmarshal(v.Value.Value, &members) != nil {
			return 0
		}
		c := map[string]float64{".r": 1, ".g": 1, ".b": 1, ".a": 1}
		for _, m := range members {
			c[m.Name] = m.Value.Value
		}
		b := func(f float64) int32 { return int32(math.Round(max(0, min(1, f)) * 255)) }
		return b(c[".a"])<<24 | b(c[".r"])<<16 | b(c[".g"])<<8 | b(c[".b"])
	}
	return 0
}

// waxParticle is the Java particle of Bedrock's wax_particle by its colour: Bedrock draws wax on,
// wax off and scrape (copper oxidation removed) with one effect in three colours.
func waxParticle(argb int32) string {
	r, g, b := argb>>16&0xff, argb>>8&0xff, argb&0xff
	switch {
	case argb == 0:
		return "wax_on"
	case r > 200 && g > 200 && b > 200:
		return "wax_off"
	case g > r:
		return "scrape"
	}
	return "wax_on"
}

func (p *Player) spawnParticleEffect(pk *packet.SpawnParticleEffect) {
	if p.rem == nil {
		return
	}
	jp, ok := particles.Named(pk.ParticleName)
	if !ok {
		n := trimNS(pk.ParticleName)
		if !strings.HasPrefix(pk.ParticleName, "minecraft:") || v777.BuiltinID("minecraft:particle_type", "minecraft:"+n) < 0 {
			p.unknownSound("particle", pk.ParticleName) // add-on particles: no Java equal
			return
		}
		jp = particles.Particle{Java: n}
	}
	pos := vec64(pk.Position)
	if pk.EntityUniqueID != -1 {
		if e := p.spawnedByUnique(pk.EntityUniqueID); e != nil {
			pos = pos.Add(e.pos)
		} else if pk.EntityUniqueID == p.rem.gd.EntityUniqueID {
			pos = pos.Add(p.mv.pos)
		}
	}
	molang, _ := pk.MoLangVariables.Value()
	colour := molangColour(molang)
	pt := p.tableParticle(jp, pos, colour)
	switch pt.name {
	case "dust":
		if colour == 0 && jp.Colour == 0 {
			pt.color = -0x10000 // red, Bedrock's default redstone dust
		}
	case "wax_on":
		pt.name = waxParticle(colour)
	case "block", "block_marker", "falling_dust", "dust_pillar", "block_crumble":
		if jp.Block == "" {
			st, ok := p.blockFor(pt.name, pos)
			if !ok {
				return
			}
			pt.state = st
		}
	case "item":
		if pt.item == 0 {
			return // breaking_item_icon names its item in molang Java cannot read
		}
	}
	// Emitters are bursts on Bedrock; Java particles are single.
	if particles.Burst(pk.ParticleName) && !strings.Contains(pt.name, "emitter") && !strings.HasPrefix(pt.name, "explosion") {
		p.sendParticle(pt, pos, mgl64.Vec3{0.3, 0.3, 0.3}, 0.05, 8)
		return
	}
	p.sendParticle(pt, pos, mgl64.Vec3{}, 0, 1)
}

// levelEventGeneric shows the particle level events that carry their data as NBT: a block
// explosion's blocks, sculk charges, shrieks and vibrations.
func (p *Player) levelEventGeneric(pk *packet.LevelEventGeneric) {
	if p.rem == nil {
		return
	}
	switch pk.EventID {
	case packet.LevelEventParticlesBlockExplosion, packet.LevelEventSculkCharge, packet.LevelEventSculkChargePop,
		packet.LevelEventParticleSculkShriek, packet.LevelEventParticlesVibrationSignal:
	default:
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
	switch pk.EventID {
	case packet.LevelEventParticlesBlockExplosion:
		p.blockExplosion(m)
	case packet.LevelEventSculkCharge, packet.LevelEventSculkChargePop:
		bp, ok := nbtBlockPos(m, "x", "y", "z")
		if !ok {
			return
		}
		charge, _ := nbtNum(m["charge"])
		if pk.EventID == packet.LevelEventSculkChargePop || charge <= 0 {
			// Java's charge 0 event plays a sound the server sends itself: the pop particles alone.
			c := mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5}
			p.sendParticle(particle{name: "sculk_charge_pop"}, c, mgl64.Vec3{0.25, 0.25, 0.25}, 0.07, 20)
			return
		}
		facing, _ := nbtNum(m["facing"])
		p.sendLevelEvent(3006, bp, sculkChargeData(int32(charge), int32(facing)), false)
	case packet.LevelEventParticleSculkShriek:
		bp, ok := nbtBlockPos(m, "originX", "originY", "originZ")
		if !ok {
			return
		}
		for i := 0; i < 10; i++ {
			p.sendShriek(bp, i*5)
		}
	case packet.LevelEventParticlesVibrationSignal:
		origin, ok1 := nbtVec(m["origin"])
		target, ok2 := nbtVec(m["target"])
		if !ok1 || !ok2 {
			return
		}
		ttl := float64(1)
		if v, ok := m["timeToLive"].(float32); ok && v > 0 {
			ttl = float64(v)
		}
		p.sendParticle(particle{name: "vibration", dest: blockPosOf(target), ticks: int32(max(1, math.Round(ttl*20)))}, origin, mgl64.Vec3{}, 0, 1)
	}
}

// sculkChargeData is the Java PARTICLES_SCULK_CHARGE data (charge << 6 | the faces by Direction
// ordinal) of Bedrock's charge and facing bits (down 1, up 2, south 4, west 8, north 16, east 32).
func sculkChargeData(charge, facing int32) int32 {
	var faces int32
	for bit, dir := range [6]int32{0, 1, 3, 4, 2, 5} {
		if facing>>bit&1 != 0 {
			faces |= 1 << dir
		}
	}
	if faces == 63 {
		faces = 0 // a full block: Java draws all faces for 0
	}
	return charge<<6 | faces
}

func nbtBlockPos(m map[string]any, kx, ky, kz string) (cube.Pos, bool) {
	x, ok1 := nbtNum(m[kx])
	y, ok2 := nbtNum(m[ky])
	z, ok3 := nbtNum(m[kz])
	return cube.Pos{int(x), int(y), int(z)}, ok1 && ok2 && ok3
}

func nbtVec(v any) (mgl64.Vec3, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return mgl64.Vec3{}, false
	}
	x, ok1 := m["x"].(float32)
	y, ok2 := m["y"].(float32)
	z, ok3 := m["z"].(float32)
	return mgl64.Vec3{float64(x), float64(y), float64(z)}, ok1 && ok2 && ok3
}

// blockExplosion shows a block explosion's blocks (2026: the destroyed blocks' positions) with
// the small poofs Java's explosion block particles are.
func (p *Player) blockExplosion(m map[string]any) {
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
