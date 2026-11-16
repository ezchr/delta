package particles

import (
	"strconv"
	"strings"
	"testing"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func TestTables(t *testing.T) {
	for id, want := range map[int32]string{
		packet.ParticleTypeHeart: "heart", packet.ParticleTypeVillagerHappy: "happy_villager",
		packet.ParticleTypeVillagerAngry: "angry_villager", packet.ParticleTypeTerrain: "block",
		packet.ParticleTypeNote: "note", packet.ParticleTypeIconCrack: "item", packet.ParticleTypeHugeExplosion: "explosion_emitter",
		packet.ParticleTypeLargeExplode: "explosion", packet.ParticleTypeSnowballPoof: "item_snowball",
		packet.ParticleTypeTotem: "totem_of_undying", packet.ParticleTypeMobSpell: "entity_effect",
	} {
		if got, ok := Legacy(id); !ok || got.Java != want {
			t.Errorf("particle type %d: %+v, want %s", id, got, want)
		}
	}
	if got, ok := Legacy(packet.ParticleTypeWolfArmourBreak); !ok || got.Java != "item" || got.Item != "wolf_armor" {
		t.Errorf("wolf armour break: %+v", got)
	}
	if got, ok := Legacy(packet.ParticleTypeBlockForceField); !ok || got.Java != "block_marker" || got.Block != "barrier" {
		t.Errorf("block force field: %+v", got)
	}
	if _, ok := Legacy(packet.ParticleTypeCarrotBoost); ok {
		t.Error("carrot boost has no Java particle")
	}
	for id, want := range map[string]string{
		"minecraft:heart_particle": "heart", "minecraft:villager_happy": "happy_villager", "minecraft:totem_particle": "totem_of_undying",
		"minecraft:basic_flame_particle": "flame", "minecraft:huge_explosion_emitter": "explosion_emitter",
		"minecraft:critical_hit_emitter": "crit", "minecraft:magic_critical_hit_emitter": "enchanted_hit",
		"minecraft:redstone_wire_dust_particle": "dust", "minecraft:falling_dust_sand_particle": "falling_dust",
		"minecraft:trial_spawner_detection": "trial_spawner_detection",
	} {
		if got, ok := Named(id); !ok || got.Java != want {
			t.Errorf("%s: %+v, want %s", id, got, want)
		}
	}
	if got, _ := Named("minecraft:redstone_wire_dust_particle"); got.Colour != 0xffff0000 {
		t.Errorf("redstone dust colour %#x", got.Colour)
	}
	if got, _ := Named("minecraft:falling_dust_sand_particle"); got.Block != "sand" {
		t.Errorf("sand dust block %q", got.Block)
	}
	if _, ok := Named("minecraft:vibration_signal"); ok {
		t.Error("vibration_signal is skipped")
	}
	if got, ok := LevelEvent(packet.LevelEventSculkCharge); !ok || got.Java != "sculk_charge" {
		t.Errorf("sculk charge level event: %+v", got)
	}
	if !Burst("minecraft:critical_hit_emitter") || Burst("minecraft:heart_particle") {
		t.Error("burst")
	}
}

// TestTablesValid: every Java particle, block and item the tables name exists in 26.3.
func TestTablesValid(t *testing.T) {
	check := func(what string, p Particle) {
		if _, ok := v777.Builtin["minecraft:particle_type"]["minecraft:"+p.Java]; !ok {
			t.Errorf("%s: no Java particle %s", what, p.Java)
		}
		if p.Item != "" {
			if _, ok := v777.Builtin["minecraft:item"]["minecraft:"+p.Item]; !ok {
				t.Errorf("%s: no Java item %s", what, p.Item)
			}
		}
	}
	for id, p := range legacy {
		check("particle type "+strconv.Itoa(int(id)), p)
	}
	for id, p := range named {
		if !strings.HasPrefix(id, "minecraft:") {
			t.Errorf("named %s without namespace", id)
		}
		check(id, p)
	}
	for _, p := range levelEvents {
		check("level event", p)
	}
	for _, s := range Skipped.Named {
		if _, ok := named[s]; ok {
			t.Errorf("%s both mapped and skipped", s)
		}
	}
}
