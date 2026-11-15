package bridge

import (
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Custom actors with no model in the converted pack (add-on floating texts, holograms, markers)
// have nothing Java can draw but their name tag: they are shown as a text display with the name,
// where Bedrock draws the tag (above the actor's box), facing the viewer like a name tag.

// textType is how a custom actor without a model is shown.
var textType = entityType{java: "minecraft:text_display", kind: entText}

// addTextActor shows a custom actor that has no Java entity and no model as its name tag. It
// reports whether it handled the actor.
func (p *Player) addTextActor(pk *packet.AddActor) bool {
	if !strings.Contains(pk.EntityType, ":") || strings.HasPrefix(pk.EntityType, "minecraft:") {
		return false
	}
	if jp := p.packFor(); jp != nil {
		if _, ok := jp.res.Entities[pk.EntityType]; ok {
			return false // entities_custom.go shows it with its model
		}
	}
	meta := pk.EntityMetadata
	if meta == nil {
		meta = protocol.EntityMetadata{}
	}
	e := &entity{rid: pk.EntityRuntimeID, uniq: pk.EntityUniqueID, bedrock: pk.EntityType, t: textType,
		java: textType.java, uuid: entityUUID(pk.EntityRuntimeID), meta: meta}
	e.flags, _ = meta[protocol.EntityDataKeyFlags].(int64)
	e.flags2, _ = meta[protocol.EntityDataKeyFlagsTwo].(int64)
	e.pos = vec64(pk.Position)
	e.vel = vec64(pk.Velocity)
	if !p.trackEntity(e) {
		return true
	}
	p.showEntity(e)
	return true
}

// writeTextData writes a text-shown actor's data: its name tag as the display's text.
func (p *Player) writeTextData(d dataWriter, e *entity) {
	m := e.meta
	h := float32(0)
	if v, ok := m[protocol.EntityDataKeyHeight].(float32); ok && v > 0 {
		h = v
	}
	name := shownName(e, m)
	if e.flag(protocol.EntityDataFlagInvisible) && !e.flag(protocol.EntityDataFlagAlwaysShowName) &&
		metaInt(m, protocol.EntityDataKeyAlwaysShowNameTag) == 0 {
		name = ""
	}
	d.head(11, serVector3)
	d.w.Float32(0)
	d.w.Float32(h + float32(textLift))
	d.w.Float32(0)
	d.byteVal(15, textBillboardAll)
	d.intVal(16, 15<<4|15<<20) // full bright, as name tags are drawn
	d.head(23, serComponent)
	writeText(d.w, name)
	d.byteVal(27, textDefaultBack|textSeeThrough)
}
