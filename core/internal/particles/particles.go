// Package particles holds the Bedrock -> Java 26.3 particle tables: Geyser's Java -> Bedrock
// particle mappings reversed, plus hand-picked Java particles for the Bedrock ones Geyser does not
// map (particles_gen.go; tools/genparticles regenerates it). Names are Java registry keys without
// the minecraft: namespace.
package particles

import "strings"

// Particle is a Java particle type with the options the table fixes.
type Particle struct {
	// Java is the Java particle type.
	Java string
	// Block is the block (Java name without namespace) of a block particle whose block is always
	// the same; empty: the block comes from the packet or the world.
	Block string
	// Item is the item of an item particle whose item is always the same.
	Item string
	// Colour is the ARGB colour of a coloured particle when the packet gives none (0: none).
	Colour uint32
	// Colour2 is the colour a dust_color_transition particle fades to.
	Colour2 uint32
}

// Legacy is the Java particle of a Bedrock particle type (LevelEvent 0x4000|type, or the low 16
// bits of a ParticleGenericSpawn level event's data).
func Legacy(id int32) (Particle, bool) {
	p, ok := legacy[id]
	return p, ok
}

// Named is the Java particle of a Bedrock particle effect identifier (SpawnParticleEffect).
func Named(id string) (Particle, bool) {
	p, ok := named[id]
	return p, ok
}

// LevelEvent is the Java particle Geyser draws a Bedrock particle level event with (the level
// events the bridge draws no other way).
func LevelEvent(id int32) (Particle, bool) {
	p, ok := levelEvents[id]
	return p, ok
}

// Burst reports whether a Bedrock particle effect is an emitter: a burst of particles at once,
// where the Java particle is a single one.
func Burst(id string) bool {
	return strings.HasSuffix(id, "_emitter")
}
