package blocks

// Custom block looks (resource pack conversion, bridge/packs.go): a custom block drawn on a spare
// Java state instead of CustomStandIn.

// SetCustomJava draws every state of the custom block name as the Java 26.3 state java. solid:
// the block has a collision box (a full, opaque cube for the bridge's light and collision data);
// otherwise it is passable and lets light through. It must be called before the palette is used.
func (p *Palette) SetCustomJava(name string, java uint32, solid bool) int {
	n := 0
	for i, s := range p.States {
		if !s.Custom || s.Name != name {
			continue
		}
		p.java[i], p.wet[i] = java, java
		if solid {
			p.data[i] = 15 | 1<<9 | 1<<10
		} else {
			p.data[i] = 0
		}
		n++
	}
	return n
}

// JavaStates calls f with the Java state (dry and wet) of every non-custom state of the palette:
// the states the bridge's block mapping uses.
func (p *Palette) JavaStates(f func(java uint32)) {
	for i, s := range p.States {
		if s.Custom {
			continue
		}
		f(p.java[i])
		f(p.wet[i])
	}
}
