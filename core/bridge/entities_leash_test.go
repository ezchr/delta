package bridge

import (
	"testing"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Leads: a wandering trader's llamas as BDS sends them (LEASH_HOLDER in AddActor, the trader
// added after its llamas), the trader leaving view and coming back, a lead held by the player
// and one tied to a fence knot.

type entityLink struct{ src, dst int32 }

// links are the set_entity_link packets of out.
func links(t *testing.T, out []outPacket) []entityLink {
	t.Helper()
	var l []entityLink
	for _, o := range out {
		if o.id != v777.ClientboundPlaySetEntityLink {
			continue
		}
		r := wire.NewReader(o.w.B)
		l = append(l, entityLink{r.Int32(), r.Int32()})
		if r.Err != nil || r.Len() != 0 {
			t.Fatalf("set_entity_link: %v, %d bytes left", r.Err, r.Len())
		}
	}
	return l
}

func leashMeta(holder int64) protocol.EntityMetadata {
	return protocol.EntityMetadata{protocol.EntityDataKeyLeashHolder: holder}
}

func TestLeash(t *testing.T) {
	for _, v := range []*version.Version{version.Newest, version.ByProtocol(776)} {
		p, take := soundPlayer(t, v)
		self := p.rem.gd.EntityUniqueID // negative, as BDS gives players
		add := func(typ string, rid uint64, uniq int64, meta protocol.EntityMetadata) *entity {
			p.dispatchBedrock(&packet.AddActor{EntityType: typ, EntityRuntimeID: rid, EntityUniqueID: uniq, EntityMetadata: meta})
			e := p.ents.shown[rid]
			if e == nil || !e.spawned {
				t.Fatalf("%s not shown", typ)
			}
			return e
		}
		expect := func(what string, want ...entityLink) {
			t.Helper()
			got := links(t, take())
			if len(got) != len(want) {
				t.Fatalf("%d %s: links %v, want %v", v.Protocol, what, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%d %s: links %v, want %v", v.Protocol, what, got, want)
				}
			}
		}

		// The llamas come first, leashed to a trader the client does not have yet.
		l1 := add("minecraft:trader_llama", 10, 1010, leashMeta(2020))
		l2 := add("minecraft:trader_llama", 11, 1011, leashMeta(2020))
		cow := add("minecraft:cow", 12, 1012, leashMeta(-1))
		expect("llamas before their trader")
		trader := add("minecraft:wandering_trader", 20, 2020, protocol.EntityMetadata{})
		got := links(t, take())
		if len(got) != 2 || !(got[0] == entityLink{l1.id, trader.id} && got[1] == entityLink{l2.id, trader.id} ||
			got[1] == entityLink{l1.id, trader.id} && got[0] == entityLink{l2.id, trader.id}) {
			t.Fatalf("%d trader added: links %v, want both llamas to %d", v.Protocol, got, trader.id)
		}

		// A llama added after its trader gets its lead with it.
		l3 := add("minecraft:trader_llama", 13, 1013, leashMeta(2020))
		expect("llama after its trader", entityLink{l3.id, trader.id})

		// The trader goes out of view: the leads go; it comes back with another runtime id.
		p.dispatchBedrock(&packet.RemoveActor{EntityUniqueID: 2020})
		got = links(t, take())
		if len(got) != 3 {
			t.Fatalf("%d trader removed: links %v", v.Protocol, got)
		}
		for _, l := range got {
			if l.dst != 0 {
				t.Fatalf("%d trader removed: links %v, want holder 0", v.Protocol, got)
			}
		}
		trader = add("minecraft:wandering_trader", 21, 2020, protocol.EntityMetadata{})
		if got = links(t, take()); len(got) != 3 || got[0].dst != trader.id || got[1].dst != trader.id || got[2].dst != trader.id {
			t.Fatalf("%d trader back: links %v, want 3 to %d", v.Protocol, got, trader.id)
		}

		// The trader lets go of one llama; another one is given to the player (a lead held by a
		// player: a negative unique id on BDS).
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 10, EntityMetadata: leashMeta(-1)})
		expect("unleashed", entityLink{l1.id, 0})
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 11, EntityMetadata: leashMeta(self)})
		expect("held by the player", entityLink{l2.id, SelfEntityID})
		// Other data of a leashed mob sends no lead again; nor does the same holder again.
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 11, EntityMetadata: protocol.EntityMetadata{protocol.EntityDataKeyVariant: int32(1)}})
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 11, EntityMetadata: leashMeta(self)})
		expect("unchanged holder")
		// An unleashed mob that never had a lead gets no "none".
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 12, EntityMetadata: leashMeta(-1)})
		expect("cow, never leashed")

		// A fence knot holds the cow.
		knot := add("minecraft:leash_knot", 30, 3030, protocol.EntityMetadata{})
		take()
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 12, EntityMetadata: leashMeta(3030)})
		expect("tied to a knot", entityLink{cow.id, knot.id})
		// The knot breaks: BDS removes it and unleashes the cow.
		p.dispatchBedrock(&packet.RemoveActor{EntityUniqueID: 3030})
		p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 12, EntityMetadata: leashMeta(-1)})
		expect("knot gone", entityLink{cow.id, 0})
	}
}

// TestLeashBeforeSpawn: a lead given to a player entity that is not shown yet (its skin is still
// loading) goes out with its add_entity.
func TestLeashBeforeSpawn(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	p.dispatchBedrock(&packet.AddActor{EntityType: "minecraft:wandering_trader", EntityRuntimeID: 20, EntityUniqueID: 2020})
	e := &entity{rid: 40, uniq: 4040, bedrock: "minecraft:llama", t: entityTypes["minecraft:llama"], java: "minecraft:llama",
		uuid: entityUUID(40), meta: protocol.EntityMetadata{}}
	if !p.trackEntity(e) {
		t.Fatal("llama not tracked")
	}
	take()
	p.dispatchBedrock(&packet.SetActorData{EntityRuntimeID: 40, EntityMetadata: leashMeta(2020)})
	if l := links(t, take()); len(l) != 0 {
		t.Fatalf("lead sent before the entity: %v", l)
	}
	p.showEntity(e)
	trader := p.ents.shown[20]
	if l := links(t, take()); len(l) != 1 || l[0] != (entityLink{e.id, trader.id}) {
		t.Fatalf("links %v, want %d -> %d", l, e.id, trader.id)
	}
}
