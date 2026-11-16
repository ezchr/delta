package bridge

import (
	"encoding/binary"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Boss bars: Bedrock BossEvent (by the boss's unique id, or any id a server picks for a bar of
// its own) -> Java boss_event (by a UUID made from that id).

// bossBar is a boss bar the Java client shows.
type bossBar struct {
	title         string
	health        float32
	colour, style int32
}

// Bedrock boss bar colours (pink, blue, red, green, yellow, purple, rebecca purple, white) ->
// Java's (pink, blue, red, green, yellow, purple, white).
var bossColours = [...]int32{0, 1, 2, 3, 4, 5, 5, 6}

func bossUUID(id int64) uuid.UUID {
	var u uuid.UUID
	copy(u[:], "bb2boss!")
	binary.BigEndian.PutUint64(u[8:], uint64(id))
	u[6] = u[6]&0x0f | 0x40
	return u
}

func init() {
	OnBedrock(packet.IDBossEvent, (*Player).bossEvent)
	OnReset(func(p *Player, newConn bool) {
		if !newConn {
			return
		}
		for id := range p.ents.bosses {
			p.sendBossRemove(id)
		}
		clear(p.ents.bosses)
	})
}

// bossEvent shows a BossEvent.
func (p *Player) bossEvent(pk *packet.BossEvent) {
	if p.ents.bosses == nil {
		p.ents.bosses = map[int64]*bossBar{}
	}
	b := p.ents.bosses[pk.BossEntityUniqueID]
	switch pk.EventType {
	case packet.BossEventShow:
		colour := int32(0)
		if int(pk.Colour) < len(bossColours) {
			colour = bossColours[pk.Colour]
		}
		nb := &bossBar{title: translate(pk.BossBarTitle, nil), health: clamp01(pk.HealthPercentage), colour: colour, style: int32(min(pk.Overlay, 4))}
		if b != nil {
			// Servers show a bar again and again (BDS every second): update what changed.
			id := bossUUID(pk.BossEntityUniqueID)
			if nb.health != b.health {
				w := p.pk()
				w.UUID(id)
				w.VarInt(2)
				w.Float32(nb.health)
				p.send(v777.ClientboundPlayBossEvent, w)
			}
			if nb.title != b.title {
				w := p.pk()
				w.UUID(id)
				w.VarInt(3)
				writeText(w, nb.title)
				p.send(v777.ClientboundPlayBossEvent, w)
			}
			if nb.colour != b.colour || nb.style != b.style {
				w := p.pk()
				w.UUID(id)
				w.VarInt(4)
				w.VarInt(nb.colour)
				w.VarInt(nb.style)
				p.send(v777.ClientboundPlayBossEvent, w)
			}
			*b = *nb
			return
		}
		p.ents.bosses[pk.BossEntityUniqueID] = nb
		w := p.pk()
		w.UUID(bossUUID(pk.BossEntityUniqueID))
		w.VarInt(0) // add
		writeText(w, nb.title)
		w.Float32(nb.health)
		w.VarInt(nb.colour)
		w.VarInt(nb.style)
		w.Byte(0) // flags
		p.send(v777.ClientboundPlayBossEvent, w)
	case packet.BossEventHide:
		if b != nil {
			delete(p.ents.bosses, pk.BossEntityUniqueID)
			p.sendBossRemove(pk.BossEntityUniqueID)
		}
	case packet.BossEventHealthPercentage:
		if b != nil {
			b.health = clamp01(pk.HealthPercentage)
			w := p.pk()
			w.UUID(bossUUID(pk.BossEntityUniqueID))
			w.VarInt(2)
			w.Float32(b.health)
			p.send(v777.ClientboundPlayBossEvent, w)
		}
	case packet.BossEventTitle:
		if b != nil {
			b.title = translate(pk.BossBarTitle, nil)
			w := p.pk()
			w.UUID(bossUUID(pk.BossEntityUniqueID))
			w.VarInt(3)
			writeText(w, b.title)
			p.send(v777.ClientboundPlayBossEvent, w)
		}
	case packet.BossEventAppearanceProperties, packet.BossEventTexture:
		if b != nil {
			if int(pk.Colour) < len(bossColours) {
				b.colour = bossColours[pk.Colour]
			}
			b.style = int32(min(pk.Overlay, 4))
			w := p.pk()
			w.UUID(bossUUID(pk.BossEntityUniqueID))
			w.VarInt(4)
			w.VarInt(b.colour)
			w.VarInt(b.style)
			p.send(v777.ClientboundPlayBossEvent, w)
		}
	}
}

func (p *Player) sendBossRemove(id int64) {
	w := p.pk()
	w.UUID(bossUUID(id))
	w.VarInt(1)
	p.send(v777.ClientboundPlayBossEvent, w)
}

func clamp01(v float32) float32 { return max(0, min(1, v)) }
