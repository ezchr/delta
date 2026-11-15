package bridge

import (
	"math"
	"testing"
	"time"

	"javabridge/core/internal/blocks"
	"javabridge/core/internal/sounds"

	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/version"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Sounds and particles: Bedrock packets as BDS 1.26.60 sends them (recorded with BB_DEBUG_SOUNDS)
// through the translator, the Java packets decoded as a 26.3 (and 26.2) client reads them.

// soundPlayer is a player on a fake connection whose Java packets the test takes.
func soundPlayer(t *testing.T, v *version.Version) (*Player, func() []outPacket) {
	t.Helper()
	pal, err := blocks.NewPalette(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	p := &Player{log: cfg.Log}
	p.java = &JavaConn{log: cfg.Log, ver: v, wake: make(chan struct{}, 1), closed: make(chan struct{})}
	p.java.writers.New = func() any { return &wire.Writer{} }
	p.rem = &Remote{p: p, gd: minecraft.GameData{EntityUniqueID: -100, EntityRuntimeID: 100, UseBlockNetworkIDHashes: true}, pal: pal}
	p.ents = newEntities(p)
	p.win = newWindows(p)
	take := func() []outPacket {
		p.java.outMu.Lock()
		defer p.java.outMu.Unlock()
		out := p.java.out
		p.java.out = nil
		return out
	}
	return p, take
}

// rid is the server runtime id (network hash) of a Bedrock block in its default state.
func rid(t *testing.T, p *Player, name string) int32 {
	t.Helper()
	props, ok := blocks.Registry{P: p.rem.pal}.DefaultProperties(name)
	if !ok {
		t.Fatalf("no %s", name)
	}
	i, ok := p.rem.pal.Lookup(name, props)
	if !ok {
		t.Fatalf("no %s %v", name, props)
	}
	return int32(p.rem.pal.IndexToServer(i))
}

func soundName(id int32) string {
	for k, v := range v777.Builtin["minecraft:sound_event"] {
		if v == id {
			return trimNS(k)
		}
	}
	return "?"
}

type javaSoundPk struct {
	name     string
	src      int32
	entity   int32 // sound_entity
	pos      [3]float64
	vol, pit float32
}

func decodeSound(t *testing.T, o outPacket) javaSoundPk {
	t.Helper()
	r := wire.NewReader(o.w.B)
	var s javaSoundPk
	id := r.VarInt()
	if id == 0 {
		s.name = "direct:" + r.String(32767)
		r.Bool()
	} else {
		s.name = soundName(id - 1)
	}
	s.src = r.VarInt()
	if o.id == v777.ClientboundPlaySoundEntity {
		s.entity = r.VarInt()
	} else {
		s.pos = [3]float64{float64(r.Int32()) / 8, float64(r.Int32()) / 8, float64(r.Int32()) / 8}
	}
	s.vol, s.pit = r.Float32(), r.Float32()
	r.Int64()
	if r.Err != nil || len(r.B) != r.Off {
		t.Fatalf("bad sound packet: %v (%d of %d bytes)", r.Err, r.Off, len(r.B))
	}
	return s
}

func onlySound(t *testing.T, out []outPacket, what string) javaSoundPk {
	t.Helper()
	if len(out) != 1 || (out[0].id != v777.ClientboundPlaySound && out[0].id != v777.ClientboundPlaySoundEntity) {
		t.Fatalf("%s: want one sound, got %d packets", what, len(out))
	}
	return decodeSound(t, out[0])
}

func TestSoundsBlocks(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	door := rid(t, p, "minecraft:wooden_door")
	at := mgl32.Vec3{-49.5, 220.5, 52.5}

	// Another player opens a door.
	p.levelSound(&packet.LevelSoundEvent{SoundType: "door.open", Position: at, ExtraData: door, EntityUniqueID: -1})
	s := onlySound(t, take(), "door")
	if s.name != "block.wooden_door.open" || s.src != srcBlocks || s.pos != [3]float64{-49.5, 220.5, 52.5} || s.vol != 1 || s.pit < 0.9 || s.pit > 1 {
		t.Errorf("door: %+v", s)
	}
	// The player opened it: the Java client played it.
	p.sounds().used[cube.Pos{-50, 220, 52}] = time.Now()
	p.levelSound(&packet.LevelSoundEvent{SoundType: "door.close", Position: at, ExtraData: door, EntityUniqueID: -1})
	if out := take(); len(out) != 0 {
		t.Errorf("own door: %d packets", len(out))
	}
	// Other block sets.
	for _, c := range []struct{ ev, block, want string }{
		{"trapdoor.open", "minecraft:iron_trapdoor", "block.iron_trapdoor.open"},
		{"fence_gate.close", "minecraft:cherry_fence_gate", "block.cherry_wood_fence_gate.close"},
		{"fence_gate.open", "minecraft:fence_gate", "block.fence_gate.open"},
		{"chest.open", "minecraft:chest", "block.chest.open"},
		{"chest.closed", "minecraft:chest", "block.chest.close"},
		{"barrel.open", "minecraft:barrel", "block.barrel.open"},
		{"button.click_on", "minecraft:stone_button", "block.stone_button.click_on"},
		{"button.click_on", "minecraft:lever", "block.lever.click"},
		{"place", "minecraft:oak_planks", "block.wood.place"},
		{"hit", "minecraft:dirt", "block.gravel.hit"},
	} {
		p.levelSound(&packet.LevelSoundEvent{SoundType: c.ev, Position: mgl32.Vec3{10.5, 64.5, 10.5}, ExtraData: rid(t, p, c.block), EntityUniqueID: -1})
		if s := onlySound(t, take(), c.ev); s.name != c.want {
			t.Errorf("%s %s: %s, want %s", c.ev, c.block, s.name, c.want)
		}
	}
	// Block sound volume and pitch as Java's (stone: 1, 1).
	p.levelSound(&packet.LevelSoundEvent{SoundType: "place", Position: mgl32.Vec3{3.5, 64.5, 3.5}, ExtraData: rid(t, p, "minecraft:stone"), EntityUniqueID: -1})
	if s := onlySound(t, take(), "stone"); s.name != "block.stone.place" || s.vol != 1 || math.Abs(float64(s.pit)-0.8) > 1e-6 {
		t.Errorf("stone place: %+v", s)
	}
	// The client's own digging: no hit sounds, no destroy particles.
	p.sounds().dug[cube.Pos{3, 64, 3}] = time.Now()
	p.levelSound(&packet.LevelSoundEvent{SoundType: "hit", Position: mgl32.Vec3{3.5, 64.5, 3.5}, ExtraData: rid(t, p, "minecraft:stone"), EntityUniqueID: -1})
	p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesDestroyBlock, Position: mgl32.Vec3{3.5, 64.5, 3.5}, EventData: rid(t, p, "minecraft:stone")})
	if out := take(); len(out) != 0 {
		t.Errorf("own digging: %d packets", len(out))
	}
	// Another player's block break: Java level event 2001 with the Java state.
	dirt := rid(t, p, "minecraft:dirt")
	p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesDestroyBlock, Position: mgl32.Vec3{-51.5, 220.5, 54.5}, EventData: dirt})
	out := take()
	if len(out) != 1 || out[0].id != v777.ClientboundPlayLevelEvent {
		t.Fatalf("break: %d packets", len(out))
	}
	r := wire.NewReader(out[0].w.B)
	typ := r.Int32()
	x, y, z := r.Position()
	data := r.Int32()
	if jb := blocks.JavaBlockOf(uint32(data)); typ != 2001 || x != -52 || y != 220 || z != 54 || jb == nil || jb.Name != "minecraft:dirt" {
		t.Errorf("break: event %d at %d %d %d data %d", typ, x, y, z, data)
	}
}

func TestSoundsNoteBlock(t *testing.T) {
	for _, v := range []*version.Version{version.Newest, version.V776} {
		p, take := soundPlayer(t, v)
		// BDS: note, ExtraData instrument<<8 | note, at the block's corner.
		p.levelSound(&packet.LevelSoundEvent{SoundType: "note", Position: mgl32.Vec3{-54, 220, 52}, ExtraData: 1<<8 | 1, EntityUniqueID: -1})
		out := take()
		if len(out) != 2 || out[0].id != v777.ClientboundPlaySound || out[1].id != v777.ClientboundPlayLevelParticles {
			t.Fatalf("%s: %d packets", v.Name, len(out))
		}
		if v.Native() {
			s := decodeSound(t, out[0])
			if s.name != "block.note_block.basedrum" || s.src != srcRecords || s.vol != 3 || math.Abs(float64(s.pit)-math.Pow(2, -11.0/12)) > 1e-6 || s.pos != [3]float64{-53.5, 220.5, 52.5} {
				t.Errorf("note: %+v", s)
			}
		}
		name, pos, dist, n := decodeParticle(t, v, out[1])
		if name != "note" || n != 0 || math.Abs(dist[0]-1.0/24) > 1e-6 || pos[1] != 221.2 {
			t.Errorf("%s: note particle %s at %v dist %v count %d", v.Name, name, pos, dist, n)
		}
	}
}

// decodeParticle reads level_particles (26.3 or 26.2 layout) of a simple particle type.
func decodeParticle(t *testing.T, v *version.Version, o outPacket) (string, [3]float64, [3]float64, int32) {
	t.Helper()
	r := wire.NewReader(o.w.B)
	pname := func(id int32) string {
		for k, i := range v777.Builtin["minecraft:particle_type"] {
			if version.Map(v.BuiltinTable("minecraft:particle_type"), i) == id {
				return trimNS(k)
			}
		}
		return "?"
	}
	var name string
	if v.Native() {
		name = pname(r.VarInt())
	}
	r.Bool()
	r.Bool()
	pos := [3]float64{r.Float64(), r.Float64(), r.Float64()}
	dist := [3]float64{float64(r.Float32()), float64(r.Float32()), float64(r.Float32())}
	var n int32
	if v.Native() {
		r.Float32()
		r.Float32()
		r.Float32()
		n = r.VarInt()
		r.VarInt()
	} else {
		r.Float32()
		n = r.Int32()
		name = pname(r.VarInt())
	}
	if r.Err != nil || r.Off != len(r.B) {
		t.Fatalf("bad particle packet: %v (%d of %d)", r.Err, r.Off, len(r.B))
	}
	return name, pos, dist, n
}

func TestSoundsEntities(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	z := &entity{rid: 5, uniq: 5, id: 7, bedrock: "minecraft:zombie", java: "minecraft:zombie", t: entityTypes["minecraft:zombie"], spawned: true}
	p.ents.shown[5], p.ents.byJava[7], p.ents.byUnique[5] = z, z, 5

	// BDS sends no hurt sound: the ActorEvent makes it.
	p.actorEventSound(&packet.ActorEvent{EntityRuntimeID: 5, EventType: packet.ActorEventHurt, EventData: 2})
	if s := onlySound(t, take(), "hurt"); s.name != "entity.zombie.hurt" || s.src != srcHostile || s.entity != 7 {
		t.Errorf("hurt: %+v", s)
	}
	// The player's own attack on it: the hit sound too.
	p.sounds().attack.rid, p.sounds().attack.at = 5, time.Now()
	p.actorEventSound(&packet.ActorEvent{EntityRuntimeID: 5, EventType: packet.ActorEventHurt})
	out := take()
	if len(out) != 2 || decodeSound(t, out[0]).name != "entity.player.attack.strong" || decodeSound(t, out[0]).entity != SelfEntityID {
		t.Errorf("own attack: %d packets", len(out))
	}
	p.actorEventSound(&packet.ActorEvent{EntityRuntimeID: 5, EventType: packet.ActorEventDeath})
	p.actorEventSound(&packet.ActorEvent{EntityRuntimeID: 5, EventType: packet.ActorEventDeath})
	if s := onlySound(t, take(), "death"); s.name != "entity.zombie.death" {
		t.Errorf("death: %+v", s)
	}
	// A level sound of the entity follows it.
	p.levelSound(&packet.LevelSoundEvent{SoundType: "mad", EntityType: "minecraft:zombie", EntityUniqueID: 5})
	if out := take(); len(out) != 0 {
		t.Errorf("zombie mad: Java has none, got %d packets", len(out))
	}
	// Another player's hit.
	pl := &entity{rid: 6, uniq: 6, id: 8, bedrock: "minecraft:player", java: "minecraft:player", t: entityTypes["minecraft:player"], spawned: true}
	p.ents.shown[6], p.ents.byJava[8], p.ents.byUnique[6] = pl, pl, 6
	p.levelSound(&packet.LevelSoundEvent{SoundType: "attack.strong", EntityType: "minecraft:player", EntityUniqueID: 6})
	if s := onlySound(t, take(), "attack"); s.name != "entity.player.attack.strong" || s.entity != 8 || s.src != srcPlayers {
		t.Errorf("other player's hit: %+v", s)
	}
	// Own splash: the client's own; spawn sounds: none on Java.
	p.levelSound(&packet.LevelSoundEvent{SoundType: "splash", EntityType: "minecraft:player", EntityUniqueID: -100, ExtraData: 5080500})
	p.levelSound(&packet.LevelSoundEvent{SoundType: "spawn", EntityType: "minecraft:zombie", EntityUniqueID: 5})
	p.levelSound(&packet.LevelSoundEvent{SoundType: "block.campfire.crackle", Position: mgl32.Vec3{1, 2, 3}, ExtraData: -1, EntityUniqueID: -1})
	if out := take(); len(out) != 0 {
		t.Errorf("dropped sounds: %d packets", len(out))
	}
	// Explosions.
	p.levelSound(&packet.LevelSoundEvent{SoundType: "explode", Position: mgl32.Vec3{-56, 220, 58}, ExtraData: -1, EntityUniqueID: -1})
	if s := onlySound(t, take(), "explode"); s.name != "entity.generic.explode" || s.vol != 4 {
		t.Errorf("explode: %+v", s)
	}
	p.levelEventParticles(&packet.LevelEvent{EventType: packet.LevelEventParticlesExplosion, Position: mgl32.Vec3{-56, 220, 58}, EventData: 4})
	if out := take(); len(out) != 1 {
		t.Errorf("explosion particle: %d packets", len(out))
	} else if name, _, _, _ := decodeParticle(t, version.Newest, out[0]); name != "explosion_emitter" {
		t.Errorf("explosion particle %s", name)
	}
	// PlaySound.
	p.playSound(&packet.PlaySound{SoundName: "random.levelup", Position: mgl32.Vec3{1, 2, 3}, Volume: 0.5, Pitch: 1.2})
	if s := onlySound(t, take(), "playsound"); s.name != "entity.player.levelup" || s.vol != 0.5 || s.pit != 1.2 {
		t.Errorf("playsound: %+v", s)
	}
	p.playSound(&packet.PlaySound{SoundName: "custom.Pack:Boom", Position: mgl32.Vec3{1, 2, 3}, Volume: 1, Pitch: 1})
	if s := onlySound(t, take(), "custom"); s.name != "direct:custom.pack:boom" {
		t.Errorf("resource pack sound: %+v", s)
	}
}

func TestSoundTables(t *testing.T) {
	if st := sounds.Block("oak_planks", 0); st.Break != "block.wood.break" {
		t.Errorf("oak planks: %+v", st)
	}
	if st := sounds.Block("glass", 0); st.Break != "block.glass.break" {
		t.Errorf("glass: %+v", st)
	}
	for _, c := range []struct{ java, ev, want string }{
		{"zombie", "ambient", "entity.zombie.ambient"}, {"cow", "step", "entity.cow.step"},
		{"mooshroom", "hurt", "entity.cow.hurt"}, {"cat", "ambient.baby", "entity.baby_cat.ambient"},
		{"horse", "land", "entity.horse.land"}, {"zombie", "attack", ""}, {"polar_bear", "mob.warning", "entity.polar_bear.warning"},
		{"creeper", "fuse", "entity.creeper.primed"}, {"player", "attack.strong", "entity.player.attack.strong"},
	} {
		if got := entitySound(c.java, c.ev); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.java, c.ev, got, c.want)
		}
	}
	if !sounds.Hostile("zombie") || sounds.Hostile("cow") {
		t.Error("hostile table")
	}
	for name, id := range map[string]int32{"heart": packet.ParticleTypeHeart, "happy_villager": packet.ParticleTypeVillagerHappy,
		"angry_villager": packet.ParticleTypeVillagerAngry, "block": packet.ParticleTypeTerrain, "note": packet.ParticleTypeNote} {
		if got, _ := sounds.LegacyParticle(int32(id)); got != name {
			t.Errorf("particle type %d: %q, want %q", id, got, name)
		}
	}
	if got, _ := sounds.NamedParticle("minecraft:heart_particle"); got != "heart" {
		t.Errorf("heart_particle: %q", got)
	}
}
