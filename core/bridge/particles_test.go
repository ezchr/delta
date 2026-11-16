package bridge

import (
	"math"
	"strconv"
	"testing"
	"time"

	"delta/core/internal/blocks"
	"delta/core/internal/javamap"

	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Particles: Bedrock level events and particle effects through the translator, the Java packets
// read as a 26.3 and a 26.2 client reads them.

var bothVersions = []*version.Version{version.Newest, version.V776}

// javaParticlePk is a decoded level_particles packet.
type javaParticlePk struct {
	name  string
	state int32 // block particles: the version's block state
	item  int32 // item particles: the version's item id
	color int32
	to    int32 // dust_color_transition
	pos   [3]float64
	dist  [3]float64
	speed float32
	count int32
}

func particleName(v *version.Version, id int32) string {
	for k, i := range v777.Builtin["minecraft:particle_type"] {
		if version.Map(v.BuiltinTable("minecraft:particle_type"), i) == id {
			return trimNS(k)
		}
	}
	return "?"
}

// decodeAnyParticle reads level_particles with the options of the particle types the bridge sends.
func decodeAnyParticle(t *testing.T, v *version.Version, o outPacket) javaParticlePk {
	t.Helper()
	if o.id != v777.ClientboundPlayLevelParticles {
		t.Fatalf("packet %d is no level_particles", o.id)
	}
	r := wire.NewReader(o.w.B)
	var pk javaParticlePk
	opts := func() {
		pk.name = particleName(v, r.VarInt())
		switch pk.name {
		case "block", "block_marker", "falling_dust", "dust_pillar", "block_crumble":
			pk.state = r.VarInt()
		case "dust":
			pk.color = r.Int32()
			r.Float32()
		case "dust_color_transition":
			pk.color, pk.to = r.Int32(), r.Int32()
			r.Float32()
		case "entity_effect", "tinted_leaves", "flash":
			pk.color = r.Int32()
		case "effect", "instant_effect":
			pk.color = r.Int32()
			r.Float32()
		case "dragon_breath", "sculk_charge":
			r.Float32()
		case "shriek":
			r.VarInt()
		case "item":
			pk.item = r.VarInt()
			r.VarInt()
			r.VarInt()
			r.VarInt()
		case "vibration":
			r.VarInt()
			r.Int64()
			r.VarInt()
		}
	}
	if v.Native() {
		opts()
	}
	r.Bool()
	r.Bool()
	pk.pos = [3]float64{r.Float64(), r.Float64(), r.Float64()}
	pk.dist = [3]float64{float64(r.Float32()), float64(r.Float32()), float64(r.Float32())}
	if v.Native() {
		pk.speed = r.Float32()
		r.Float32()
		r.Float32()
		pk.count = r.VarInt()
		r.VarInt()
	} else {
		pk.speed = r.Float32()
		pk.count = r.Int32()
		opts()
	}
	if r.Err != nil || r.Off != len(r.B) {
		t.Fatalf("bad particle packet %s: %v (%d of %d)", pk.name, r.Err, r.Off, len(r.B))
	}
	return pk
}

// javaLevelEventPk is a decoded level_event packet.
type javaLevelEventPk struct {
	typ  int32
	pos  cube.Pos
	data int32
}

func decodeLevelEvent(t *testing.T, o outPacket) javaLevelEventPk {
	t.Helper()
	if o.id != v777.ClientboundPlayLevelEvent {
		t.Fatalf("packet %d is no level_event", o.id)
	}
	r := wire.NewReader(o.w.B)
	var e javaLevelEventPk
	e.typ = r.Int32()
	x, y, z := r.Position()
	e.pos = cube.Pos{int(x), int(y), int(z)}
	e.data = r.Int32()
	r.Bool()
	if r.Err != nil || r.Off != len(r.B) {
		t.Fatalf("bad level event: %v", r.Err)
	}
	return e
}

func javaBlockName(v *version.Version, state int32, want uint32) bool {
	return state == v.BlockState(int32(want))
}

// TestParticleBlockBreak: break, no-sound break and crack particles carry the block's Java state.
func TestParticleBlockBreak(t *testing.T) {
	for _, v := range bothVersions {
		p, take := soundPlayer(t, v)
		stairs := rid(t, p, "minecraft:oak_stairs")
		want, ok := p.javaStateOfRID(stairs)
		if jb := blocks.JavaBlockOf(want); !ok || jb == nil || jb.Name != "minecraft:oak_stairs" {
			t.Fatalf("%s: oak stairs state %d %v", v.Name, want, jb)
		}
		at := mgl32.Vec3{10.5, 64.5, -3.5}
		bp := cube.Pos{10, 64, -4}

		// Another player breaks it: 2001 (particles and the break sound) on both.
		p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesDestroyBlock, Position: at, EventData: stairs})
		out := take()
		if len(out) != 1 {
			t.Fatalf("%s: break: %d packets", v.Name, len(out))
		}
		if e := decodeLevelEvent(t, out[0]); e.typ != 2001 || e.pos != bp || e.data != v.BlockState(int32(want)) {
			t.Errorf("%s: break %+v", v.Name, e)
		}

		// Without the sound: 26.3's 2014; 26.2 has none, so the particles.
		p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesDestroyBlockNoSound, Position: at, EventData: stairs})
		out = take()
		if len(out) != 1 {
			t.Fatalf("%s: no-sound break: %d packets", v.Name, len(out))
		}
		if v.Native() {
			if e := decodeLevelEvent(t, out[0]); e.typ != 2014 || e.pos != bp || e.data != int32(want) {
				t.Errorf("no-sound break %+v", e)
			}
		} else if pk := decodeAnyParticle(t, v, out[0]); pk.name != "block" || !javaBlockName(v, pk.state, want) || pk.count < 16 {
			t.Errorf("%s: no-sound break %+v", v.Name, pk)
		}

		// Cracking, BDS's two forms: 2014 rid + face<<24, and 3603+face with the rid.
		for _, c := range []struct {
			ev, data, face int32
		}{
			{packet.LevelEventParticlesCrackBlock, stairs + 3<<24, 3},
			{packet.LevelEventParticlesCrackBlock, stairs, 0},
			{packet.LevelEventParticlesCrackBlockNorth, stairs, 2},
			{packet.LevelEventParticlesCrackBlockEast, stairs, 5},
		} {
			p.levelEventParticles(&packet.LevelEvent{EventType: c.ev, Position: at, EventData: c.data})
			out := take()
			if len(out) != 1 {
				t.Fatalf("%s: crack %d: %d packets", v.Name, c.ev, len(out))
			}
			if v.Native() {
				if e := decodeLevelEvent(t, out[0]); e.typ != 2019 || e.pos != bp || e.data != c.face {
					t.Errorf("crack %d face %d: %+v", c.ev, c.face, e)
				}
				continue
			}
			pk := decodeAnyParticle(t, v, out[0])
			n := faceNormals[c.face]
			wantPos := [3]float64{10.5 + n[0]*0.55, 64.5 + n[1]*0.55, -3.5 + n[2]*0.55}
			if pk.name != "block" || !javaBlockName(v, pk.state, want) || pk.count != 1 || math.Abs(pk.pos[0]-wantPos[0]) > 1e-9 ||
				math.Abs(pk.pos[1]-wantPos[1]) > 1e-9 || math.Abs(pk.pos[2]-wantPos[2]) > 1e-9 {
				t.Errorf("%s: crack face %d: %+v, want at %v", v.Name, c.face, pk, wantPos)
			}
		}

		// The client's own digging: it draws its cracks itself.
		p.sounds().dug[bp] = time.Now()
		p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesCrackBlockUp, Position: at, EventData: stairs})
		if out := take(); len(out) != 0 {
			t.Errorf("%s: own crack: %d packets", v.Name, len(out))
		}
	}
}

// TestParticlePotionColour: a splash potion's ARGB colour reaches Java as its colour.
func TestParticlePotionColour(t *testing.T) {
	const argb = int32(-0x00c7a23a) // 0xff385dc6 (regeneration-ish)
	for _, v := range bothVersions {
		p, take := soundPlayer(t, v)
		p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesPotionSplash, Position: mgl32.Vec3{5.5, 70, 5.5}, EventData: argb})
		out := take()
		if v.Native() {
			if len(out) != 1 {
				t.Fatalf("splash: %d packets", len(out))
			}
			if e := decodeLevelEvent(t, out[0]); e.typ != 2002 || e.data != 0x385dc6 || e.pos != (cube.Pos{5, 70, 5}) {
				t.Errorf("splash %+v", e)
			}
			continue
		}
		// 26.2: the bottle shards and the coloured ring as particles (its 2002 plays a second sound).
		if len(out) != 2 {
			t.Fatalf("%s: splash: %d packets", v.Name, len(out))
		}
		shards, ring := decodeAnyParticle(t, v, out[0]), decodeAnyParticle(t, v, out[1])
		if shards.name != "item" || shards.item != v.Builtin("minecraft:item", v777.Builtin["minecraft:item"]["minecraft:splash_potion"]) {
			t.Errorf("%s: shards %+v", v.Name, shards)
		}
		if ring.name != "effect" || ring.color != argb || ring.count != 100 {
			t.Errorf("%s: ring %+v", v.Name, ring)
		}
	}
	if c := potionColour(0x7f00ff00); c != 0x00ff00 {
		t.Errorf("potion colour %#x", c)
	}
}

// TestParticleGenericSpawn: ParticleGenericSpawn's type and item data (snowball and egg hits,
// villager reactions, the splash potion's bottle).
func TestParticleGenericSpawn(t *testing.T) {
	for _, v := range bothVersions {
		p, take := soundPlayer(t, v)
		p.rem.gd.Items = []protocol.ItemEntry{{Name: "minecraft:egg", RuntimeID: 400}}
		egg, _ := javamap.ItemID("minecraft:egg", 0)
		for _, c := range []struct {
			data int32
			want string
			item int32
		}{
			{packet.ParticleTypeVillagerHappy, "happy_villager", 0},
			{packet.ParticleTypeVillagerAngry, "angry_villager", 0},
			{packet.ParticleTypeSnowballPoof, "item_snowball", 0},
			{400<<16 | packet.ParticleTypeIconCrack, "item", egg},
			{packet.ParticleTypeLargeExplode, "explosion", 0},
		} {
			p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticleGenericSpawn, Position: mgl32.Vec3{1, 65, 1}, EventData: c.data})
			out := take()
			if len(out) != 1 {
				t.Fatalf("%s: generic %d: %d packets", v.Name, c.data, len(out))
			}
			pk := decodeAnyParticle(t, v, out[0])
			if pk.name != c.want || c.item != 0 && pk.item != v.Builtin("minecraft:item", c.item) {
				t.Errorf("%s: generic %#x: %+v, want %s", v.Name, c.data, pk, c.want)
			}
		}
	}
}

// TestParticleNamed: named particle effects, their fallbacks, molang colours and add-on ones.
func TestParticleNamed(t *testing.T) {
	colour := func(r, g, b float64) protocol.Optional[[]byte] {
		return protocol.Option([]byte(`[{"name":"variable.color","value":{"type":"member_array","value":[` +
			`{"name":".r","value":{"type":"float","value":` + ftoa(r) + `}},{"name":".g","value":{"type":"float","value":` + ftoa(g) +
			`}},{"name":".b","value":{"type":"float","value":` + ftoa(b) + `}},{"name":".a","value":{"type":"float","value":1.0}}]}}]`))
	}
	for _, v := range bothVersions {
		p, take := soundPlayer(t, v)
		for _, c := range []struct {
			name   string
			molang protocol.Optional[[]byte]
			want   string
			color  int32
			count  int32
		}{
			{"minecraft:villager_happy", protocol.Optional[[]byte]{}, "happy_villager", 0, 1},
			{"minecraft:totem_particle", protocol.Optional[[]byte]{}, "totem_of_undying", 0, 1},
			{"minecraft:redstone_wire_dust_particle", protocol.Optional[[]byte]{}, "dust", -0x10000, 1},
			{"minecraft:critical_hit_emitter", protocol.Optional[[]byte]{}, "crit", 0, 8},
			{"minecraft:wax_particle", colour(0.91, 0.55, 0.08), "wax_on", 0, 1},
			{"minecraft:wax_particle", colour(1, 0.9, 1), "wax_off", 0, 1},
			{"minecraft:wax_particle", colour(0.29, 0.58, 0.51), "scrape", 0, 1},
			{"minecraft:mobspell_lingering", colour(1, 0, 0), "entity_effect", -0x10000, 1},
		} {
			p.spawnParticleEffect(&packet.SpawnParticleEffect{ParticleName: c.name, Position: mgl32.Vec3{0, 70, 0}, EntityUniqueID: -1, MoLangVariables: c.molang})
			out := take()
			if len(out) != 1 {
				t.Fatalf("%s: %s: %d packets", v.Name, c.name, len(out))
			}
			pk := decodeAnyParticle(t, v, out[0])
			if pk.name != c.want || c.color != 0 && pk.color != c.color || pk.count != c.count {
				t.Errorf("%s: %s: %+v, want %s %#x x%d", v.Name, c.name, pk, c.want, c.color, c.count)
			}
		}
		// Sculk sensor redstone: dust fading from sculk to redstone.
		p.spawnParticleEffect(&packet.SpawnParticleEffect{ParticleName: "minecraft:sculk_sensor_redstone_particle", EntityUniqueID: -1})
		if pk := decodeAnyParticle(t, v, take()[0]); pk.name != "dust_color_transition" || uint32(pk.color) != 0xff39d6e0 || uint32(pk.to) != 0xffff0000 {
			t.Errorf("%s: sculk redstone %+v", v.Name, pk)
		}
		// Add-on particles have no Java equal.
		p.spawnParticleEffect(&packet.SpawnParticleEffect{ParticleName: "myaddon:sparkle", EntityUniqueID: -1})
		if out := take(); len(out) != 0 {
			t.Errorf("%s: add-on particle: %d packets", v.Name, len(out))
		}
	}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// TestParticleLevelEvents: level events Java draws with a level event of its own or particles.
func TestParticleLevelEvents(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	// Trial spawner detection with 2 players: 30 + 2*5 particles at the block's centre.
	p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesTrialSpawnerDetection, Position: mgl32.Vec3{4, 60, 4}, EventData: 2})
	if pk := decodeAnyParticle(t, version.Newest, take()[0]); pk.name != "trial_spawner_detection" || pk.count != 40 || pk.pos != [3]float64{4.5, 60.5, 4.5} {
		t.Errorf("trial spawner detection %+v", pk)
	}
	// Dripstone drip: Java's own drip particle event.
	p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesDripstoneDrip, Position: mgl32.Vec3{2.5, 80.5, 2.5}})
	if e := decodeLevelEvent(t, take()[0]); e.typ != 1504 || e.pos != (cube.Pos{2, 80, 2}) {
		t.Errorf("drip %+v", e)
	}
	// Dragon egg: 3 blocks east and 1 down of the start (data: the start minus the destination).
	p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesDragonEgg, Position: mgl32.Vec3{0, 64, 0}, EventData: 1<<24 | 3<<16 | 1<<8})
	out := take()
	if len(out) != 17 {
		t.Fatalf("dragon egg: %d packets", len(out))
	}
	if first, last := decodeAnyParticle(t, version.Newest, out[0]), decodeAnyParticle(t, version.Newest, out[16]); first.pos != [3]float64{0.5, 64.5, 0.5} || last.pos != [3]float64{3.5, 63.5, 0.5} {
		t.Errorf("dragon egg path %v -> %v", first.pos, last.pos)
	}
	// Squid ink on a squid fleeing.
	p.ents.shown[7] = &entity{id: 70, spawned: true, bedrock: "minecraft:glow_squid"}
	p.actorEventParticles(&packet.ActorEvent{EntityRuntimeID: 7, EventType: packet.ActorEventSquidFleeing})
	if pk := decodeAnyParticle(t, version.Newest, take()[0]); pk.name != "glow_squid_ink" || pk.count != 30 {
		t.Errorf("squid ink %+v", pk)
	}
}

func TestSculkChargeData(t *testing.T) {
	for _, c := range []struct{ charge, facing, want int32 }{
		{3, 1, 3<<6 | 1},          // down
		{1, 2 | 16, 1<<6 | 2 | 4}, // up, north
		{2, 4 | 8 | 32, 2<<6 | 8 | 16 | 32},
		{5, 63, 5 << 6}, // every face: Java's full-block 0
	} {
		if got := sculkChargeData(c.charge, c.facing); got != c.want {
			t.Errorf("charge %d facing %b: %b, want %b", c.charge, c.facing, got, c.want)
		}
	}
}
