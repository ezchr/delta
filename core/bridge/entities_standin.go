package bridge

import (
	"math"
	"sync"

	"delta/core/internal/javapack"
	"delta/core/internal/molang"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Custom actors with a vanilla shape (internal/javapack/vanillashapes.go): an add-on's own entity
// type whose client entity file names a geometry of the vanilla pack has no model to convert,
// and is shown as the Java entity of that shape instead: a villager shape as a villager, a
// humanoid shape as a mannequin (Java 1.21.9+: a player-shaped entity that is no player, so no
// tab list entry and no fake profile) wearing the pack's texture as its skin.
//
// A mannequin's skin is entity data 17, a profile with a skin patch: the patch's body texture
// is a resource location ("delta:entity/x" = assets/delta/textures/entity/x.png), so a texture
// of the converted pack works without a skin server or signature. Which texture, when the
// entity's render controller picks one from an array (Array.skins[q.property('ns:trainer')]),
// is that index expression evaluated for the actor (its variant, mark variant and properties),
// again when they change.
//
// The stand-in is an ordinary entity to the rest of the bridge: a Java right click on it is the
// usual UseItemOnEntity interact (entities_events.go), which is what a script's
// playerInteractWithEntity and an NPC component see; what the server answers (a form, an NPC
// dialogue) is shown by forms.go and npc.go. The Java client opens no screen of its own for a
// villager: the trade screen only ever comes from the server.

// Mannequin entity data (26.2 and 26.3): Avatar 15 main hand, 16 skin parts; Mannequin 17
// profile, 18 immovable, 19 description (the "NPC" line under the name; none here).
const (
	mannequinSkinParts   = 16
	mannequinProfile     = 17
	mannequinDescription = 19
	serProfile           = 41
)

// standIn is a shown actor's stand-in state.
type standIn struct {
	def  *javapack.StandIn
	skin int // index into def.Skins sent (-1: none yet)
	expr *molang.Expr
}

var (
	javaKinds     map[string]entityType
	javaKindsOnce sync.Once
)

// javaEntityType is the entity type entry (data layout) of a Java entity type.
func javaEntityType(java string) (entityType, bool) {
	javaKindsOnce.Do(func() {
		javaKinds = map[string]entityType{"minecraft:mannequin": {java: "minecraft:mannequin", kind: entLiving}}
		for bedrock, t := range entityTypes {
			// The entry of the same name wins (minecraft:villager_v2 and minecraft:npc are both
			// villagers; either has the villager's layout).
			if _, have := javaKinds[t.java]; t.java != "" && (!have || bedrock == t.java) {
				javaKinds[t.java] = t
			}
		}
	})
	t, ok := javaKinds[java]
	return t, ok
}

// standInFor is the stand-in of a custom actor type, if the converted pack has one.
func (p *Player) standInFor(typ string) (entityType, *standIn, bool) {
	jp := p.packFor()
	if jp == nil {
		return entityType{}, nil, false
	}
	def, ok := jp.res.StandIns[typ]
	if !ok {
		return entityType{}, nil, false
	}
	t, ok := javaEntityType(def.Java)
	if !ok || p.ver().BuiltinID("minecraft:entity_type", t.java) < 0 {
		return entityType{}, nil, false
	}
	st := &standIn{def: &def, skin: -1}
	if def.SkinIndex != "" {
		if x, err := molang.Compile(def.SkinIndex, nil); err == nil && x.Known() {
			st.expr = x
		}
	}
	return t, st, true
}

// Query answers the queries a render controller indexes its arrays with.
func (e *entity) Query(q molang.Query, args []float64) float64 {
	switch q {
	case molang.QVariant:
		return float64(metaInt(e.meta, protocol.EntityDataKeyVariant))
	case molang.QMarkVariant:
		return float64(metaInt(e.meta, protocol.EntityDataKeyMarkVariant))
	case molang.QSkinID:
		return float64(metaInt(e.meta, protocol.EntityDataKeySkinID))
	case molang.QIsBaby:
		if e.flag(protocol.EntityDataFlagBaby) {
			return 1
		}
	case molang.QProperty, molang.QHasProperty:
		if len(args) == 0 {
			return 0
		}
		name, _ := molang.StringOf(args[0])
		v, has := e.props[name]
		if q == molang.QHasProperty {
			if has {
				return 1
			}
			return 0
		}
		switch v := v.(type) {
		case bool:
			if v {
				return 1
			}
		case int32:
			return float64(v)
		case float32:
			return float64(v)
		case string:
			return molang.Intern(v)
		}
	}
	return 0
}

// sendStandInData sends what a stand-in needs beyond the usual entity data: a mannequin's skin
// (again when the actor's data picks another one).
func (p *Player) sendStandInData(e *entity, first bool) {
	st := e.stand
	if st == nil || st.def.Java != "minecraft:mannequin" || !e.spawned {
		return
	}
	skin := 0
	if st.expr != nil {
		v := st.expr.Eval(molang.NewEnv(e, e.rid))
		skin = int(math.Floor(v))
	}
	if skin < 0 || skin >= len(st.def.Skins) {
		skin = 0 // Bedrock wraps or clamps an index out of range; the first is the safe choice
	}
	if !first && skin == st.skin {
		return
	}
	st.skin = skin
	tex := ""
	if skin < len(st.def.Skins) {
		tex = st.def.Skins[skin]
	}
	w := p.pk()
	w.VarInt(e.id)
	if first {
		w.Byte(mannequinSkinParts)
		w.VarInt(serByte)
		w.Byte(0x7f) // every outer layer
		w.Byte(mannequinDescription)
		w.VarInt(serOptComponent)
		w.Bool(false)
	}
	w.Byte(mannequinProfile)
	w.VarInt(serProfile)
	w.Bool(false) // a partial profile:
	w.Bool(false) // no name,
	w.Bool(false) // no id,
	w.VarInt(0)   // no properties
	w.Bool(tex != "")
	if tex != "" {
		w.String(tex) // the skin patch's body texture
	}
	w.Bool(false) // cape
	w.Bool(false) // elytra
	w.Bool(true)  // model
	w.Bool(st.def.Slim)
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
}

func init() {
	// After entities_meta.go's handler (file order): the actor's data and properties are in.
	OnBedrock(packet.IDSetActorData, func(p *Player, pk *packet.SetActorData) {
		if e, ok := p.ents.shown[pk.EntityRuntimeID]; ok && e.stand != nil {
			p.sendStandInData(e, false)
		}
	})
}
