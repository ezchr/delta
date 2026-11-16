package bridge

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Entities: the server's actors shown to the Java client (milestone 2). This file holds the entity
// id map and the reset that removes every entity when the player changes server or dimension; the
// translation is in entities_*.go:
//
//	entities_types.go   Bedrock identifier -> Java type table, variant tables, poses
//	entities_spawn.go   AddActor/AddItemActor/AddPainting/RemoveActor -> add_entity/remove_entities
//	entities_players.go AddPlayer, PlayerList (tab list, skins from the Geyser skin API)
//	entities_move.go    MoveActorAbsolute/Delta, MovePlayer (others), SetActorMotion -> moves
//	entities_meta.go    SetActorData/UpdateAttributes -> entity data, equipment, item stacks
//	entities_events.go  ActorEvent/Animate/TakeItemActor, own MobEffect, SetActorLink (riding),
//	                    Java attack/interact -> InventoryTransaction

// Entities maps the server's actors to Java entity ids.
type Entities struct {
	p *Player
	// byRuntime maps a Bedrock runtime id to its Java entity id; byUnique a unique id to the
	// runtime id.
	byRuntime map[uint64]int32
	byUnique  map[int64]uint64
	next      int32
	// state is free for other parts to keep per-entity data in.
	state map[int32]any

	// shown are the entities the Java client has, by runtime id and by Java id.
	shown  map[uint64]*entity
	byJava map[int32]*entity
	// tab is the server's player list as the Java client has it (kept over dimension changes).
	tab map[uuid.UUID]*tabEntry
	// ride is the vehicle the player rides on the server.
	ride rideState
	// rideSneak: the Java sneak key was down at the last player_input.
	rideSneak bool
	// effects are the player's own effects (shown again after a dimension change).
	effects       map[int32]selfEffect
	resendEffects bool
	// itemNames: the server's item network ids -> names (made from StartGame when needed).
	itemNames map[int32]string
	// frames are the item frame entities by block; framePunch a frame to hit with the next input.
	frames         map[cube.Pos]uint64
	framePunch     *cube.Pos
	framePunchFace int32
	// bosses are the boss bars shown, by the server's id.
	bosses map[int64]*bossBar
	// props are the server's actor property lists (entities_props.go).
	props actorProps
	// teams are the name tag teams the client has, tagDisplays the name tag text displays
	// (entities_nametags.go).
	teams       map[string]bool
	tagDisplays map[int32]bool
	// javaUUIDs maps the server's player UUIDs to the ones the client has (entities_players.go).
	javaUUIDs map[uuid.UUID]uuid.UUID
	// scratch is where entity data is put together.
	scratch wire.Writer
}

func newEntities(p *Player) *Entities {
	return &Entities{p: p, byRuntime: map[uint64]int32{}, byUnique: map[int64]uint64{}, next: SelfEntityID + 1,
		state: map[int32]any{}, shown: map[uint64]*entity{}, byJava: map[int32]*entity{},
		tab: map[uuid.UUID]*tabEntry{}, effects: map[int32]selfEffect{}}
}

// JavaID returns the Java entity id of a Bedrock runtime id, making one if it has none. The
// player's own runtime id is SelfEntityID.
func (e *Entities) JavaID(runtimeID uint64) int32 {
	if e.p.rem != nil && runtimeID == e.p.rem.gd.EntityRuntimeID {
		return SelfEntityID
	}
	if id, ok := e.byRuntime[runtimeID]; ok {
		return id
	}
	id := e.next
	e.next++
	e.byRuntime[runtimeID] = id
	return id
}

// reserve skips n ids (the ender dragon's parts: the Java client gives them the ids after the
// dragon's).
func (e *Entities) reserve(n int32) { e.next += n }

// Lookup returns the Java entity id of a runtime id the client was shown.
func (e *Entities) Lookup(runtimeID uint64) (int32, bool) {
	if e.p.rem != nil && runtimeID == e.p.rem.gd.EntityRuntimeID {
		return SelfEntityID, true
	}
	id, ok := e.byRuntime[runtimeID]
	return id, ok
}

// SetUnique remembers the unique id of a runtime id (AddActor and AddPlayer carry both).
func (e *Entities) SetUnique(unique int64, runtimeID uint64) { e.byUnique[unique] = runtimeID }

// RuntimeOf is the runtime id of a unique id.
func (e *Entities) RuntimeOf(unique int64) (uint64, bool) {
	r, ok := e.byUnique[unique]
	return r, ok
}

// Forget drops an entity from the map and returns its Java id (to remove it on the client).
func (e *Entities) Forget(runtimeID uint64) (int32, bool) {
	id, ok := e.byRuntime[runtimeID]
	if ok {
		delete(e.byRuntime, runtimeID)
		delete(e.state, id)
		delete(e.byJava, id)
	}
	if en, ok := e.shown[runtimeID]; ok {
		delete(e.shown, runtimeID)
		if r, ok := e.byUnique[en.uniq]; ok && r == runtimeID {
			delete(e.byUnique, en.uniq)
		}
	}
	return id, ok
}

// reset removes every entity from the Java client and forgets them (a new server or dimension).
func (e *Entities) reset() {
	if len(e.byRuntime) > 0 {
		w := e.p.pk()
		w.VarInt(int32(len(e.byRuntime)))
		for _, id := range e.byRuntime {
			w.VarInt(id)
		}
		e.p.send(v777.ClientboundPlayRemoveEntities, w)
	}
	clear(e.byRuntime)
	clear(e.byUnique)
	clear(e.state)
	clear(e.shown)
	clear(e.byJava)
	e.ride = rideState{}
	clear(e.frames)
	e.framePunch = nil
}

// handleMovePlayer is MovePlayer for another player.
func (e *Entities) handleMovePlayer(pk *packet.MovePlayer) {
	for _, f := range otherPlayerMoveHooks {
		f(e.p, pk)
	}
}

// otherPlayerMoveHooks get MovePlayer packets of other players (entities_move.go registers one).
var otherPlayerMoveHooks []func(p *Player, pk *packet.MovePlayer)
