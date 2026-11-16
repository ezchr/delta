package bridge

import (
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"delta/core/internal/blocks"
	"delta/core/internal/sounds"

	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Bedrock sounds -> Java sound / sound_entity / stop_sound.
//
// What BDS sends (seen with JB_DEBUG_SOUNDS, see sounds_trace.go): LevelSoundEvent (a string event
// such as "door.open", with the block runtime id in ExtraData for block sounds, or the entity
// type and unique id for entity sounds), PlaySound (named sounds, /playsound and scripts),
// sound-type LevelEvents, and LevelEvent 2001 for a broken block (particles and the break sound,
// as Java's level event 2001). It does NOT send what a Bedrock client makes up itself: mob ambient
// sounds, hurt and death sounds (the client plays them on ActorEvent hurt/death), footsteps of
// other players and mobs, eating sounds and the player's own attack sound. Those are made here
// the way a Java server makes them (Mob.baseTick ambient timer, Entity.move step distance,
// Consumable use ticks), so the Java client hears what it would on a Java server.
//
// The Java client plays some sounds itself and must not get them again: its own steps, block
// placing, digging (hit/break), door/trapdoor/fence gate/button use, eating, buckets, splashes;
// the item pickup sound (take_item_entity), totem and guardian sounds (entity events); ambient
// block sounds of its own animateTick (campfire, fire, lava, water, portal, furnaces...); biome
// and underwater ambience.

// Java sound sources (net.minecraft.sounds.SoundSource order).
const (
	srcMaster = iota
	srcMusic
	srcRecords
	srcWeather
	srcBlocks
	srcHostile
	srcNeutral
	srcPlayers
	srcAmbient
)

// soundState is the player's sound translation state (p.snd, p.mu).
type soundState struct {
	// used are blocks the Java client clicked (use_item_on) and the block next to it a placement
	// would fill; dug the blocks it dug (player_action). Their sounds and particles the client
	// made itself.
	used, dug map[cube.Pos]time.Time
	// attack is the entity the Java client last attacked (its hurt event makes the hit sound).
	attack struct {
		rid uint64
		at  time.Time
	}
	// orbTaken: an experience orb was picked up (the client plays that pickup sound itself).
	orbTaken time.Time
	// mobs is the made-up sound state of each shown entity by runtime id.
	mobs map[uint64]*mobSound
	// unknown are the events already logged as untranslated.
	unknown map[string]bool
}

// mobSound is what the Java server would keep per entity for its sounds.
type mobSound struct {
	ambient            int // Mob.ambientSoundTime
	last               mgl64.Vec3
	tracked            bool
	moveDist, nextStep float64
	useTicks           int    // ticks it has been using an item
	useItem            string // the Bedrock item it uses
	died               bool
}

func (p *Player) sounds() *soundState {
	s := &p.snd
	if s.mobs == nil {
		s.used, s.dug = map[cube.Pos]time.Time{}, map[cube.Pos]time.Time{}
		s.mobs, s.unknown = map[uint64]*mobSound{}, map[string]bool{}
	}
	return s
}

func init() {
	OnBedrock(packet.IDLevelSoundEvent, (*Player).levelSound)
	OnBedrock(packet.IDPlaySound, (*Player).playSound)
	OnBedrock(packet.IDStopSound, (*Player).stopSound)
	OnBedrock(packet.IDLevelEvent, (*Player).levelEventSound)
	OnBedrock(packet.IDActorEvent, (*Player).actorEventSound)
	OnBedrock(packet.IDTakeItemActor, func(p *Player, pk *packet.TakeItemActor) {
		if e, ok := p.ents.shown[pk.ItemEntityRuntimeID]; ok && e.bedrock == "minecraft:xp_orb" {
			p.sounds().orbTaken = time.Now()
		}
	})
	OnJava(v777.ServerboundPlayUseItemOn, func(p *Player, r *wireReader) error {
		r.VarInt()
		x, y, z := r.Position()
		face := r.VarInt()
		if r.Err != nil {
			return nil // interact.go reports it
		}
		s, now := p.sounds(), time.Now()
		pos := cube.Pos{x, y, z}
		s.used[pos] = now
		if face >= 0 && face <= 5 {
			s.used[pos.Side(cube.Face(face))] = now
		}
		return nil
	})
	OnJava(v777.ServerboundPlayPlayerAction, func(p *Player, r *wireReader) error {
		raw := r.VarInt()
		x, y, z := r.Position()
		if r.Err != nil {
			return nil
		}
		if a, ok := javaAction(p.ver().Protocol, raw); ok && a > digFinish {
			return nil // drops, swaps: no block
		}
		p.sounds().dug[cube.Pos{x, y, z}] = time.Now()
		return nil
	})
	OnJava(v777.ServerboundPlayAttack, func(p *Player, r *wireReader) error {
		target := r.VarInt()
		if r.Err != nil {
			return nil
		}
		if e, ok := p.ents.byJava[target]; ok && e.living() {
			s := p.sounds()
			s.attack.rid, s.attack.at = e.rid, time.Now()
		}
		return nil
	})
	OnTick((*Player).soundTick)
	OnReset(func(p *Player, newConn bool) {
		s := p.sounds()
		clear(s.used)
		clear(s.dug)
		clear(s.mobs)
		s.attack.rid = 0
	})
}

// ---- Java packets ----

// writeSoundHolder writes a Holder<SoundEvent>: the registry entry (id + 1), or 0 and the name for
// a sound the client's registry has not got (a resource pack's).
func (p *Player) writeSoundHolder(w *wire.Writer, name string) {
	if id := p.soundID(name); id >= 0 {
		w.VarInt(id + 1)
		return
	}
	w.VarInt(0)
	w.String(soundIdent(name))
	w.Bool(false) // no fixed range
}

// soundID is the client's registry id of a Java sound event (-1: none).
func (p *Player) soundID(name string) int32 {
	if name == "" {
		return -1
	}
	if !strings.Contains(name, ":") {
		name = "minecraft:" + name
	}
	return p.ver().BuiltinID("minecraft:sound_event", name)
}

// javaSound reports whether a Java sound event of this name exists (26.3).
func javaSound(name string) bool {
	return name != "" && v777.BuiltinID("minecraft:sound_event", "minecraft:"+name) >= 0
}

// soundIdent makes a valid resource location of a Bedrock sound name.
func soundIdent(name string) string {
	b := []byte(strings.ToLower(name))
	colon := false
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-', c == '.', c == '/':
		case c == ':' && !colon:
			colon = true
		default:
			b[i] = '_'
		}
	}
	if !colon {
		return "minecraft:" + string(b)
	}
	return string(b)
}

// sendSound plays a sound at a position (Java sound).
func (p *Player) sendSound(name string, src int32, pos mgl64.Vec3, volume, pitch float32) {
	if name == "" || !valid(pos[0]) || !valid(pos[1]) || !valid(pos[2]) {
		return
	}
	w := p.pk()
	p.writeSoundHolder(w, name)
	w.VarInt(src)
	w.Int32(int32(pos[0] * 8))
	w.Int32(int32(pos[1] * 8))
	w.Int32(int32(pos[2] * 8))
	w.Float32(volume)
	w.Float32(pitch)
	w.Int64(rand.Int64())
	p.send(v777.ClientboundPlaySound, w)
}

// sendSoundEntity plays a sound that follows an entity (Java sound_entity).
func (p *Player) sendSoundEntity(name string, src int32, entityID int32, volume, pitch float32) {
	if name == "" {
		return
	}
	w := p.pk()
	p.writeSoundHolder(w, name)
	w.VarInt(src)
	w.VarInt(entityID)
	w.Float32(volume)
	w.Float32(pitch)
	w.Int64(rand.Int64())
	p.send(v777.ClientboundPlaySoundEntity, w)
}

// sendStopSound stops sounds by name and/or source (src < 0: any source; name "": any sound).
func (p *Player) sendStopSound(src int32, name string) {
	w := p.pk()
	var flags byte
	if src >= 0 {
		flags |= 1
	}
	if name != "" {
		flags |= 2
	}
	w.Byte(flags)
	if src >= 0 {
		w.VarInt(src)
	}
	if name != "" {
		w.String(soundIdent(name))
	}
	p.send(v777.ClientboundPlayStopSound, w)
}

// ---- helpers ----

// rnd is random.nextFloat().
func rnd() float32 { return rand.Float32() }

// voicePitch is Mob.getVoicePitch().
func voicePitch(baby bool) float32 {
	if baby {
		return (rnd()-rnd())*0.2 + 1.5
	}
	return (rnd()-rnd())*0.2 + 1
}

// trimNS drops the minecraft: namespace.
func trimNS(s string) string { return strings.TrimPrefix(s, "minecraft:") }

// entityAliases are entity types that use another type's sounds.
var entityAliases = map[string]string{"mooshroom": "cow", "cave_spider": "spider", "trader_llama": "llama"}

// eventSuffix is the Java sound name part of Bedrock events whose name differs.
var eventSuffix = map[string]string{
	"mad": "angry", "add.chest": "chest", "fuse": "primed", "mob.warning": "warning",
	"attack.strong": "attack", "breathe": "breath", "imitate": "ambient",
}

// entitySound is the Java sound event of a Bedrock sound event of a Java entity type (no
// namespace): Geyser's mapping, else entity.<type>.<event>; "" if Java has none.
func entitySound(java, event string) string {
	if a, ok := entityAliases[java]; ok {
		java = a
	}
	if s, ok := sounds.EntitySound(java, event); ok {
		return s
	}
	suffix, ok := eventSuffix[event]
	if !ok {
		suffix = strings.ReplaceAll(event, ".", "_")
	}
	if s := "entity." + java + "." + suffix; javaSound(s) {
		return s
	}
	return ""
}

// entitySource is the sound source of a Java entity type.
func entitySource(java string) int32 {
	switch {
	case java == "player":
		return srcPlayers
	case sounds.Hostile(java):
		return srcHostile
	}
	return srcNeutral
}

// javaBlockOfRID is the Java block (name without namespace) and 26.3 state of a server block
// runtime id.
func (p *Player) javaBlockOfRID(rid int32) (string, uint32, bool) {
	if p.rem == nil || p.rem.pal == nil || rid == -1 || rid == 0 && !p.rem.gd.UseBlockNetworkIDHashes {
		return "", 0, false
	}
	st := p.rem.pal.Java(p.rem.pal.ServerToIndex(uint32(rid)), false)
	jb := blocks.JavaBlockOf(st)
	if jb == nil {
		return "", 0, false
	}
	return trimNS(jb.Name), st, true
}

// javaBlockAt is the Java block (name without namespace) and state at a block position.
func (p *Player) javaBlockAt(pos cube.Pos) (string, uint32, bool) {
	if p.world == nil || p.rem == nil {
		return "", 0, false
	}
	i, ok := p.world.Block(pos)
	if !ok {
		return "", 0, false
	}
	st := p.rem.pal.Java(i, false)
	jb := blocks.JavaBlockOf(st)
	if jb == nil {
		return "", 0, false
	}
	return trimNS(jb.Name), st, true
}

// blockPosOf is the block a Bedrock sound position is in.
func blockPosOf(v mgl64.Vec3) cube.Pos {
	return cube.Pos{int(math.Floor(v[0])), int(math.Floor(v[1])), int(math.Floor(v[2]))}
}

// recent reports whether pos is in m since within.
func recent(m map[cube.Pos]time.Time, pos cube.Pos, within time.Duration) bool {
	t, ok := m[pos]
	return ok && time.Since(t) < within
}

// ownUse reports whether the Java client just used the block at pos itself (and so played its
// sound): a door, trapdoor, fence gate or button it clicked, a block it placed.
func (p *Player) ownUse(pos cube.Pos) bool {
	return recent(p.sounds().used, pos, 1500*time.Millisecond)
}

// ownDig reports whether the Java client is digging or just dug the block at pos.
func (p *Player) ownDig(pos cube.Pos) bool {
	if a := &p.win.act; a.breaking && int(a.breakPos[0]) == pos[0] && int(a.breakPos[1]) == pos[1] && int(a.breakPos[2]) == pos[2] {
		return true
	}
	return recent(p.sounds().dug, pos, 2*time.Second)
}

// spawnedByUnique is the entity with this unique id the Java client has.
func (p *Player) spawnedByUnique(uniq int64) *entity {
	if uniq == -1 || uniq == 0 {
		return nil
	}
	if e := p.entityByUnique(uniq); e != nil && e.spawned {
		return e
	}
	return nil
}

func (p *Player) unknownSound(kind, name string) {
	s := p.sounds()
	if !s.unknown[kind+name] {
		s.unknown[kind+name] = true
		p.log.Debug("untranslated "+kind, "name", name)
	}
}

// ---- LevelSoundEvent ----

// selfPredicted are events of the player's own entity the Java client already played.
var selfPredicted = map[string]bool{
	"step": true, "land": true, "jump": true, "splash": true, "swim": true, "place": true, "break": true,
	"hit": true, "item.use.on": true, "eat": true, "drink": true, "ignite": true, "fall": true, "fall.big": true,
	"fall.small": true, "heavy.step": true, "spawn": true, "ambient.underwater.enter": true,
	"ambient.underwater.exit": true, "bottle.fill": true, "bottle.empty": true, "bottle.dragonbreath": true,
	"hurt": true, "death": true, "throw": false,
}

// clientAmbient are sounds the Java client plays itself from blocks (animateTick), the biome or
// its own state.
var clientAmbient = map[string]bool{
	"block.campfire.crackle": true, "lava": true, "lava.pop": true, "water": true, "portal": true,
	"furnace.use": true, "blastfurnace.use": true, "smoker.use": true, "bubble.up": true, "bubble.down": true,
	"bubble.upinside": true, "bubble.downinside": true, "respawn_anchor.ambient": true, "ambient.candle": true,
	"ambient.underwater.enter": true, "ambient.underwater.exit": true, "ambient.in.water": true, "ambient.in.air": true,
	"ambient.in.raid": true, "ambient.cave": true, "disappeared": true, "default": true, "undefined": true,
	"block.sweet_berry_bush.place": false,
}

// levelSoundNames are Java sounds of events whose Geyser reverse mapping is not the vanilla one
// ("": none).
var levelSoundNames = map[string]string{
	"bow.hit": "entity.arrow.hit", "armor": "item.armor.equip_generic", "attack": "", "death": "entity.generic.death",
	"hurt": "entity.generic.hurt", "eat": "entity.generic.eat", "drink": "entity.generic.drink",
	"fall.big": "entity.generic.big_fall", "fall.small": "entity.generic.small_fall", "burp": "entity.player.burp",
	"glass": "entity.splash_potion.break", "levelup": "entity.player.levelup", "thunder": "entity.lightning_bolt.thunder",
	"shear": "item.shears.snip", "teleport": "entity.enderman.teleport",
}

// entityAgnostic are events of an entity whose Java sound does not depend on its type.
var entityAgnostic = map[string]bool{
	"bow": true, "bow.hit": true, "explode": true, "thunder": true, "throw": true, "attack.nodamage": true,
	"attack.strong": true, "shield.block": true, "armor.equip_generic": true, "item.trident.throw": true,
	"crossbow.shoot": true, "crossbow.loading.start": true, "crossbow.loading.middle": true,
	"crossbow.loading.end": true, "item.trident.hit": true, "item.trident.hit_ground": true, "burp": true,
}

func (p *Player) levelSound(pk *packet.LevelSoundEvent) {
	if p.rem == nil {
		return
	}
	ev := pk.SoundType
	pos := vec64(pk.Position)
	self := pk.EntityUniqueID != -1 && pk.EntityUniqueID == p.rem.gd.EntityUniqueID
	if clientAmbient[ev] || self && selfPredicted[ev] {
		return
	}
	if pk.EntityType == "" && (ev == "fire" || ev == "ambient" || strings.HasPrefix(ev, "ambient.")) {
		return // FireBlock.animateTick, biome, cave and underwater ambience: the client's own
	}
	var e *entity
	if !self {
		e = p.spawnedByUnique(pk.EntityUniqueID)
	}
	java := trimNS(pk.EntityType)
	if e != nil && e.java != "" {
		java = trimNS(e.java)
	} else if t, ok := entityTypes[pk.EntityType]; ok && t.java != "" {
		java = trimNS(t.java)
	}
	if _, known := entityTypes[pk.EntityType]; !known && pk.EntityType != "" && !strings.HasPrefix(pk.EntityType, "minecraft:") {
		// A custom mob's own sounds are in the Bedrock pack, and sounds are not converted (they
		// are most of a big pack's size). So that a hit is heard at all, hurt and death are the
		// generic Java sounds; its other events (ambient, step, spawn) stay silent.
		rid, shown := p.ents.byUnique[pk.EntityUniqueID]
		if model := shown && p.packs.ents[rid] != nil; model && (ev == "hurt" || ev == "death") {
			return // a mob drawn as a model: its hurt and death are heard from the actor events
		}
		switch ev {
		case "hurt":
			p.sendSound("entity.generic.hurt", entitySource(java), pos, 1, 1)
			return
		case "death":
			p.sendSound("entity.generic.death", entitySource(java), pos, 1, 1)
			return
		}
	}
	bp := blockPosOf(pos)

	switch ev {
	case "note":
		p.noteSound(bp, pk.ExtraData)
		return
	case "place", "break", "hit", "fall", "fall.big", "fall.small", "land", "step", "heavy.step", "item.use.on":
		if p.blockSound(ev, pk, e, java, pos, bp) {
			return
		}
	case "door.open", "door.close", "trapdoor.open", "trapdoor.close", "fence_gate.open", "fence_gate.close":
		p.openSound(ev, pk.ExtraData, pos, bp)
		return
	case "chest.open", "chest.closed", "enderchest.open", "enderchest.closed", "shulkerbox.open",
		"shulkerbox.closed", "barrel.open", "barrel.close":
		p.containerSound(ev, pk.ExtraData, pos)
		return
	case "button.click_on", "button.click_off":
		p.buttonSound(ev, pk.ExtraData, pos, bp)
		return
	case "explode":
		if java == "" || java == "tnt" || java == "creeper" || java == "tnt_minecart" || java == "end_crystal" {
			p.sendSound("entity.generic.explode", srcBlocks, pos, 4, (1+(rnd()-rnd())*0.2)*0.7)
			return
		}
	case "pop":
		if java == "" || java == "player" || java == "item" {
			p.sendSound("entity.item.pickup", srcPlayers, pos, 0.2, ((rnd()-rnd())*0.7+1)*2)
			return
		}
	case "throw":
		p.throwSound(pk, e, pos, self)
		return
	case "splash", "swim":
		if e != nil || java != "" {
			p.swimSound(ev, pk, e, java, pos)
			return
		}
	case "bell":
		p.sendSound("block.bell.use", srcBlocks, mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5}, 2, 1)
		return
	case "stop.record":
		p.sendStopSound(srcRecords, "")
		return
	case "attack.strong", "attack.nodamage":
		if self {
			hitSomething := p.sounds().attack.rid != 0
			p.sounds().attack.rid = 0 // the server said it: do not make it up
			if ev == "attack.nodamage" && !hitSomething {
				return // the player's own swing at the air: silent on Java
			}
		}
	}
	if strings.HasPrefix(ev, "record.") {
		p.recordSound(ev, pos)
		return
	}
	if strings.HasPrefix(ev, "bucket.") && (self || p.ownUse(bp)) {
		return // BucketItem: the client plays fill and empty itself
	}

	// Generic: the entity's sound, else the event's own.
	name, src, vol, pitch := "", int32(srcBlocks), float32(1), float32(1)
	if java != "" {
		if name = entitySound(java, ev); name != "" {
			src = entitySource(java)
			vol = sounds.SoundVolume(java, pk.BabyMob)
			pitch = voicePitch(pk.BabyMob)
		}
	}
	if name == "" && (java == "" || entityAgnostic[ev]) {
		s, ok := levelSoundNames[ev]
		if !ok {
			s, ok = sounds.LevelSound(ev)
			// Geyser's pick for an event of no entity may be some mob's (spawn: the ghastling's).
			ok = ok && (!strings.HasPrefix(s, "entity.") || entityAgnostic[ev])
		}
		switch {
		case ok:
			name = s
			if java != "" {
				src = entitySource(java)
			}
		case javaSound(ev):
			name = ev
		}
	}
	if name == "" {
		p.unknownSound("level sound", ev+" "+java)
		return
	}
	if e != nil && !self {
		p.sendSoundEntity(name, src, e.id, vol, pitch)
		return
	}
	if self {
		p.sendSoundEntity(name, src, SelfEntityID, vol, pitch)
		return
	}
	p.sendSound(name, src, pos, vol, pitch)
}

// noteBedrockInstruments are Bedrock's note block instruments (ExtraData >> 8) as Java sounds.
var noteBedrockInstruments = []string{"harp", "basedrum", "snare", "hat", "bass", "bell", "flute", "chime",
	"guitar", "xylophone", "iron_xylophone", "cow_bell", "didgeridoo", "bit", "banjo", "pling",
	"imitate.skeleton", "imitate.wither_skeleton", "imitate.zombie", "imitate.creeper", "imitate.ender_dragon",
	"imitate.piglin", "trumpet", "trumpet_exposed", "trumpet_weathered", "trumpet_oxidized"}

// noteSound plays a note block (NoteBlock.triggerEvent): the instrument at the note's pitch and a
// note particle coloured by the note; mob heads play their sound at pitch 1 without one.
func (p *Player) noteSound(bp cube.Pos, extra int32) {
	inst, note := int(extra>>8), int(extra&0xff)
	if inst < 0 || inst >= len(noteBedrockInstruments) {
		return
	}
	pitch := float32(1)
	tunable := !strings.HasPrefix(noteBedrockInstruments[inst], "imitate.")
	if tunable {
		pitch = float32(math.Pow(2, float64(note-12)/12))
	}
	at := mgl64.Vec3{float64(bp[0]) + 0.5, float64(bp[1]) + 0.5, float64(bp[2]) + 0.5}
	p.sendSound("block.note_block."+noteBedrockInstruments[inst], srcRecords, at, 3, pitch)
	if tunable {
		p.sendParticle(particle{name: "note"}, at.Add(mgl64.Vec3{0, 0.7, 0}), mgl64.Vec3{float64(note) / 24, 0, 0}, 1, 0)
	}
}

// blockSound plays a sound of a block's sound group. false: not a block sound (the event names
// no block), translate it otherwise.
func (p *Player) blockSound(ev string, pk *packet.LevelSoundEvent, e *entity, java string, pos mgl64.Vec3, bp cube.Pos) bool {
	switch ev {
	case "place", "break", "item.use.on":
		if p.ownUse(bp) {
			return true // the client placed it and played the sound
		}
	case "hit":
		if p.ownDig(bp) {
			return true // the client's own digging sounds
		}
	case "step", "heavy.step":
		if e != nil && e.living() {
			return true // footsteps are made up from movement (soundTick)
		}
	}
	name, st, ok := p.javaBlockOfRID(pk.ExtraData)
	if !ok || name == "air" {
		if e == nil && java == "" {
			return false
		}
		// An entity sound with no block (a horse landing, a slime squishing).
		if s := entitySound(java, ev); s != "" {
			if e != nil {
				p.sendSoundEntity(s, entitySource(java), e.id, sounds.SoundVolume(java, pk.BabyMob), voicePitch(pk.BabyMob))
			} else {
				p.sendSound(s, entitySource(java), pos, sounds.SoundVolume(java, pk.BabyMob), voicePitch(pk.BabyMob))
			}
		}
		return true
	}
	if java != "" && java != "player" && (ev == "land" || ev == "step") {
		if s := entitySound(java, ev); s != "" { // horses land with their own sound
			p.sendSound(s, entitySource(java), pos, sounds.SoundVolume(java, pk.BabyMob)*0.4, voicePitch(pk.BabyMob))
			return true
		}
	}
	t := sounds.Block(name, st)
	src := int32(srcBlocks)
	if java == "player" {
		src = srcPlayers
	} else if java != "" {
		src = entitySource(java)
	}
	switch ev {
	case "place":
		p.sendSound(t.Place, srcBlocks, pos, (t.Volume+1)/2, t.Pitch*0.8)
	case "item.use.on":
		switch name {
		case "farmland":
			p.sendSound("item.hoe.till", srcBlocks, pos, 1, 1)
		case "dirt_path":
			p.sendSound("item.shovel.flatten", srcBlocks, pos, 1, 1)
		default:
			p.sendSound(t.Place, srcBlocks, pos, (t.Volume+1)/2, t.Pitch*0.8)
		}
	case "break":
		p.sendSound(t.Break, srcBlocks, pos, (t.Volume+1)/2, t.Pitch*0.8)
	case "hit":
		p.sendSound(t.Hit, srcBlocks, pos, (t.Volume+1)/8, t.Pitch*0.5)
	case "fall", "fall.big", "fall.small":
		p.sendSound(t.Fall, src, pos, t.Volume*0.5, t.Pitch*0.75)
	case "land", "step", "heavy.step":
		// Java plays no landing sound: a landing is a footfall.
		p.sendSound(t.Step, src, pos, t.Volume*0.15, t.Pitch)
	}
	return true
}

// openSound plays a door, trapdoor or fence gate opening or closing (DoorBlock.playSound etc.).
func (p *Player) openSound(ev string, extra int32, pos mgl64.Vec3, bp cube.Pos) {
	name, _, ok := p.javaBlockOfRID(extra)
	if !ok {
		name, _, _ = p.javaBlockAt(bp)
	}
	if p.ownUse(bp) && !strings.Contains(name, "iron") {
		return // the client opened it and played the sound (iron ones it cannot open by hand)
	}
	kind, act, _ := strings.Cut(ev, ".")
	set := woodSet(name)
	s := "block." + set + "_" + kind + "." + act // block.cherry_wood_trapdoor.open
	if kind == "fence_gate" && set == "wooden" {
		s = "block.fence_gate." + act
	}
	if !javaSound(s) {
		s = "block.wooden_" + kind + "." + act
		if kind == "fence_gate" {
			s = "block.fence_gate." + act
		}
	}
	p.sendSound(s, srcBlocks, pos, 1, rnd()*0.1+0.9)
}

// woodSet is the Java block set type sound prefix of a door, trapdoor, fence gate or button.
func woodSet(name string) string {
	switch {
	case strings.Contains(name, "iron"):
		return "iron"
	case strings.Contains(name, "copper"):
		return "copper"
	case strings.Contains(name, "bamboo"):
		return "bamboo_wood"
	case strings.Contains(name, "cherry"):
		return "cherry_wood"
	case strings.Contains(name, "crimson"), strings.Contains(name, "warped"):
		return "nether_wood"
	}
	return "wooden"
}

// containerSound plays a chest, ender chest, shulker box or barrel opening or closing (the server
// plays these on Java, also to the player who opens it).
func (p *Player) containerSound(ev string, extra int32, pos mgl64.Vec3) {
	name, _, _ := p.javaBlockOfRID(extra)
	open := strings.HasSuffix(ev, ".open")
	act := "close"
	if open {
		act = "open"
	}
	var s string
	vol, pitch := float32(0.5), rnd()*0.1+0.9
	switch {
	case strings.HasPrefix(ev, "enderchest"):
		s = "block.ender_chest." + act
	case strings.HasPrefix(ev, "shulkerbox"):
		s, vol = "block.shulker_box."+act, 0.5
	case strings.HasPrefix(ev, "barrel"):
		s = "block.barrel." + act
	case strings.Contains(name, "copper_chest"):
		s = "block.copper_chest." + act
		if strings.Contains(name, "oxidized") {
			s = "block.copper_chest_oxidized." + act
		} else if strings.Contains(name, "weathered") {
			s = "block.copper_chest_weathered." + act
		}
	default:
		s = "block.chest." + act
	}
	p.sendSound(s, srcBlocks, pos, vol, pitch)
}

// buttonSound plays a button or lever click (ButtonBlock.playSound, LeverBlock.playSound).
func (p *Player) buttonSound(ev string, extra int32, pos mgl64.Vec3, bp cube.Pos) {
	name, _, ok := p.javaBlockOfRID(extra)
	if !ok {
		name, _, _ = p.javaBlockAt(bp)
	}
	on := ev == "button.click_on"
	if name == "lever" {
		pitch := float32(0.5)
		if on {
			pitch = 0.6
		}
		p.sendSound("block.lever.click", srcBlocks, pos, 0.3, pitch)
		return
	}
	if on && p.ownUse(bp) {
		return // the client pressed it and played the click
	}
	act := "click_off"
	if on {
		act = "click_on"
	}
	set := woodSet(name)
	if strings.Contains(name, "stone") || strings.Contains(name, "blackstone") {
		set = "stone"
	}
	s := "block." + set + "_button." + act
	if !javaSound(s) {
		s = "block.wooden_button." + act
	}
	p.sendSound(s, srcBlocks, pos, 1, 1)
}

// throwSound plays a throw by the item the thrower holds (snowball, egg, ender pearl...).
func (p *Player) throwSound(pk *packet.LevelSoundEvent, e *entity, pos mgl64.Vec3, self bool) {
	item := ""
	if e != nil && e.hasEquip {
		item = trimNS(p.itemName(e.equip[0].NetworkID))
	}
	if self {
		item = trimNS(p.heldItemName())
	}
	s := "entity.snowball.throw"
	switch item {
	case "egg", "blue_egg", "brown_egg":
		s = "entity.egg.throw"
	case "ender_pearl":
		s = "entity.ender_pearl.throw"
	case "splash_potion":
		s = "entity.splash_potion.throw"
	case "lingering_potion":
		s = "entity.lingering_potion.throw"
	case "experience_bottle":
		s = "entity.experience_bottle.throw"
	case "wind_charge":
		s = "entity.wind_charge.throw"
	case "ender_eye":
		s = "entity.ender_eye.launch"
	}
	pitch := 0.4 / (rnd()*0.4 + 0.8)
	switch {
	case self:
		p.sendSoundEntity(s, srcPlayers, SelfEntityID, 0.5, pitch)
	case e != nil:
		p.sendSoundEntity(s, entitySource(trimNS(e.java)), e.id, 0.5, pitch)
	default:
		p.sendSound(s, srcNeutral, pos, 0.5, pitch)
	}
}

// heldItemName is the Bedrock name of the item the player holds ("" if unknown).
func (p *Player) heldItemName() string {
	if entityHeldItem == nil {
		return ""
	}
	_, it := entityHeldItem(p)
	return p.itemName(it.Stack.NetworkID)
}

// swimSound plays an entity splashing into or swimming in water. BDS sends "splash" with a speed
// in ExtraData (millionths) every few ticks for swimming mobs: big ones are splashes, small ones
// swim strokes.
func (p *Player) swimSound(ev string, pk *packet.LevelSoundEvent, e *entity, java string, pos mgl64.Vec3) {
	speed := float32(pk.ExtraData) / 1e6
	who := "generic"
	if java == "player" {
		who = "player"
	}
	s := "entity." + who + ".swim"
	vol := min(max(speed, 0.05), 1) * 0.35
	if ev == "splash" && (speed >= 1 || pk.ExtraData <= 0) {
		s, vol = "entity."+who+".splash", min(max(speed*0.2, 0.2), 1)
		if who == "player" && speed > 2.5 {
			s = "entity.player.splash.high_speed"
		}
	}
	if sp := entitySound(java, ev); sp != "" && java != "player" {
		s = sp // fish flop, fishing bobber, dolphin splash
	}
	pitch := 1 + (rnd()-rnd())*0.4
	if e != nil {
		p.sendSoundEntity(s, entitySource(java), e.id, vol, pitch)
		return
	}
	p.sendSound(s, entitySource(java), pos, vol, pitch)
}

// recordSound plays a music disc (record.13 -> music_disc.13) at a jukebox.
func (p *Player) recordSound(ev string, pos mgl64.Vec3) {
	s := "music_disc." + strings.TrimPrefix(ev, "record.")
	if !javaSound(s) {
		p.unknownSound("record", ev)
		return
	}
	p.sendSound(s, srcRecords, pos, 4, 1)
}

// ---- PlaySound / StopSound ----

// playSoundNames are Bedrock sound names whose Geyser reverse mapping is not the vanilla one.
var playSoundNames = map[string]string{
	"random.door_open": "block.wooden_door.open", "random.door_close": "block.wooden_door.close",
	"random.levelup": "entity.player.levelup", "random.orb": "entity.experience_orb.pickup",
	"random.pop": "entity.item.pickup", "random.click": "ui.button.click", "random.explode": "entity.generic.explode",
	"random.anvil_use": "block.anvil.use", "random.anvil_land": "block.anvil.land", "random.toast": "ui.toast.in",
	"random.chestopen": "block.chest.open", "random.chestclosed": "block.chest.close", "random.bow": "entity.arrow.shoot",
	"random.bowhit": "entity.arrow.hit", "random.burp": "entity.player.burp", "random.fizz": "block.fire.extinguish",
	"random.glass": "block.glass.break", "random.hurt": "entity.player.hurt", "random.splash": "entity.generic.splash",
	"random.totem": "item.totem.use", "note.harp": "block.note_block.harp", "note.bass": "block.note_block.bass",
	"note.pling": "block.note_block.pling", "note.bd": "block.note_block.basedrum", "note.snare": "block.note_block.snare",
	"note.hat": "block.note_block.hat", "note.bell": "block.note_block.bell", "note.flute": "block.note_block.flute",
	"note.chime": "block.note_block.chime", "note.guitar": "block.note_block.guitar",
	"note.xylophone": "block.note_block.xylophone", "note.iron_xylophone": "block.note_block.iron_xylophone",
	"note.cow_bell": "block.note_block.cow_bell", "note.didgeridoo": "block.note_block.didgeridoo",
	"note.bit": "block.note_block.bit", "note.banjo": "block.note_block.banjo",
	"mob.endermen.portal": "entity.enderman.teleport", "ambient.weather.thunder": "entity.lightning_bolt.thunder",
	"ambient.weather.lightning.impact": "entity.lightning_bolt.impact", "mob.villager.yes": "entity.villager.yes",
	"mob.villager.no": "entity.villager.no", "mob.villager.haggle": "entity.villager.trade",
	"mob.zombie.wood": "entity.zombie.attack_wooden_door", "mob.zombie.woodbreak": "entity.zombie.break_wooden_door",
}

// playSoundJava is the Java sound of a Bedrock sound name: the table above, Geyser's mapping, the
// same name if Java has it, else the name itself (a resource pack may define it).
func playSoundJava(name string) string {
	if s, ok := playSoundNames[name]; ok {
		return s
	}
	if s, ok := sounds.PlaySound(name); ok {
		return s
	}
	return name
}

// playSoundSource guesses a sound source from a Bedrock sound name.
func playSoundSource(java string) int32 {
	switch {
	case strings.HasPrefix(java, "music_disc."), strings.HasPrefix(java, "block.note_block."):
		return srcRecords
	case strings.HasPrefix(java, "music."):
		return srcMusic
	case strings.HasPrefix(java, "ambient.") || strings.HasPrefix(java, "weather."):
		return srcAmbient
	case strings.HasPrefix(java, "entity.player."), strings.HasPrefix(java, "ui."):
		return srcPlayers
	case strings.HasPrefix(java, "entity."):
		if f := strings.Split(java, "."); len(f) > 1 && sounds.Hostile(f[1]) {
			return srcHostile
		}
		return srcNeutral
	}
	return srcBlocks
}

func (p *Player) playSound(pk *packet.PlaySound) {
	name := playSoundJava(pk.SoundName)
	pos := vec64(pk.Position)
	vol, pitch := pk.Volume, pk.Pitch
	if vol <= 0 {
		vol = 1
	}
	if pitch <= 0 {
		pitch = 1
	}
	src := playSoundSource(name)
	if strings.HasPrefix(name, "music.") || pk.BypassListenerRangeCheck {
		p.sendSoundEntity(name, src, SelfEntityID, vol, pitch) // wherever the player goes
		return
	}
	p.sendSound(name, src, pos, vol, pitch)
}

func (p *Player) stopSound(pk *packet.StopSound) {
	if pk.StopAll || pk.SoundName == "" {
		p.sendStopSound(-1, "")
		return
	}
	p.sendStopSound(-1, playSoundJava(pk.SoundName))
}

// ---- sound-type LevelEvents ----

// levelEventJava maps Bedrock sound level events to Java level events (the client plays the
// sound with Java's volume and pitch).
var levelEventJava = map[int32]int32{
	packet.LevelEventSoundGhastWarning:         1015,
	packet.LevelEventSoundGhastFireball:        1016,
	packet.LevelEventSoundBlazeFireball:        1018,
	packet.LevelEventSoundZombieWoodenDoor:     1019,
	packet.LevelEventSoundZombieDoorCrash:      1021,
	packet.LevelEventSoundZombieInfected:       1026,
	packet.LevelEventSoundZombieConverted:      1027,
	packet.LevelEventSoundAnvilBroken:          1029,
	packet.LevelEventSoundAnvilUsed:            1030,
	packet.LevelEventSoundAnvilLand:            1031,
	packet.LevelEventSoundPointedDripstoneLand: 1045,
}

// levelEventSounds maps Bedrock sound level events to Java sounds (name, volume, pitch; pitch 0:
// random 0.9-1.1).
var levelEventSounds = map[int32]struct {
	name       string
	vol, pitch float32
}{
	packet.LevelEventSoundOpenDoor:            {"block.wooden_door.open", 1, 0},
	packet.LevelEventSoundFizz:                {"block.fire.extinguish", 0.5, 2.6},
	packet.LevelEventSoundFuse:                {"entity.tnt.primed", 1, 1},
	packet.LevelEventSoundEndermanTeleport:    {"entity.enderman.teleport", 1, 1},
	packet.LevelEventSoundInfinityArrowPickup: {"entity.item.pickup", 0.2, 2},
	packet.LevelEventSoundTeleportEnderPearl:  {"entity.player.teleport", 1, 1},
	packet.LevelEventSoundAddItem:             {"entity.item_frame.add_item", 1, 1},
	packet.LevelEventSoundItemFrameBreak:      {"entity.item_frame.break", 1, 1},
	packet.LevelEventSoundItemFramePlace:      {"entity.item_frame.place", 1, 1},
	packet.LevelEventSoundItemFrameRemoveItem: {"entity.item_frame.remove_item", 1, 1},
	packet.LevelEventSoundItemFrameRotateItem: {"entity.item_frame.rotate_item", 1, 1},
	packet.LevelEventSoundExperienceOrbPickup: {"entity.experience_orb.pickup", 0.1, 0},
	packet.LevelEventSoundArmorStandBreak:     {"entity.armor_stand.break", 1, 1},
	packet.LevelEventSoundArmorStandLand:      {"entity.armor_stand.fall", 1, 1},
	packet.LevelEventSoundArmorStandPlace:     {"entity.armor_stand.place", 1, 1},
	packet.LevelEventSoundDyeUsed:             {"item.dye.use", 1, 1},
	packet.LevelEventSoundInkSacUsed:          {"item.ink_sac.use", 1, 1},
	packet.LevelEventSoundAmethystResonate:    {"block.amethyst_block.resonate", 1, 1},
	packet.LevelEventCauldronFillPotion:       {"item.bottle.empty", 1, 1},
	packet.LevelEventCauldronTakePotion:       {"item.bottle.fill", 1, 1},
	packet.LevelEventCauldronFillWater:        {"item.bucket.empty", 1, 1},
	packet.LevelEventCauldronTakeWater:        {"item.bucket.fill", 1, 1},
	packet.LevelEventCauldronFillLava:         {"item.bucket.empty_lava", 1, 1},
	packet.LevelEventCauldronTakeLava:         {"item.bucket.fill_lava", 1, 1},
	packet.LevelEventCauldronFillPowderSnow:   {"item.bucket.empty_powder_snow", 1, 1},
	packet.LevelEventCauldronTakePowderSnow:   {"item.bucket.fill_powder_snow", 1, 1},
}

func (p *Player) levelEventSound(pk *packet.LevelEvent) {
	pos := vec64(pk.Position)
	bp := blockPosOf(pos)
	switch pk.EventType {
	case packet.LevelEventSoundClick, packet.LevelEventSoundClickFail, packet.LevelEventSoundLaunch:
		// Dispensers: Bedrock gives the pitch (x1000).
		s := map[int32]string{packet.LevelEventSoundClick: "block.dispenser.dispense",
			packet.LevelEventSoundClickFail: "block.dispenser.fail", packet.LevelEventSoundLaunch: "block.dispenser.launch"}[pk.EventType]
		pitch := float32(pk.EventData) / 1000
		if pitch <= 0 {
			pitch = 1
		}
		p.sendSound(s, srcBlocks, pos, 1, pitch)
		return
	case packet.LevelEventSoundExperienceOrbPickup:
		if time.Since(p.sounds().orbTaken) < 250*time.Millisecond {
			return // take_item_entity of the orb played it
		}
	case packet.LevelEventSoundTotemUsed, packet.LevelEventSoundArmorStandHit:
		return // entity events 35 and 32 play these on the client
	}
	if id, ok := levelEventJava[pk.EventType]; ok {
		p.sendLevelEvent(id, bp, 0, false)
		return
	}
	if s, ok := levelEventSounds[pk.EventType]; ok {
		pitch := s.pitch
		switch {
		case pitch == 0:
			pitch = rnd()*0.2 + 0.9
		case pk.EventType == packet.LevelEventSoundFizz:
			pitch = 2.6 + (rnd()-rnd())*0.8
		}
		p.sendSound(s.name, srcBlocks, pos, s.vol, pitch)
	}
}

// sendLevelEvent sends a Java level_event.
func (p *Player) sendLevelEvent(id int32, pos cube.Pos, data int32, global bool) {
	w := p.pk()
	w.Int32(id)
	w.Position(pos[0], pos[1], pos[2])
	w.Int32(data)
	w.Bool(global)
	p.send(v777.ClientboundPlayLevelEvent, w)
}

// ---- made-up sounds: hurt, death, ambient, steps, eating, own attacks ----

// hurtCauseSounds are player hurt sounds by Bedrock damage cause (ActorEvent hurt data).
var hurtCauseSounds = map[int32]string{
	6: "entity.player.hurt_on_fire", 7: "entity.player.hurt_on_fire", 8: "entity.player.hurt_on_fire",
	9: "entity.player.hurt_drown", 22: "entity.player.hurt_on_fire", 27: "entity.player.hurt_freeze",
	32: "entity.player.hurt_on_fire", 33: "entity.player.hurt_on_fire",
}

func (p *Player) actorEventSound(pk *packet.ActorEvent) {
	if p.rem == nil || pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID {
		return // own hurt: damage_event plays it; own death: Java plays none to the dying player
	}
	e, ok := p.ents.shown[pk.EntityRuntimeID]
	if !ok || !e.spawned || !e.living() {
		return
	}
	java := trimNS(e.java)
	if java == "armor_stand" {
		return
	}
	baby := e.flag(protocol.EntityDataFlagBaby)
	s := p.sounds()
	switch pk.EventType {
	case packet.ActorEventHurt:
		if s.attack.rid == e.rid && time.Since(s.attack.at) < 600*time.Millisecond {
			s.attack.rid = 0
			p.sendSoundEntity("entity.player.attack.strong", srcPlayers, SelfEntityID, 1, 1)
		}
		if e.flag(protocol.EntityDataFlagSilent) {
			return
		}
		name := ""
		if java == "player" {
			name = hurtCauseSounds[pk.EventData]
		}
		if name == "" {
			name = entitySound(java, "hurt")
			if baby {
				if b := entitySound(java, "hurt.baby"); b != "" {
					name = b
				}
			}
		}
		p.sendSoundEntity(name, entitySource(java), e.id, sounds.SoundVolume(java, baby), voicePitch(baby))
	case packet.ActorEventDeath:
		m := s.mob(e.rid)
		if m.died || e.flag(protocol.EntityDataFlagSilent) {
			return
		}
		m.died = true
		name := entitySound(java, "death")
		if baby {
			if b := entitySound(java, "death.baby"); b != "" {
				name = b
			}
		}
		p.sendSoundEntity(name, entitySource(java), e.id, sounds.SoundVolume(java, baby), voicePitch(baby))
	case packet.ActorEventPlayAmbient:
		p.ambientSound(e, s.mob(e.rid))
	}
}

func (s *soundState) mob(rid uint64) *mobSound {
	m := s.mobs[rid]
	if m == nil {
		m = &mobSound{ambient: -80}
		s.mobs[rid] = m
	}
	return m
}

// ambientSound plays a mob's ambient sound (Mob.playAmbientSound) and restarts its timer.
func (p *Player) ambientSound(e *entity, m *mobSound) {
	java := trimNS(e.java)
	m.ambient = -sounds.AmbientInterval(java)
	baby := e.flag(protocol.EntityDataFlagBaby)
	name := ""
	water := p.inWater(e.pos)
	switch java {
	case "axolotl":
		name = map[bool]string{true: "entity.axolotl.idle_water", false: "entity.axolotl.idle_air"}[water]
	case "dolphin", "drowned":
		if water {
			name = "entity." + java + ".ambient_water"
		}
	case "guardian", "elder_guardian":
		if !water {
			name = "entity." + java + ".ambient_land"
		}
	case "turtle":
		if !water {
			name = "entity.turtle.ambient_land"
		}
	case "allay":
		name = "entity.allay.ambient_without_item"
	case "sniffer":
		name = "entity.sniffer.idle"
	case "breeze":
		name = map[bool]string{true: "entity.breeze.idle_ground", false: "entity.breeze.idle_air"}[e.onGround]
	case "bee", "creeper", "slime", "magma_cube", "iron_golem", "armor_stand", "shulker":
		return // no ambient sound (bees: the client's own flying sound)
	}
	if name == "" && baby {
		name = entitySound(java, "ambient.baby")
	}
	if name == "" {
		name = entitySound(java, "ambient")
	}
	if name == "" {
		return
	}
	p.sendSoundEntity(name, entitySource(java), e.id, sounds.SoundVolume(java, baby), voicePitch(baby))
}

// inWater reports whether the block at a Java position is water.
func (p *Player) inWater(pos mgl64.Vec3) bool {
	if p.world == nil {
		return false
	}
	i, ok := p.world.Block(blockPosOf(pos.Add(mgl64.Vec3{0, 0.3, 0})))
	return ok && p.rem.pal.IsWater(i)
}

// stepVolume is a mob's step sound volume where it is not Java's usual 0.15.
var stepVolume = map[string]float32{"iron_golem": 1, "warden": 10, "camel": 1, "sniffer": 0.35}

// edible are the Bedrock items a Java client eats (entity.generic.eat); drinks are below.
var edible = map[string]bool{
	"apple": true, "golden_apple": true, "enchanted_golden_apple": true, "appleenchanted": true, "mushroom_stew": true,
	"bread": true, "porkchop": true, "cooked_porkchop": true, "cod": true, "salmon": true, "tropical_fish": true,
	"pufferfish": true, "cooked_cod": true, "cooked_salmon": true, "cookie": true, "melon_slice": true,
	"dried_kelp": true, "beef": true, "cooked_beef": true, "chicken": true, "cooked_chicken": true,
	"rotten_flesh": true, "spider_eye": true, "carrot": true, "potato": true, "baked_potato": true,
	"poisonous_potato": true, "golden_carrot": true, "pumpkin_pie": true, "rabbit": true, "cooked_rabbit": true,
	"rabbit_stew": true, "mutton": true, "cooked_mutton": true, "chorus_fruit": true, "beetroot": true,
	"beetroot_soup": true, "sweet_berries": true, "glow_berries": true, "suspicious_stew": true,
}

// drinks are the Bedrock items a Java client drinks (entity.generic.drink).
var drinks = map[string]string{
	"potion": "entity.generic.drink", "milk_bucket": "entity.generic.drink", "ominous_bottle": "entity.generic.drink",
	"honey_bottle": "item.honey_bottle.drink",
}

// soundTick makes the sounds a Java server makes every tick: mob ambient sounds, footsteps of
// other players and mobs, eating, and the "no damage" sound of the player's own attacks.
func (p *Player) soundTick() {
	if p.rem == nil || p.world == nil {
		return
	}
	s := p.sounds()
	if s.attack.rid != 0 && time.Since(s.attack.at) > 600*time.Millisecond {
		s.attack.rid = 0
		p.sendSoundEntity("entity.player.attack.nodamage", srcPlayers, SelfEntityID, 1, 1)
	}
	for rid := range s.mobs {
		if _, ok := p.ents.shown[rid]; !ok {
			delete(s.mobs, rid)
		}
	}
	me := p.mv.pos
	for rid, e := range p.ents.shown {
		if !e.spawned || !e.living() || e.dead {
			continue
		}
		java := trimNS(e.java)
		if java == "armor_stand" || java == "" {
			continue
		}
		m := s.mob(rid)
		near := e.pos.Sub(me).Len() < 24
		silent := e.flag(protocol.EntityDataFlagSilent)
		// Ambient: Mob.baseTick.
		if java != "player" && !silent {
			if rand.IntN(1000) < m.ambient {
				if near {
					p.ambientSound(e, m)
				} else {
					m.ambient = -sounds.AmbientInterval(java)
				}
			}
			m.ambient++
		}
		// Steps: Entity.move's moveDist / nextStep.
		if !m.tracked || e.pos.Sub(m.last).Len() > 8 {
			m.last, m.tracked = e.pos, true
		} else {
			d := e.pos.Sub(m.last)
			m.last = e.pos
			horiz := math.Hypot(d[0], d[2])
			if horiz > 0.0001 && e.onGround && e.vehicle == 0 && !silent {
				m.moveDist += horiz * 0.6
				if m.moveDist > m.nextStep {
					if p.stepSound(e, java, near) {
						m.nextStep = m.moveDist + 1
					}
				}
			}
		}
		// Eating and drinking of other players (Consumable: from 7 ticks in, every 4 ticks; the
		// server sends the burp).
		if java == "player" && e.flag(protocol.EntityDataFlagUsingItem) && e.hasEquip {
			item := trimNS(p.itemName(e.equip[0].NetworkID))
			if item != m.useItem {
				m.useItem, m.useTicks = item, 0
			}
			m.useTicks++
			sound, drink := drinks[item]
			if !drink && edible[item] {
				sound = "entity.generic.eat"
			}
			total := 32
			if item == "dried_kelp" {
				total = 16
			} else if item == "honey_bottle" {
				total = 40
			}
			if sound != "" && near && m.useTicks > total*7/32 && (total-m.useTicks)%4 == 0 && m.useTicks <= total {
				p.sendSoundEntity(sound, srcPlayers, e.id, 0.5+0.5*float32(rand.IntN(2)), (rnd()-rnd())*0.2+1)
			}
		} else if m.useItem != "" {
			m.useItem, m.useTicks = "", 0
		}
	}
}

// stepSound plays an entity's footstep (Entity.playStepSound, the mobs' own step sounds). false:
// it stands on air (no step yet).
func (p *Player) stepSound(e *entity, java string, near bool) bool {
	feet := blockPosOf(e.pos.Sub(mgl64.Vec3{0, 0.2, 0}))
	name, st, ok := p.javaBlockAt(feet)
	if !ok || name == "air" || name == "cave_air" || name == "void_air" {
		return false
	}
	if !near {
		return true
	}
	if java == "player" && e.flag(protocol.EntityDataFlagSneaking) {
		return true // Player.getMovementEmission: sneaking on the ground makes no sound
	}
	if p.inWater(e.pos) {
		return true
	}
	if java != "player" {
		if s := entitySound(java, "step"); s != "" {
			vol, ok := stepVolume[java]
			if !ok {
				vol = 0.15
			}
			p.sendSoundEntity(s, entitySource(java), e.id, vol, 1)
			return true
		}
	}
	// A carpet or snow layer on top sounds instead of the block below.
	if above, ast, ok := p.javaBlockAt(feet.Side(cube.FaceUp)); ok && (strings.HasSuffix(above, "carpet") || above == "snow") {
		name, st = above, ast
	}
	t := sounds.Block(name, st)
	src := entitySource(java)
	p.sendSoundEntity(t.Step, src, e.id, t.Volume*0.15, t.Pitch)
	return true
}
