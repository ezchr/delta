package bridge

import (
	"math"

	"github.com/df-mc/dragonfly/server/java/protocol/text"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/version"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Java clientbound packets, encoded as javasession encodes them (26.3 layout; the connection
// remaps the ids for 26.2 clients).

// SelfEntityID is the entity id the Java client knows itself by. Other entities get ids from the
// entity map (entities.go), which never hands out this one.
const SelfEntityID = 1

// simulationDistance is the simulation distance Java clients are told.
const simulationDistance = 4

// Java dimensions (also their dimension type keys).
const (
	DimOverworld = "minecraft:overworld"
	DimNether    = "minecraft:the_nether"
	DimEnd       = "minecraft:the_end"
)

// dimSections is how many chunk sections a Java client expects in a dimension type.
func dimSections(dim string) int {
	switch dim {
	case DimNether, DimEnd:
		return 16 // y 0 to 255
	}
	return 24 // y -64 to 319
}

// dimMinY is the lowest block y of a Java dimension type.
func dimMinY(dim string) int {
	if dim == DimOverworld {
		return -64
	}
	return 0
}

// Java game modes.
const (
	GameSurvival  = 0
	GameCreative  = 1
	GameAdventure = 2
	GameSpectator = 3
)

func (p *Player) pk() *wire.Writer              { return p.java.Packet() }
func (p *Player) send(id int32, w *wire.Writer) { p.java.Queue(id, w) }
func (p *Player) ver() *version.Version         { return p.java.ver }

// writeText writes Bedrock-formatted text (§ codes) as a component.
func writeText(w *wire.Writer, s string) {
	c := text.Legacy(s, text.Bedrock)
	c.Write(w)
}

// sendLogin sends the play login packet into dim with game mode mode.
func (p *Player) sendLogin(dim string, mode int32) {
	w := p.pk()
	w.Int32(SelfEntityID)
	w.Bool(false) // hardcore
	w.VarInt(3)
	w.String(DimOverworld)
	w.String(DimNether)
	w.String(DimEnd)
	w.VarInt(100) // max players (tab list layout only)
	w.VarInt(p.viewDistance)
	w.VarInt(min(p.viewDistance, simulationDistance))
	w.Bool(false) // reduced debug info
	w.Bool(true)  // death screen
	w.Bool(false) // limited crafting
	w.VarInt(p.ver().RegistryID("minecraft:dimension_type", dim))
	w.String(dim)
	w.Int64(0)     // hashed seed
	w.VarInt(mode) // game mode
	w.VarInt(0)    // previous game mode (javasession's encoding)
	w.Bool(false)  // debug world
	w.Bool(false)  // flat
	w.Bool(false)  // no death location
	w.VarInt(0)    // portal cooldown
	w.VarInt(63)   // sea level
	w.Bool(false)  // online mode (javasession's layout)
	w.Bool(false)  // enforces secure chat
	p.send(v777.ClientboundPlayLogin, w)
	p.javaDim = dim
	p.javaMode = mode
}

// sendRespawn sends a respawn into dim. keep: 1 attributes, 2 entity data.
func (p *Player) sendRespawn(dim string, mode int32, keep byte) {
	w := p.pk()
	w.VarInt(p.ver().RegistryID("minecraft:dimension_type", dim))
	w.String(dim)
	w.Int64(0)
	w.VarInt(mode)
	w.VarInt(0)
	w.Bool(false)
	w.Bool(false)
	w.Bool(false) // no death location
	w.VarInt(0)
	w.VarInt(63)
	w.Byte(keep)
	p.send(v777.ClientboundPlayRespawn, w)
	p.javaDim = dim
	p.javaMode = mode
}

// sendWaitForChunks is game event 13: the client waits on "Loading terrain" for its chunk.
func (p *Player) sendWaitForChunks() { p.sendGameEvent(13, 0) }

func (p *Player) sendGameEvent(event byte, v float32) {
	w := p.pk()
	w.Byte(event)
	w.Float32(v)
	p.send(v777.ClientboundPlayGameEvent, w)
}

// sendSpawnPoint sends the world spawn (compass, respawn screen position).
func (p *Player) sendSpawnPoint(x, y, z int) {
	w := p.pk()
	w.String(p.javaDim)
	w.Position(x, y, z)
	w.Float32(0)
	w.Float32(0)
	p.send(v777.ClientboundPlaySetDefaultSpawnPosition, w)
}

// Relative flags of player_position.
const (
	relX = 1 << iota
	relY
	relZ
	relYaw
	relPitch
	relDX
	relDY
	relDZ
	relRotateDelta
)

// sendPosition sends player_position: pos and rotation, with the flags saying which values are
// relative. It returns the teleport id the client answers with accept_teleportation.
func (p *Player) sendPosition(pos mgl64.Vec3, yaw, pitch float32, flags int32) int32 {
	return p.sendPositionVel(pos, mgl64.Vec3{}, yaw, pitch, flags)
}

// sendPositionVel is sendPosition with a velocity (blocks per tick; relative with relDX..).
func (p *Player) sendPositionVel(pos, vel mgl64.Vec3, yaw, pitch float32, flags int32) int32 {
	p.tpID++
	w := p.pk()
	w.VarInt(p.tpID)
	w.Float64(pos[0])
	w.Float64(pos[1])
	w.Float64(pos[2])
	w.Float64(vel[0])
	w.Float64(vel[1])
	w.Float64(vel[2])
	w.Float32(yaw)
	w.Float32(pitch)
	w.Int32(flags)
	p.send(v777.ClientboundPlayPlayerPosition, w)
	return p.tpID
}

// sendCentre sends set_chunk_cache_center when the player's chunk changed.
func (p *Player) sendCentre(pos mgl64.Vec3, force bool) {
	cx, cz := int32(math.Floor(pos[0]))>>4, int32(math.Floor(pos[2]))>>4
	if !force && p.centreSent && cx == p.centre[0] && cz == p.centre[1] {
		return
	}
	p.centre, p.centreSent = [2]int32{cx, cz}, true
	w := p.pk()
	w.VarInt(cx)
	w.VarInt(cz)
	p.send(v777.ClientboundPlaySetChunkCacheCenter, w)
}

// sendAbilities sends player_abilities for a Java game mode.
func (p *Player) sendAbilities(mode int32, mayFly, flying bool, flySpeed float32) {
	var flags byte
	if mode == GameCreative || mode == GameSpectator {
		flags |= 1 // invulnerable
	}
	if flying {
		flags |= 2
	}
	if mayFly || mode == GameCreative || mode == GameSpectator {
		flags |= 4
	}
	if mode == GameCreative {
		flags |= 8 // instant break
	}
	if flySpeed <= 0 {
		flySpeed = 0.05
	}
	w := p.pk()
	w.Byte(flags)
	w.Float32(flySpeed)
	w.Float32(0.1) // walking speed (field of view)
	p.send(v777.ClientboundPlayPlayerAbilities, w)
}

// sendGameMode is game event 3 (change game mode).
func (p *Player) sendGameMode(mode int32) {
	p.sendGameEvent(3, float32(mode))
	p.javaMode = mode
}

// sendHealth sends set_health.
func (p *Player) sendHealth(health float32, food int32, saturation float32) {
	w := p.pk()
	w.Float32(health)
	w.VarInt(food)
	w.Float32(saturation)
	p.send(v777.ClientboundPlaySetHealth, w)
}

// sendAbsorption sets the golden hearts (player entity data 17, a float).
func (p *Player) sendAbsorption(v float32) {
	w := p.pk()
	w.VarInt(SelfEntityID)
	w.Byte(17)
	w.VarInt(3) // float
	w.Float32(v)
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
}

// sendExperience sends set_experience.
func (p *Player) sendExperience(progress float32, level int32) {
	w := p.pk()
	w.Float32(progress)
	w.VarInt(level)
	w.VarInt(0)
	p.send(v777.ClientboundPlaySetExperience, w)
}

// sprintModifierAttr is sendAttributesMod's modifier: the Java client's own sprint modifier.
const sprintModifierAttr = 1

// sendAttributes sets base values of the player's own attributes (registry names such as
// "minecraft:step_height"), without modifiers.
func (p *Player) sendAttributes(names []string, values []float64) {
	p.sendAttributesMod("", 0, names, values)
}

// sendAttributesMod is sendAttributes giving the attribute modName the modifier mod (0: none).
func (p *Player) sendAttributesMod(modName string, mod int, names []string, values []float64) {
	type attr struct {
		id  int32
		val float64
		mod int
	}
	var list []attr
	var legacy []int32
	if !p.ver().Native() {
		legacy = p.ver().BuiltinTable("minecraft:attribute")
	}
	for i, name := range names {
		id := v777.BuiltinID("minecraft:attribute", name)
		if id < 0 {
			continue
		}
		if legacy != nil {
			if id = version.Map(legacy, id); id < 0 {
				continue
			}
		}
		m := 0
		if name == modName {
			m = mod
		}
		list = append(list, attr{id, values[i], m})
	}
	if len(list) == 0 {
		return
	}
	w := p.pk()
	w.VarInt(SelfEntityID)
	w.VarInt(int32(len(list)))
	for _, a := range list {
		w.VarInt(a.id)
		w.Float64(a.val)
		if a.mod == sprintModifierAttr {
			// LivingEntity.SPEED_MODIFIER_SPRINTING: +30%, add_multiplied_total.
			w.VarInt(1)
			w.String("minecraft:sprinting")
			w.Float64(0.3)
			w.Byte(2)
			continue
		}
		w.VarInt(0)
	}
	p.send(v777.ClientboundPlayUpdateAttributes, w)
}

// sendTime sets the time of day (26.3 world clocks; rate 0 stops the clock).
func (p *Player) sendTime(t int64, cycle bool) {
	rate := float32(1)
	if !cycle {
		rate = 0
	}
	w := p.pk()
	w.Int64(t)
	w.VarInt(1)
	w.VarInt(p.ver().RegistryID("minecraft:world_clock", "minecraft:overworld"))
	w.VarLong(t)
	w.Float32(0)
	w.Float32(rate)
	p.send(v777.ClientboundPlaySetTime, w)
}

// sendWeather shows rain (level 0-1) and thunder (level 0-1).
func (p *Player) sendWeather(rain, thunder float32) {
	if rain > 0 {
		p.sendGameEvent(1, 0) // start raining
	} else {
		p.sendGameEvent(2, 0) // stop raining
	}
	p.sendGameEvent(7, rain)
	p.sendGameEvent(8, thunder)
}

// sendSystem sends a system_chat message (overlay: above the hotbar).
func (p *Player) sendSystem(c *text.Component, overlay bool) {
	w := p.pk()
	c.Write(w)
	w.Bool(overlay)
	p.send(v777.ClientboundPlaySystemChat, w)
}

// Message sends a chat line in Bedrock formatting.
func (p *Player) Message(s string) {
	c := text.Legacy(s, text.Bedrock)
	p.sendSystem(&c, false)
}

// BridgeMessage sends a line from the bridge itself.
func (p *Player) BridgeMessage(s string) { p.Message("§7[§b" + ProductName + "§7]§r " + s) }

// sendClickable sends a chat line that runs command when clicked.
func (p *Player) sendClickable(message, command, hover string) {
	c := text.Legacy(message, text.Bedrock)
	c.Click = &text.ClickEvent{Action: text.ClickRunCommand, Value: command}
	if hover != "" {
		h := text.Legacy(hover, text.Bedrock)
		c.Hover = &h
	}
	p.sendSystem(&c, false)
}

// sendSuggest sends a chat line that types command into the chat box when clicked.
func (p *Player) sendSuggest(message, command, hover string) {
	c := text.Legacy(message, text.Bedrock)
	c.Click = &text.ClickEvent{Action: text.ClickSuggestCommand, Value: command}
	if hover != "" {
		h := text.Legacy(hover, text.Bedrock)
		c.Hover = &h
	}
	p.sendSystem(&c, false)
}

// sendActionBar sets the action bar text.
func (p *Player) sendActionBar(s string) {
	w := p.pk()
	writeText(w, s)
	p.send(v777.ClientboundPlaySetActionBarText, w)
}

func (p *Player) sendTitle(s string) {
	w := p.pk()
	writeText(w, s)
	p.send(v777.ClientboundPlaySetTitleText, w)
}

func (p *Player) sendSubtitle(s string) {
	w := p.pk()
	writeText(w, s)
	p.send(v777.ClientboundPlaySetSubtitleText, w)
}

// sendClearTitles clears (reset: and resets the times of) the title.
func (p *Player) sendClearTitles(reset bool) {
	w := p.pk()
	w.Bool(reset)
	p.send(v777.ClientboundPlayClearTitles, w)
}

// sendTitleTimes sets fade in, stay and fade out in ticks.
func (p *Player) sendTitleTimes(in, stay, out int32) {
	w := p.pk()
	w.Int32(in)
	w.Int32(stay)
	w.Int32(out)
	p.send(v777.ClientboundPlaySetTitlesAnimation, w)
}

// sendCombatKill shows the death screen with a message.
func (p *Player) sendCombatKill(msg string) {
	w := p.pk()
	w.VarInt(SelfEntityID)
	writeText(w, msg)
	p.send(v777.ClientboundPlayPlayerCombatKill, w)
}

// sendForgetChunk sends forget_level_chunk.
func (p *Player) sendForgetChunk(x, z int32) {
	w := p.pk()
	w.Int64(int64(uint64(uint32(x)) | uint64(uint32(z))<<32))
	p.send(v777.ClientboundPlayForgetLevelChunk, w)
}

// sendBlockUpdate sends block_update with a 26.3 state.
func (p *Player) sendBlockUpdate(x, y, z int, state uint32) {
	w := p.pk()
	w.Position(x, y, z)
	w.VarInt(p.ver().BlockState(int32(state)))
	p.send(v777.ClientboundPlayBlockUpdate, w)
}

// sendPluginMessage sends a custom_payload on channel with a raw body.
func (p *Player) sendPluginMessage(channel string, body []byte) {
	w := p.pk()
	w.String(channel)
	w.Raw(body)
	p.send(v777.ClientboundPlayCustomPayload, w)
}

// ProxyConnect asks a BungeeCord/Velocity proxy in front to move the player to server.
func (p *Player) ProxyConnect(server string) {
	p.sendPluginMessage("bungeecord:main", append(javaUTF("Connect"), javaUTF(server)...))
}

// javaUTF is s as Java DataOutput.writeUTF writes it (big-endian uint16 length, then the bytes).
func javaUTF(s string) []byte {
	b := make([]byte, 0, 2+len(s))
	b = append(b, byte(len(s)>>8), byte(len(s)))
	return append(b, s...)
}

// Transfer sends the client to host:port (transfer packet).
func (p *Player) Transfer(host string, port int) {
	w := p.pk()
	w.String(host)
	w.VarInt(int32(port))
	p.send(v777.ClientboundPlayTransfer, w)
}

// StoreCookie stores a cookie on the client (at most 5 KiB).
func (p *Player) StoreCookie(key string, value []byte) bool {
	if len(value) > 5120 {
		return false
	}
	w := p.pk()
	w.String(key)
	w.VarInt(int32(len(value)))
	w.Raw(value)
	p.send(v777.ClientboundPlayStoreCookie, w)
	return true
}

// sendKeepAlive sends a keep-alive with id.
func (p *Player) sendKeepAlive(id int64) {
	w := p.pk()
	w.Int64(id)
	p.send(v777.ClientboundPlayKeepAlive, w)
}

// sendSelfTab adds the player's own tab list entry (skin, game mode, listed), which the client
// looks itself up by.
func (p *Player) sendSelfTab(mode int32) {
	const (
		actAdd     = 0x01
		actMode    = 0x04
		actListed  = 0x08
		actLatency = 0x10
	)
	w := p.pk()
	w.Byte(actAdd | actMode | actListed | actLatency)
	w.VarInt(1)
	w.UUID(p.profile.UUID)
	w.String(p.profile.Name)
	w.VarInt(int32(len(p.profile.Properties)))
	for _, pr := range p.profile.Properties {
		w.String(pr.Name)
		w.String(pr.Value)
		w.Bool(pr.Signature != "")
		if pr.Signature != "" {
			w.String(pr.Signature)
		}
	}
	w.VarInt(mode)
	w.Bool(true)
	w.VarInt(0)
	p.send(v777.ClientboundPlayPlayerInfoUpdate, w)
}

// sendSkinParts sends the player's own skin layers (entity data 16).
func (p *Player) sendSkinParts() {
	w := p.pk()
	w.VarInt(SelfEntityID)
	w.Byte(16)
	w.VarInt(0) // byte
	w.Byte(p.info.SkinParts)
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
}

// stopJavaSprint ends the Java client's sprint itself: its shared flags without the sprint bit and
// the movement speed without the sprint modifier. Java 26 keeps sprinting while crouching; BDS
// stops a sprint when the player sneaks, so a sprinting Java player that crouched moved at sprint
// speed x0.3 (with the sprint jump boost) while BDS walked: corrected every few ticks. A crouching
// client does not start sprinting again (canStartSprinting); it says STOP_SPRINTING itself.
func (p *Player) stopJavaSprint() {
	f := p.self.bflags
	var v byte = 0x02 // crouching
	if f&(1<<protocol.EntityDataFlagOnFire) != 0 {
		v |= 0x01
	}
	if f&(1<<protocol.EntityDataFlagSwimming) != 0 {
		v |= 0x10
	}
	if f&(1<<protocol.EntityDataFlagInvisible) != 0 {
		v |= 0x20
	}
	if f&(1<<protocol.EntityDataFlagGliding) != 0 {
		v |= 0x80
	}
	w := p.pk()
	w.VarInt(SelfEntityID)
	w.Byte(0)
	w.VarInt(serByte)
	w.Byte(v)
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
	p.mv.sprinting = false
	p.sendMovementAttributes()
}
