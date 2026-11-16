package bridge

import (
	"testing"

	"delta/core/internal/javapack"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// mannequinSkin is the skin texture and model of the last profile sent for entity id in out
// ("-" when none was sent).
func mannequinSkin(t *testing.T, out []outPacket, id int32) (skin string, slim bool) {
	t.Helper()
	skin = "-"
	for _, o := range out {
		if o.id != v777.ClientboundPlaySetEntityData {
			continue
		}
		r := wire.NewReader(o.w.B)
		if r.VarInt() != id {
			continue
		}
	data:
		for r.Err == nil {
			idx := r.Byte()
			if idx == 0xff {
				break
			}
			ser := r.VarInt()
			switch {
			case idx == mannequinSkinParts && ser == serByte:
				if r.Byte() != 0x7f {
					t.Fatal("skin parts")
				}
			case idx == mannequinDescription && ser == serOptComponent:
				if r.Bool() {
					t.Fatal("a description")
				}
			case idx == mannequinProfile && ser == serProfile:
				if r.Bool() || r.Bool() || r.Bool() || r.VarInt() != 0 {
					t.Fatal("not an empty partial profile")
				}
				skin = ""
				if r.Bool() {
					skin = r.String(32767)
				}
				if r.Bool() || r.Bool() || !r.Bool() {
					t.Fatal("cape, elytra or no model")
				}
				slim = r.Bool()
			default:
				break data // the entity's ordinary data: not looked at here
			}
		}
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	return skin, slim
}

func TestStandIns(t *testing.T) {
	for _, v := range []*version.Version{version.Newest, version.ByProtocol(776)} {
		p, take := soundPlayer(t, v)
		p.rem.jpack = &javaPack{packBuild: &packBuild{sha: "x", res: &javapack.Result{StandIns: map[string]javapack.StandIn{
			"t:nurse":   {Java: "minecraft:villager"},
			"t:trainer": {Java: "minecraft:mannequin", Skins: []string{"delta:entity/a", "delta:entity/b", ""}, SkinIndex: "q.property('t:trainer')"},
			"t:ghost":   {Java: "minecraft:no_such_entity"},
		}}}}
		p.ents.props = actorProps{rem: p.rem, types: map[string][]propDef{"t:trainer": {{name: "t:trainer", kind: propInt}}}}
		added := func(out []outPacket) (n int) {
			for _, o := range out {
				if o.id == v777.ClientboundPlayAddEntity {
					n++
				}
			}
			return n
		}
		// A villager shape: a villager, and nothing a mannequin needs.
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:nurse", EntityRuntimeID: 10, EntityUniqueID: -10, Position: mgl32.Vec3{2, 0, 2}})
		e := p.ents.shown[10]
		out := take()
		if e == nil || e.java != "minecraft:villager" || !e.living() || added(out) != 1 {
			t.Fatalf("%d: nurse shown as %+v", v.Protocol, e)
		}
		if skin, _ := mannequinSkin(t, out, e.id); skin != "-" {
			t.Fatalf("%d: a villager was sent a profile", v.Protocol)
		}
		if p.ents.byJava[e.id] != e {
			t.Fatalf("%d: the stand-in cannot be clicked", v.Protocol)
		}
		// A humanoid: a mannequin wearing the skin its property picks.
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:trainer", EntityRuntimeID: 11, EntityUniqueID: -11, Position: mgl32.Vec3{2, 0, 3},
			EntityProperties: protocol.EntityProperties{IntegerProperties: []protocol.IntegerEntityProperty{{Index: 0, Value: 1}}}})
		m := p.ents.shown[11]
		out = take()
		if m == nil || m.java != "minecraft:mannequin" || added(out) != 1 {
			t.Fatalf("%d: trainer shown as %+v", v.Protocol, m)
		}
		if skin, slim := mannequinSkin(t, out, m.id); skin != "delta:entity/b" || slim {
			t.Fatalf("%d: skin %q slim %v", v.Protocol, skin, slim)
		}
		// The property changes: the other skin; the same again: nothing; a skin the pack does
		// not have: the default; out of range: the first.
		for _, c := range []struct {
			value int32
			want  string
		}{{0, "delta:entity/a"}, {0, "-"}, {2, ""}, {7, "delta:entity/a"}} {
			p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 11, EntityMetadata: protocol.EntityMetadata{},
				EntityProperties: protocol.EntityProperties{IntegerProperties: []protocol.IntegerEntityProperty{{Index: 0, Value: c.value}}}})
			if skin, _ := mannequinSkin(t, take(), m.id); skin != c.want {
				t.Fatalf("%d: property %d: skin %q, want %q", v.Protocol, c.value, skin, c.want)
			}
		}
		// A stand-in the client has no entity for is handled as any unknown actor type.
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:ghost", EntityRuntimeID: 12, EntityUniqueID: -12})
		if g := p.ents.shown[12]; g != nil && g.stand != nil {
			t.Fatalf("%d: ghost shown as %s", v.Protocol, g.java)
		}
	}
}
