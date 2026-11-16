package bridge

import (
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Leads. Bedrock tells what holds a mob's lead with the LEASH_HOLDER actor data: the holder's
// unique id, -1 for none (BDS sets the LEASHED flag with it). It comes in AddActor and, when the
// holder changes, in SetActorData. A Java client gets set_entity_link (the mob's id, the holder's
// id, 0 for none) after the mob's add_entity and finds the holder by that id when it draws the
// lead, so the holder must be an entity the client has. The holder (a wandering trader, a player,
// a fence knot) can come after the mob or go out of view while the mob stays: the lead is sent
// again when the holder is shown, and taken back when it is removed.

// leashHolder is the unique id of what holds a mob's lead by its actor data (0: nothing). Player
// unique ids are negative on BDS; -1 is "none".
func leashHolder(m protocol.EntityMetadata) int64 {
	v, ok := m[protocol.EntityDataKeyLeashHolder].(int64)
	if !ok || v == -1 {
		return 0
	}
	return v
}

// updateLeash takes the holder from e's actor data (merged) and tells the client when it changed.
func (p *Player) updateLeash(e *entity) {
	v := leashHolder(e.meta)
	if v == e.leash {
		return
	}
	e.leash = v
	if e.spawned {
		p.sendLeash(e)
	}
}

// sendLeash sends a mob's lead as the client should have it now: the holder's Java id, or 0 when
// it has none or the client does not have the holder. A mob the client has no lead on gets no
// "none".
func (p *Player) sendLeash(e *entity) {
	holder := int32(0)
	if e.leash != 0 {
		if id, ok := p.uniqueJavaID(e.leash); ok {
			holder = id
		}
	}
	if holder == 0 && !e.leashSent {
		return
	}
	e.leashSent = holder != 0
	w := p.pk()
	w.Int32(e.id)
	w.Int32(holder)
	p.send(v777.ClientboundPlaySetEntityLink, w)
}

// relinkLeashed sends the leads of the mobs held by h again: h was just shown (a new Java id or a
// new entity on the client) or removed (the lead goes until it comes back).
func (p *Player) relinkLeashed(h *entity) {
	if h.uniq == 0 {
		return
	}
	for _, e := range p.ents.shown {
		if e.leash == h.uniq && e != h && e.spawned {
			p.sendLeash(e)
		}
	}
}
