package bridge

import (
	"os"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// JB_DEBUG_SOUNDS=1 logs every sound and particle packet the server sends (and the actor events),
// with the block a block-sound's runtime id names, to see what a server sends for what.
var debugSounds = os.Getenv("JB_DEBUG_SOUNDS") == "1"

// blockNameOf is the Bedrock block name of a server block runtime id ("" if none).
func (p *Player) blockNameOf(rid int32) string {
	if p.rem == nil || p.rem.pal == nil || rid == -1 {
		return ""
	}
	return p.rem.pal.Name(p.rem.pal.ServerToIndex(uint32(rid)))
}

func init() {
	if !debugSounds {
		return
	}
	OnBedrock(packet.IDLevelSoundEvent, func(p *Player, pk *packet.LevelSoundEvent) {
		p.log.Info("BDS level sound", "type", pk.SoundType, "pos", pk.Position, "extra", pk.ExtraData, "block", p.blockNameOf(pk.ExtraData),
			"entity", pk.EntityType, "baby", pk.BabyMob, "global", pk.DisableRelativeVolume, "uniq", pk.EntityUniqueID,
			"self", pk.EntityUniqueID == p.rem.gd.EntityUniqueID, "fire", pk.FireAtPosition)
	})
	OnBedrock(packet.IDPlaySound, func(p *Player, pk *packet.PlaySound) {
		p.log.Info("BDS play sound", "name", pk.SoundName, "pos", pk.Position, "vol", pk.Volume, "pitch", pk.Pitch)
	})
	OnBedrock(packet.IDStopSound, func(p *Player, pk *packet.StopSound) {
		p.log.Info("BDS stop sound", "name", pk.SoundName, "all", pk.StopAll)
	})
	OnBedrock(packet.IDLevelEvent, func(p *Player, pk *packet.LevelEvent) {
		if pk.EventType >= packet.LevelEventStartBlockCracking && pk.EventType <= packet.LevelEventUpdateBlockCracking {
			return
		}
		p.log.Info("BDS level event", "type", pk.EventType, "pos", pk.Position, "data", pk.EventData, "block", p.blockNameOf(pk.EventData))
	})
	OnBedrock(packet.IDLevelEventGeneric, func(p *Player, pk *packet.LevelEventGeneric) {
		p.log.Info("BDS level event generic", "id", pk.EventID, "len", len(pk.SerialisedEventData))
	})
	OnBedrock(packet.IDSpawnParticleEffect, func(p *Player, pk *packet.SpawnParticleEffect) {
		p.log.Info("BDS particle", "name", pk.ParticleName, "uniq", pk.EntityUniqueID, "pos", pk.Position)
	})
	OnBedrock(packet.IDActorEvent, func(p *Player, pk *packet.ActorEvent) {
		p.log.Info("BDS actor event", "type", pk.EventType, "rid", pk.EntityRuntimeID, "data", pk.EventData, "self", pk.EntityRuntimeID == p.rem.gd.EntityRuntimeID)
	})
	OnBedrock(packet.IDTakeItemActor, func(p *Player, pk *packet.TakeItemActor) {
		p.log.Info("BDS take item", "item", pk.ItemEntityRuntimeID, "taker", pk.TakerEntityRuntimeID)
	})
}
