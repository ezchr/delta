package bridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// selfState is the player's own state on the server.
type selfState struct {
	pos        mgl64.Vec3 // feet
	yaw, pitch float32
	baseMode   int32 // Bedrock game type
	mode       int32 // Java game mode
	mayFly     bool
	flying     bool
	flySpeed   float32
	health     float32
	maxHealth  float32
	absorption float32
	food       int32
	saturation float32
	xpLevel    int32
	xpProgress float32
	speed      float64 // Bedrock movement attribute without the sprint modifier
	dead       bool
	// searching: the server put the player above the world or inside blocks (BDS spawn search);
	// the first column there moves them to where they stand.
	searching bool
	// immobile: the server froze the player (entity flag no AI).
	immobile bool
	// held: the client is kept in the air until the column it is in has been sent (hold.go).
	held bool
	// usingItem: the server's using-item flag of the player, as the Java client was told (food.go).
	usingItem bool
	// opLevel is the Java op level the client is given (entity_event 24+n): 2 for everyone, since
	// a Java client below 2 refuses target selectors (@s, @a...) in what it types and Bedrock
	// allows them in any command a player may run; 4 for operators. 0: not decided yet (2).
	opLevel byte
}

// canFly reports whether the player may toggle flight (the abilities' may-fly, or the game mode
// before the abilities came).
func (s *selfState) canFly() bool {
	return s.mayFly || s.mode == GameCreative || s.mode == GameSpectator
}

// levelClock is the time and weather of the level.
type levelClock struct {
	time          int64
	cycle         bool
	rain, thunder float32
}

func vec64(v mgl32.Vec3) mgl64.Vec3 { return mgl64.Vec3{float64(v[0]), float64(v[1]), float64(v[2])} }
func vec32(v mgl64.Vec3) mgl32.Vec3 { return mgl32.Vec3{float32(v[0]), float32(v[1]), float32(v[2])} }

// javaGameMode is the Java game mode of a Bedrock game type.
func javaGameMode(t int32) int32 {
	switch t {
	case 1:
		return GameCreative
	case 2:
		return GameAdventure
	case 3, 6:
		return GameSpectator
	}
	return GameSurvival
}

// enterRemote makes rem the player's connection and shows its world: the Java login on joining,
// a respawn into its dimension otherwise. p.mu is held.
func (p *Player) enterRemote(rem *Remote, first bool) {
	gd := rem.gd
	p.leaveWorld()
	p.resetState(true)
	p.rem, p.target, p.inWaiting = rem, rem.target, false
	p.world = newWorld(rem.pal, gd.Dimension)
	s := &p.self
	s.pos = vec64(gd.PlayerPosition).Sub(mgl64.Vec3{0, eyeHeight, 0})
	s.yaw, s.pitch = gd.Yaw, gd.Pitch
	t := gd.PlayerGameMode
	if t == 5 { // default: the world's
		t = gd.WorldGameMode
	}
	s.baseMode, s.mode = t, javaGameMode(t)
	s.opLevel = opLevelOf(gd.PlayerPermissions >= 2)
	s.health, s.maxHealth, s.food, s.saturation, s.speed, s.flySpeed = 20, 20, 20, 5, 0.1, 0.05
	s.searching = true
	p.clock = levelClock{time: gd.Time, cycle: true}
	p.mv.reset(s.pos, s.yaw, s.pitch)
	dim := javaDimension(gd.Dimension)
	if first {
		p.sendLogin(dim, s.mode)
		p.sendSelfTab(s.mode)
	} else {
		p.sendRespawn(dim, s.mode, 0)
	}
	p.showSelf(true)
	p.sendSpawnPoint(int(gd.WorldSpawn[0]), int(gd.WorldSpawn[1]), int(gd.WorldSpawn[2]))
	p.cmds.sendTree()
	p.log.Debug("entered server", "target", rem.target, "dim", dim, "pos", s.pos, "mode", s.mode, "first", first)
}

// resetState forgets the per-connection (or per-dimension) state: entities, windows and the
// rest registered with OnReset. p.mu is held.
func (p *Player) resetState(newConn bool) {
	p.ents.reset()
	for _, f := range resetHooks {
		f(p, newConn)
	}
	p.batchInFlight = false
	p.centreSent = false
	p.javaLoaded = false
	if newConn {
		p.self = selfState{}
		p.text = textState{}
	}
}

// showSelf sends the player's own state to the Java client after a login or respawn: position,
// abilities, attributes, health, food, experience, time and weather. teleport: also put the
// client at the server position.
func (p *Player) showSelf(teleport bool) {
	s := &p.self
	if teleport {
		p.teleportTo(s.pos, s.yaw, s.pitch)
	}
	p.sendWaitForChunks()
	p.sendAbilities(s.mode, s.mayFly, s.flying, s.flySpeed)
	p.sendOpLevel()
	p.sendMovementAttributes()
	p.sendHealth(max(s.health, 0), s.food, s.saturation)
	p.sendAbsorption(s.absorption)
	p.sendExperience(s.xpProgress, s.xpLevel)
	p.sendTime(p.clock.time, p.clock.cycle)
	p.sendWeather(p.clock.rain, p.clock.thunder)
	p.sendSkinParts()
	p.sendCentre(s.pos, true)
}

// Bedrock's movement values given to the Java client, so it moves as a Bedrock client would and
// the server's movement checks agree with it.
const (
	bedrockStepHeight    = 0.5625
	bedrockJumpStrength  = 0.42
	bedrockGravity       = 0.08
	bedrockSneakSpeed    = 0.3
	bedrockSafeFall      = 3
	bedrockBlockRange    = 5.0
	bedrockEntityRange   = 3.0
	noAttackCooldown     = 1024 // Java's attack cooldown recharges at once
	bedrockCreativeRange = 12.0
)

// sendMovementAttributes sends the Java client Bedrock's movement attributes.
func (p *Player) sendMovementAttributes() {
	s := &p.self
	blockRange := bedrockBlockRange
	if s.mode == GameCreative {
		blockRange = bedrockCreativeRange
	}
	maxHealth := float64(s.maxHealth)
	if maxHealth <= 0 {
		maxHealth = 20
	}
	p.sendAttributes(
		[]string{"minecraft:step_height", "minecraft:jump_strength", "minecraft:gravity", "minecraft:movement_speed",
			"minecraft:sneaking_speed", "minecraft:safe_fall_distance", "minecraft:block_interaction_range",
			"minecraft:entity_interaction_range", "minecraft:attack_speed", "minecraft:max_health"},
		[]float64{bedrockStepHeight, bedrockJumpStrength, bedrockGravity, s.speed,
			bedrockSneakSpeed, bedrockSafeFall, blockRange,
			bedrockEntityRange, noAttackCooldown, maxHealth},
	)
}

func init() {
	OnBedrock(packet.IDSetHealth, func(p *Player, pk *packet.SetHealth) {
		p.setHealth(float32(pk.Health), p.self.maxHealth)
	})
	OnBedrock(packet.IDUpdateAttributes, func(p *Player, pk *packet.UpdateAttributes) {
		if pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID {
			p.updateSelfAttributes(pk.Attributes)
		}
	})
	OnBedrock(packet.IDSetPlayerGameType, func(p *Player, pk *packet.SetPlayerGameType) { p.setGameType(pk.GameType) })
	OnBedrock(packet.IDUpdatePlayerGameType, func(p *Player, pk *packet.UpdatePlayerGameType) {
		if pk.PlayerUniqueID == p.rem.gd.EntityUniqueID {
			p.setGameType(pk.GameType)
		}
	})
	OnBedrock(packet.IDUpdateAbilities, func(p *Player, pk *packet.UpdateAbilities) {
		if pk.AbilityData.EntityUniqueID != p.rem.gd.EntityUniqueID {
			return
		}
		s := &p.self
		if debugMove {
			p.log.Info("MV abilities", "layers", fmt.Sprintf("%+v", pk.AbilityData.Layers), "tick", p.mv.tick)
		}
		for _, l := range pk.AbilityData.Layers {
			if l.Type != protocol.AbilityLayerTypeBase {
				continue
			}
			s.mayFly = l.Values&protocol.AbilityMayFly != 0
			flying := l.Values&protocol.AbilityFlying != 0
			// Abilities the server sent before it got our request to fly (movement.go) still say
			// not flying: they must not end the flight the Java client just started.
			if flying || !s.mayFly || time.Since(p.mv.flyRequested) > time.Second {
				s.flying = flying
			}
			if l.FlySpeed > 0 {
				s.flySpeed = l.FlySpeed
			}
		}
		p.sendAbilities(s.mode, s.mayFly, s.flying, s.flySpeed)
		op := pk.AbilityData.PlayerPermissions >= 2 || pk.AbilityData.CommandPermissions >= 1
		if lvl := opLevelOf(op); lvl != s.opLevel {
			s.opLevel = lvl
			p.sendOpLevel()
		}
	})
	OnBedrock(packet.IDSetSpawnPosition, func(p *Player, pk *packet.SetSpawnPosition) {
		if pk.SpawnType == packet.SpawnTypeWorld {
			p.sendSpawnPoint(int(pk.SpawnPosition[0]), int(pk.SpawnPosition[1]), int(pk.SpawnPosition[2]))
		}
	})
	OnBedrock(packet.IDSetTime, func(p *Player, pk *packet.SetTime) {
		if p.rem.clocks != nil {
			// A server with world clocks sends the game time here (it goes on when /time set
			// moves the clock); the time of day is the overworld clock (SyncWorldClocks).
			p.log.Debug("set time ignored: world clocks", "time", pk.Time)
			return
		}
		p.clock.time = int64(pk.Time)
		p.sendTime(p.clock.time, p.clock.cycle)
	})
	// BDS 1.26.60 sets the time with world clocks (/time set, the day cycle gamerule) and no
	// longer sends SetTime for it: the overworld clock is the time of day.
	OnBedrock(packet.IDSyncWorldClocks, func(p *Player, pk *packet.SyncWorldClocks) {
		r := p.rem
		apply := func(id uint64, t int32, paused bool) {
			if !dayClock(r.clocks, id) {
				return
			}
			p.clock.time, p.clock.cycle = int64(t), !paused
			p.sendTime(p.clock.time, p.clock.cycle)
		}
		switch pk.PayloadType {
		case protocol.ClockPayloadTypeInitializeRegistry:
			r.clocks = map[uint64]string{}
			for _, c := range pk.Clocks {
				r.clocks[c.ID] = c.Name
			}
			p.log.Debug("world clocks", "clocks", r.clocks)
			for _, c := range pk.Clocks {
				apply(c.ID, c.Time, c.Paused)
			}
		case protocol.ClockPayloadTypeSyncState:
			for _, c := range pk.SyncStates {
				apply(c.ClockID, c.Time, c.Paused)
			}
		}
	})
	OnBedrock(packet.IDGameRulesChanged, func(p *Player, pk *packet.GameRulesChanged) {
		for _, r := range pk.GameRules {
			if strings.EqualFold(r.Name, "dodaylightcycle") {
				if v, ok := r.Value.(bool); ok {
					p.clock.cycle = v
					p.sendTime(p.clock.time, v)
				}
			}
		}
	})
	OnBedrock(packet.IDLevelEvent, func(p *Player, pk *packet.LevelEvent) {
		switch pk.EventType {
		case packet.LevelEventStartRaining:
			p.clock.rain = 1
		case packet.LevelEventStopRaining:
			p.clock.rain = 0
		case packet.LevelEventStartThunderstorm:
			p.clock.thunder = 1
		case packet.LevelEventStopThunderstorm:
			p.clock.thunder = 0
		default:
			return
		}
		p.sendWeather(p.clock.rain, p.clock.thunder)
	})
	OnBedrock(packet.IDSetActorData, func(p *Player, pk *packet.SetActorData) {
		if pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID {
			p.selfMetadata(pk.EntityMetadata)
		}
	})
	OnBedrock(packet.IDChangeDimension, (*Player).changeDimension)
}

// setHealth applies the server's health of the player.
func (p *Player) setHealth(health, maxHealth float32) {
	s := &p.self
	s.health = health
	if maxHealth > 0 {
		s.maxHealth = maxHealth
	}
	if health <= 0 && !s.dead {
		s.dead = true
		p.log.Debug("died", "pos", s.pos)
		p.sendHealth(0, s.food, s.saturation)
		p.sendCombatKill(p.text.deathMessage)
		return
	}
	if s.dead && health <= 0 {
		return
	}
	if s.dead {
		return // health coming back alone does not end a death: the respawn does (respawn.go)
	}
	p.sendHealth(health, s.food, s.saturation)
}

// updateSelfAttributes applies the player's own attributes.
func (p *Player) updateSelfAttributes(attrs []protocol.Attribute) {
	s := &p.self
	var health, food, xp, move bool
	for _, a := range attrs {
		switch a.Name {
		case "minecraft:health":
			s.maxHealth = a.Max
			if s.maxHealth <= 0 {
				s.maxHealth = 20
			}
			p.setHealth(a.Value, a.Max)
			health = true
		case "minecraft:absorption":
			s.absorption = a.Value
			p.sendAbsorption(a.Value)
		case "minecraft:player.hunger":
			s.food = int32(a.Value)
			food = true
		case "minecraft:player.saturation":
			s.saturation = a.Value
			food = true
		case "minecraft:player.level":
			s.xpLevel = int32(a.Value)
			xp = true
		case "minecraft:player.experience":
			s.xpProgress = a.Value
			xp = true
		case "minecraft:movement":
			p.sprintJumpVerdict(sprintModifier(a))
			speed := baseMovement(a)
			if debugMove {
				p.log.Info("MV movement attribute", "value", a.Value, "default", a.Default, "modifiers", a.Modifiers, "base", speed)
			}
			if d := speed - s.speed; d > 1e-4 || d < -1e-4 {
				s.speed = speed
				move = true
			}
		}
	}
	if food && !health && !s.dead {
		p.sendHealth(s.health, s.food, s.saturation)
	}
	if xp {
		p.sendExperience(s.xpProgress, s.xpLevel)
	}
	if move || health {
		p.sendMovementAttributes()
	}
}

// sprintModifier reports whether a movement attribute has Bedrock's sprint modifier.
func sprintModifier(a protocol.Attribute) bool {
	for _, m := range a.Modifiers {
		if m.ID == "d208fc00-42aa-4aad-9276-d5446530de43" || strings.Contains(strings.ToLower(m.Name), "sprint") {
			return true
		}
	}
	return false
}

// baseMovement is the Bedrock movement attribute without the sprint boost, which the Java client
// applies itself (effects stay in: the Java client does not know them yet).
func baseMovement(a protocol.Attribute) float64 {
	v := float64(a.Value)
	for _, m := range a.Modifiers {
		if m.ID == "d208fc00-42aa-4aad-9276-d5446530de43" || strings.Contains(strings.ToLower(m.Name), "sprint") {
			if m.Operation == 2 || m.Operation == 1 {
				v /= 1 + float64(m.Amount)
			} else {
				v -= float64(m.Amount)
			}
		}
	}
	if v <= 0 {
		v = 0.1
	}
	return v
}

// setGameType applies a game type the server set for the player.
func (p *Player) setGameType(t int32) {
	s := &p.self
	if t == 5 {
		t = p.rem.gd.WorldGameMode
	}
	s.baseMode, s.mode = t, javaGameMode(t)
	p.sendGameMode(s.mode)
	p.sendAbilities(s.mode, s.mayFly, s.flying, s.flySpeed)
	p.sendMovementAttributes()
}

// selfMetadata applies the player's own entity data: immobile (a server freezing the player).
func (p *Player) selfMetadata(meta protocol.EntityMetadata) {
	flags, ok := meta[protocol.EntityDataKeyFlags].(int64)
	if !ok {
		return
	}
	p.self.immobile = flags&(1<<protocol.EntityDataFlagNoAI) != 0
	p.selfUsing(flags&(1<<protocol.EntityDataFlagUsingItem) != 0)
}

// changeDimension moves the player into a new, empty world of the dimension the server moved
// them to.
func (p *Player) changeDimension(pk *packet.ChangeDimension) {
	// ChangeDimension carries the feet position (BDS: the /tp target exactly), unlike MovePlayer.
	pos := vec64(pk.Position)
	p.log.Debug("dimension change", "dim", pk.Dimension, "pos", pos)
	p.leaveWorld()
	p.resetState(false)
	p.world = newWorld(p.rem.pal, pk.Dimension)
	s := &p.self
	s.pos, s.searching = pos, true
	p.mv.reset(pos, s.yaw, s.pitch)
	p.sendRespawn(javaDimension(pk.Dimension), s.mode, 3)
	p.showSelf(true)
	// The server waits for the client to finish loading the new dimension before it simulates the
	// player again: that is when the Java client closes its loading screen (player_loaded), or
	// after dimLoadTimeout for a client that does not say.
	r, entity := p.rem, p.rem.gd.EntityRuntimeID
	screen, hasScreen := pk.LoadingScreenID.Value()
	done := false
	p.dimLoaded = func() {
		if done || p.rem != r {
			return
		}
		done = true
		r.write(&packet.PlayerAction{EntityRuntimeID: entity, ActionType: protocol.PlayerActionDimensionChangeDone})
		if hasScreen {
			r.write(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeStart, LoadingScreenID: protocol.Option(screen)})
			r.write(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeEnd, LoadingScreenID: protocol.Option(screen)})
		}
	}
	f := p.dimLoaded
	p.after(dimLoadTimeout, "dimension load", func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		f()
	})
}

// dimLoadTimeout is how long a dimension change waits for the Java client's player_loaded.
const dimLoadTimeout = 5 * time.Second

// columnArrived runs when a column became complete: the spawn search looks for the ground.
func (p *Player) columnArrived(pos ChunkPos) {
	s := &p.self
	if !s.searching {
		return
	}
	x, z := int(mfloor(s.pos[0])), int(mfloor(s.pos[2]))
	if pos != (ChunkPos{int32(x >> 4), int32(z >> 4)}) {
		return
	}
	s.searching = false
	to := p.world.standingPos(s.pos)
	if to == s.pos {
		return
	}
	p.log.Debug("spawn search", "from", s.pos, "to", to)
	s.pos = to
	p.teleportTo(to, p.mv.yaw, p.mv.pitch)
}

// opLevelOf is the Java op level for a Bedrock player who is (or is not) an operator.
func opLevelOf(operator bool) byte {
	if operator {
		return 4
	}
	return 2
}

// sendOpLevel gives the Java client its op level (entity_event 24-28 on its own entity).
func (p *Player) sendOpLevel() {
	lvl := p.self.opLevel
	if lvl == 0 {
		lvl = 2
	}
	p.sendEntityEvent(SelfEntityID, 24+lvl)
}

// dayClock reports whether clock id is the one that gives the time of day: the overworld's, or
// the only one (also when the server sent no clock names).
func dayClock(names map[uint64]string, id uint64) bool {
	if len(names) <= 1 {
		return true
	}
	return strings.HasSuffix(strings.ToLower(names[id]), "overworld")
}
