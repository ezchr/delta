// Package sounds holds the data for translating Bedrock sounds and particles to Java 26.3: the
// Java block sound groups, mob categories, and Geyser's Java -> Bedrock sound and particle
// mappings reversed (sounds_gen.go, from tools/gensounds). Names are Java registry keys without
// the minecraft: namespace.
package sounds

// SoundType is a Java block sound group (net.minecraft.world.level.block.SoundType).
type SoundType struct {
	Break, Step, Place, Hit, Fall string
	Volume, Pitch                 float32
}

// Block is the sound group of a Java block (name without namespace) in a 26.3 state.
func Block(name string, state uint32) SoundType {
	if i, ok := stateSoundType[state]; ok {
		return soundTypes[i]
	}
	return soundTypes[blockSoundType[name]] // 0 (stone) when unknown, as Java's default
}

// Hostile reports whether a Java entity type is a monster (its sounds are in the HOSTILE source).
func Hostile(entity string) bool { return hostile[entity] }

// PlaySound is the Java sound event Geyser maps to a Bedrock sound name.
func PlaySound(bedrock string) (string, bool) {
	s, ok := playSounds[bedrock]
	return s, ok
}

// EntitySound is the Java sound event of a Bedrock level sound event of an entity type.
func EntitySound(entity, event string) (string, bool) {
	s, ok := entitySounds[entity][event]
	return s, ok
}

// LevelSound is the Java sound event of a Bedrock level sound event that names no entity or
// block (or of whichever entity Geyser maps it from).
func LevelSound(event string) (string, bool) {
	s, ok := levelSounds[event]
	return s, ok
}

// LegacyParticle is the Java particle type of a Bedrock particle type (LevelEvent 0x4000|id).
func LegacyParticle(id int32) (string, bool) {
	s, ok := legacyParticles[id]
	return s, ok
}

// NamedParticle is the Java particle type of a Bedrock particle effect identifier.
func NamedParticle(id string) (string, bool) {
	s, ok := namedParticles[id]
	return s, ok
}

// SoundVolume is a Java mob's getSoundVolume(): how loud its voice is (1 for most).
func SoundVolume(entity string, baby bool) float32 {
	switch entity {
	case "bat":
		return 0.1
	case "allay", "bee", "cow", "mooshroom", "squid", "glow_squid", "wolf", "slime", "magma_cube":
		return 0.4
	case "horse", "donkey", "mule", "skeleton_horse", "zombie_horse", "llama", "trader_llama", "camel", "camel_husk":
		return 0.8
	case "happy_ghast":
		if baby {
			return 1
		}
		return 4
	case "ghast", "ender_dragon":
		return 5
	case "warden":
		return 4
	}
	return 1
}

// AmbientInterval is a Java mob's getAmbientSoundInterval() in ticks (water animals 120).
func AmbientInterval(entity string) int {
	switch entity {
	case "cod", "salmon", "tropical_fish", "pufferfish", "squid", "glow_squid", "dolphin", "tadpole", "axolotl":
		return 120
	}
	return 80
}
