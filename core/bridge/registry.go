package bridge

import (
	"fmt"

	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Handler registration. Each area of the translator (world, self, movement, text, entities,
// windows, commands) registers the packets it handles from an init function in its own file:
//
//	func init() {
//		OnBedrock(packet.IDAddActor, (*Player).handleAddActor)
//		OnJava(v777.ServerboundPlayInteract, (*Player).javaInteract)
//	}
//
// Bedrock handlers get the decoded gophertunnel packet (assert it to the concrete type) and run
// with p.mu held, only for packets of the player's current connection. Java handlers get a
// reader over the packet body (v777 layout) and run with p.mu held too. Several handlers may be
// registered for one packet; they run in registration order (file order within the package, then
// init order), and a handler that returns true stops the ones after it.

// BedrockHandler handles one packet from the Bedrock server. It returns true when no later handler
// should see the packet.
type BedrockHandler func(p *Player, pk packet.Packet) bool

// JavaHandler handles one packet from the Java client. It returns true when no later handler
// should see the packet, and an error for a malformed packet (the client is disconnected).
type JavaHandler func(p *Player, r *wire.Reader) (bool, error)

var (
	bedrockHandlers = map[uint32][]BedrockHandler{}
	javaHandlers    = map[int32][]JavaHandler{}
)

// OnBedrock registers h for Bedrock packets with this id (packet.IDxxx). Only call it from init.
func OnBedrock[T packet.Packet](id uint32, h func(p *Player, pk T)) {
	bedrockHandlers[id] = append(bedrockHandlers[id], func(p *Player, pk packet.Packet) bool {
		if t, ok := pk.(T); ok {
			h(p, t)
		}
		return false
	})
}

// OnBedrockStop is OnBedrock for a handler that decides whether later handlers see the packet.
func OnBedrockStop[T packet.Packet](id uint32, h func(p *Player, pk T) bool) {
	bedrockHandlers[id] = append(bedrockHandlers[id], func(p *Player, pk packet.Packet) bool {
		if t, ok := pk.(T); ok {
			return h(p, t)
		}
		return false
	})
}

// OnJava registers h for Java packets with this v777 id. Only call it from init.
func OnJava(id int32, h func(p *Player, r *wire.Reader) error) {
	javaHandlers[id] = append(javaHandlers[id], func(p *Player, r *wire.Reader) (bool, error) {
		return false, h(p, r)
	})
}

// OnJavaStop is OnJava for a handler that decides whether later handlers see the packet.
func OnJavaStop(id int32, h JavaHandler) { javaHandlers[id] = append(javaHandlers[id], h) }

// dispatchBedrock runs the handlers of pk. p.mu must be held.
func (p *Player) dispatchBedrock(pk packet.Packet) {
	hs, ok := bedrockHandlers[pk.ID()]
	if !ok {
		if debugPackets {
			p.log.Debug("unhandled bedrock packet", "type", fmt.Sprintf("%T", pk))
		}
		return
	}
	for _, h := range hs {
		if h(p, pk) {
			return
		}
	}
}

// dispatchJava runs the handlers of a Java packet. p.mu must be held.
func (p *Player) dispatchJava(id int32, body []byte) error {
	for _, h := range javaHandlers[id] {
		// Every handler reads the body from the start.
		stop, err := h(p, wire.NewReader(body))
		if err != nil || stop {
			return err
		}
	}
	return nil
}
