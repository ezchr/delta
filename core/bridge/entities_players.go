package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Other players: the server's player list becomes the Java tab list, AddPlayer a player entity.
// A Java client needs a player's tab entry (profile) before it can spawn them, and keeps the skin
// it first drew them with, so a player whose skin is still being looked up (GeyserMC's global
// skin API, by XUID: the Mojang-signed texture Floodgate servers uploaded) is spawned once the
// lookup is done, or after skinWait without it (the client then draws its default skin).

// tabEntry is a player as the Java tab list has it.
type tabEntry struct {
	id       uuid.UUID
	name     string // Java profile name (16 characters, no formatting)
	display  string // the Bedrock name, when the profile name is not it
	xuid     string
	uniq     int64 // the server's unique id of the player (scoreboard lines name it)
	props    []jserver.Property
	gameMode int32
	sent     bool // the client has it
	listed   bool // in the server's player list (else: made for an entity only)
	waiting  bool // its skin is being looked up
}

// Java player_info_update actions.
const (
	tabAdd     = 0x01
	tabMode    = 0x04
	tabListed  = 0x08
	tabLatency = 0x10
	tabDisplay = 0x20
)

// skinWait is how long a player is held back for their skin.
const skinWait = 3 * time.Second

func init() {
	OnBedrock(packet.IDPlayerList, (*Player).playerList)
	OnBedrock(packet.IDAddPlayer, (*Player).addPlayer)
	OnBedrock(packet.IDUpdatePlayerGameType, func(p *Player, pk *packet.UpdatePlayerGameType) {
		if pk.PlayerUniqueID == p.rem.gd.EntityUniqueID {
			return // self.go
		}
		if e := p.entityByUnique(pk.PlayerUniqueID); e != nil {
			if te := p.ents.tab[e.uuid]; te != nil && te.sent {
				te.gameMode = javaGameMode(pk.GameType)
				w := p.pk()
				w.Byte(tabMode)
				w.VarInt(1)
				w.UUID(te.id)
				w.VarInt(te.gameMode)
				p.send(v777.ClientboundPlayPlayerInfoUpdate, w)
			}
		}
	})
	OnReset(func(p *Player, newConn bool) {
		if !newConn {
			return // the tab list stays over a dimension change
		}
		var ids []uuid.UUID
		for id, te := range p.ents.tab {
			if te.sent {
				ids = append(ids, id)
			}
		}
		p.removeTab(ids...)
		clear(p.ents.tab)
		clear(p.ents.javaUUIDs)
	})
}

// isSelf reports whether a player list entry is the player themselves.
func (p *Player) isSelf(en *protocol.PlayerListEntry) bool {
	if en.EntityUniqueID == p.rem.gd.EntityUniqueID || en.UUID == p.profile.UUID {
		return true
	}
	id := p.rem.conn.IdentityData()
	return (en.XUID != "" && en.XUID == id.XUID) || (id.XUID == "" && en.Username == id.DisplayName)
}

// playerList applies the server's player list.
func (p *Player) playerList(pk *packet.PlayerList) {
	tab := p.ents.tab
	for i := range pk.Entries {
		en := &pk.Entries[i]
		if en.ActionType != protocol.PlayerListActionAdd {
			id, ok := p.ents.javaUUIDs[en.UUID]
			if !ok {
				continue
			}
			te := tab[id]
			if te == nil {
				continue
			}
			te.listed = false
			if p.playerShown(id) {
				if te.sent {
					p.sendTabListed(te)
				}
				continue
			}
			delete(tab, id)
			if te.sent {
				p.removeTab(id)
			}
			continue
		}
		if p.isSelf(en) {
			continue
		}
		id := p.javaUUID(en.UUID, skinSlim(&en.Skin))
		te := tab[id]
		if te != nil {
			wasListed := te.listed
			te.listed = true
			te.uniq = en.EntityUniqueID
			if te.sent && !wasListed {
				p.sendTabListed(te)
			}
			continue
		}
		te = &tabEntry{id: id, name: javaName(en.Username), xuid: en.XUID, listed: true, uniq: en.EntityUniqueID}
		if te.name != en.Username {
			te.display = en.Username
		}
		tab[id] = te
		p.lookUpSkin(te)
	}
}

// lookUpSkin sends a new tab entry now if its skin is known (or there is none to look up), else
// once the lookup is done.
func (p *Player) lookUpSkin(te *tabEntry) {
	if te.xuid == "" || geyserSkinURL == "" {
		p.sendTabAdd(te)
		return
	}
	sk := skinOf(te.xuid)
	select {
	case <-sk.done:
		te.props = sk.props
		p.sendTabAdd(te)
		return
	default:
	}
	te.waiting = true
	r, id := p.rem, te.id
	p.goSafe("tab skin wait", func() {
		select {
		case <-sk.done:
		case <-time.After(skinWait):
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.rem != r {
			return
		}
		te := p.ents.tab[id]
		if te == nil || !te.waiting {
			return
		}
		te.waiting = false
		te.props = sk.result()
		p.sendTabAdd(te)
		for _, e := range p.ents.shown {
			if !e.spawned && e.uuid == id {
				p.showEntity(e)
			}
		}
	})
}

// sendTabAdd sends a tab entry to the client.
func (p *Player) sendTabAdd(te *tabEntry) {
	te.sent = true
	w := p.pk()
	writeTabAdd(w, te)
	p.send(v777.ClientboundPlayPlayerInfoUpdate, w)
}

// writeTabAdd writes the player_info_update that adds a tab entry: profile (name, skin),
// game mode, listed, latency, display name.
func writeTabAdd(w *wire.Writer, te *tabEntry) {
	w.Byte(tabAdd | tabMode | tabListed | tabLatency | tabDisplay)
	w.VarInt(1)
	w.UUID(te.id)
	w.String(te.name)
	w.VarInt(int32(len(te.props)))
	for _, pr := range te.props {
		w.String(pr.Name)
		w.String(pr.Value)
		w.Bool(pr.Signature != "")
		if pr.Signature != "" {
			w.String(pr.Signature)
		}
	}
	w.VarInt(te.gameMode)
	w.Bool(te.listed)
	w.VarInt(0)
	w.Bool(te.display != "")
	if te.display != "" {
		writeText(w, te.display)
	}
}

func (p *Player) sendTabListed(te *tabEntry) {
	w := p.pk()
	w.Byte(tabListed)
	w.VarInt(1)
	w.UUID(te.id)
	w.Bool(te.listed)
	p.send(v777.ClientboundPlayPlayerInfoUpdate, w)
}

// removeTab removes tab entries from the client.
func (p *Player) removeTab(ids ...uuid.UUID) {
	if len(ids) == 0 {
		return
	}
	w := p.pk()
	w.VarInt(int32(len(ids)))
	for _, id := range ids {
		w.UUID(id)
	}
	p.send(v777.ClientboundPlayPlayerInfoRemove, w)
}

// playerShown reports whether a player entity with this UUID is tracked.
func (p *Player) playerShown(id uuid.UUID) bool {
	for _, e := range p.ents.shown {
		if e.uuid == id && e.t.kind&entPlayer != 0 {
			return true
		}
	}
	return false
}

// playerGone drops the tab entry of a removed player entity, unless the player is still listed.
func (p *Player) playerGone(e *entity) {
	te := p.ents.tab[e.uuid]
	if te == nil || te.listed {
		return
	}
	delete(p.ents.tab, e.uuid)
	if te.sent {
		p.removeTab(e.uuid)
	}
}

// addPlayer shows another player.
func (p *Player) addPlayer(pk *packet.AddPlayer) {
	t := entityTypes["minecraft:player"]
	id := pk.UUID
	if id == uuid.Nil || id == p.profile.UUID {
		id = entityUUID(pk.EntityRuntimeID)
	} else {
		id = p.javaUUID(id, false)
	}
	meta := pk.EntityMetadata
	if meta == nil {
		meta = protocol.EntityMetadata{}
	}
	e := &entity{rid: pk.EntityRuntimeID, uniq: pk.AbilityData.EntityUniqueID, bedrock: "minecraft:player", t: t,
		java: t.java, uuid: id, meta: meta}
	e.flags, _ = meta[protocol.EntityDataKeyFlags].(int64)
	e.flags2, _ = meta[protocol.EntityDataKeyFlagsTwo].(int64)
	// AddPlayer has the feet position (MovePlayer the eyes).
	e.pos = vec64(pk.Position)
	e.vel = vec64(pk.Velocity)
	e.yaw, e.pitch, e.head = pk.Yaw, pk.Pitch, pk.HeadYaw
	if pk.HeldItem.Stack.NetworkID != 0 {
		e.equip[0], e.hasEquip = pk.HeldItem.Stack, true
	}
	if !p.trackEntity(e) {
		return
	}
	p.log.Debug("add player", "name", pk.Username, "rid", pk.EntityRuntimeID, "pos", e.pos, "java", e.id)
	te := p.ents.tab[id]
	if te == nil {
		// Not in the player list (an NPC): an unlisted entry of its own.
		te = &tabEntry{id: id, name: javaName(pk.Username)}
		p.ents.tab[id] = te
		e.ownTab = true
		p.sendTabAdd(te)
	}
	if te.uniq == 0 {
		te.uniq = pk.AbilityData.EntityUniqueID
	}
	te.gameMode = javaGameMode(pk.GameType)
	if !te.waiting {
		p.showEntity(e)
	}
	for _, l := range pk.EntityLinks {
		p.applyLink(l)
	}
}

// revivePlayer shows a player the server had said died alive again: Java keeps a dead entity
// lying down, Bedrock the same actor for the respawned player.
func (p *Player) revivePlayer(e *entity) {
	if !e.dead || !e.spawned {
		return
	}
	e.dead = false
	if e.hasHealth && e.health <= 0 {
		e.health = 20
	}
	w := p.pk()
	w.VarInt(1)
	w.VarInt(e.id)
	p.send(v777.ClientboundPlayRemoveEntities, w)
	p.showEntity(e)
}

// checkRevive revives a dead player that moved away from where it died.
func (p *Player) checkRevive(e *entity, pos mgl64.Vec3) {
	if e.dead && e.t.kind&entPlayer != 0 && time.Since(e.diedAt) > 2*time.Second && pos.Sub(e.diedPos).Len() > 1 {
		p.revivePlayer(e)
	}
}

// Skins from GeyserMC's global skin API (javasession's geyserskin.go, without Dragonfly).

// geyserSkinURL is where skins are looked up by XUID ("" turns lookups off; tests point it at
// their own server).
var geyserSkinURL = "https://api.geysermc.org/v2/skin/"

type skinLookup struct {
	done  chan struct{}
	at    time.Time
	props []jserver.Property // nil: none (never uploaded, or the lookup failed)
}

func (s *skinLookup) result() []jserver.Property {
	select {
	case <-s.done:
		skinMu.Lock()
		defer skinMu.Unlock()
		return s.props
	default:
		return nil
	}
}

var (
	skinMu    sync.Mutex
	skins     = map[string]*skinLookup{}
	skinHTTP  = &http.Client{Timeout: 5 * time.Second}
	skinSlots = make(chan struct{}, 4)
)

// skinOf returns the lookup of a XUID's skin, starting one if there is none or it is stale.
func skinOf(xuid string) *skinLookup {
	skinMu.Lock()
	defer skinMu.Unlock()
	if sk, ok := skins[xuid]; ok {
		select {
		case <-sk.done:
			stale := 10 * time.Minute
			if sk.props != nil {
				stale = time.Hour
			}
			if time.Since(sk.at) < stale {
				return sk
			}
		default:
			return sk // in flight
		}
	}
	sk := &skinLookup{done: make(chan struct{})}
	skins[xuid] = sk
	goSafe("skin lookup", func() {
		defer func() {
			select {
			case <-sk.done:
			default:
				close(sk.done)
			}
		}()
		props := fetchGeyserSkin(xuid)
		skinMu.Lock()
		sk.props, sk.at = props, time.Now()
		skinMu.Unlock()
		close(sk.done)
	}, nil)
	return sk
}

// fetchGeyserSkin looks a skin up; nil when there is none or the lookup failed.
func fetchGeyserSkin(xuid string) []jserver.Property {
	skinSlots <- struct{}{}
	defer func() { <-skinSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geyserSkinURL+xuid, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", ProductName+" (Java to Bedrock bridge)")
	resp, err := skinHTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		Value     string `json:"value"`
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil
	}
	if body.Value == "" || body.Signature == "" {
		return nil
	}
	return []jserver.Property{{Name: "textures", Value: body.Value, Signature: body.Signature}}
}

// Default skins. A Java client draws a player without (Mojang-signed) skin textures with one of
// its 18 default skins, picked by the profile UUID's hash; unsigned textures (a Steve or Alex
// texture URL without Mojang's signature) are ignored for other players. A player whose skin the
// Geyser API does not have gets a Java UUID (the server's with its last byte changed) that picks
// Steve, or Alex for a slim (thin-armed) Bedrock skin.

// Indices of the default skins in the client's list (DefaultPlayerSkin: 9 slim, then 9 wide).
const (
	defaultSkins     = 18
	defaultSkinAlex  = 0  // slim/alex
	defaultSkinSteve = 15 // wide/steve
)

// skinSlim reports whether a Bedrock skin has slim arms.
func skinSlim(s *protocol.Skin) bool {
	return s.ArmSize == protocol.ArmSizeSlim && len(s.SkinData) > 0 || bytes.Contains(s.SkinResourcePatch, []byte("Slim"))
}

// javaUUIDHash is Java's UUID.hashCode.
func javaUUIDHash(id uuid.UUID) int32 {
	var msb, lsb uint64
	for i := 0; i < 8; i++ {
		msb = msb<<8 | uint64(id[i])
		lsb = lsb<<8 | uint64(id[8+i])
	}
	hilo := msb ^ lsb
	return int32(hilo>>32) ^ int32(hilo)
}

// defaultSkinOf is the default skin index the Java client picks for a UUID.
func defaultSkinOf(id uuid.UUID) int {
	h := int(javaUUIDHash(id)) % defaultSkins
	if h < 0 {
		h += defaultSkins
	}
	return h
}

// defaultSkinUUID changes the last byte of id so the Java client's default skin for it is Steve
// (wide) or Alex (slim).
func defaultSkinUUID(id uuid.UUID, slim bool) uuid.UUID {
	want := defaultSkinSteve
	if slim {
		want = defaultSkinAlex
	}
	for b := 0; b < 256; b++ {
		c := id
		c[15] ^= byte(b)
		if defaultSkinOf(c) == want {
			return c
		}
	}
	return id
}

// javaUUID is the Java UUID of a server player UUID: the one its player list entry got, else a
// new one (slim: the Bedrock skin has slim arms).
func (p *Player) javaUUID(id uuid.UUID, slim bool) uuid.UUID {
	if j, ok := p.ents.javaUUIDs[id]; ok {
		return j
	}
	j := defaultSkinUUID(id, slim)
	if p.ents.javaUUIDs == nil {
		p.ents.javaUUIDs = map[uuid.UUID]uuid.UUID{}
	}
	p.ents.javaUUIDs[id] = j
	return j
}
