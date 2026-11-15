package blocks

// Registry is a Palette as Dragonfly's chunk.BlockRegistry, so the chunk package can decode,
// hold and light chunks in palette indices. Only the parts chunk decoding and lighting use are
// meaningful; nothing is ever ticked.
type Registry struct{ P *Palette }

func (r Registry) BlockCount() int      { return len(r.P.States) }
func (r Registry) AirRuntimeID() uint32 { return r.P.Air }

func (r Registry) RuntimeIDToState(rid uint32) (string, map[string]any, bool) {
	if int(rid) >= len(r.P.States) {
		return "", nil, false
	}
	s := r.P.States[rid]
	return s.Name, s.Properties, true
}

func (r Registry) StateToRuntimeID(name string, properties map[string]any) (uint32, bool) {
	i, ok := r.P.byHash[networkBlockHash(name, properties)]
	return i, ok
}

func (r Registry) DefaultProperties(name string) (map[string]any, bool) {
	for _, s := range r.P.States {
		if s.Name == name {
			return s.Properties, true
		}
	}
	return nil, false
}

func (r Registry) FilteringBlock(rid uint32) uint8      { return r.P.Filter(rid) }
func (r Registry) LightBlock(rid uint32) uint8          { return r.P.Emits(rid) }
func (r Registry) RandomTickBlock(uint32) bool          { return false }
func (r Registry) NBTBlock(rid uint32) bool             { return r.P.BlockEntity(rid) != "" }
func (r Registry) LiquidDisplacingBlock(uint32) bool    { return false }
func (r Registry) LiquidBlock(rid uint32) bool          { return r.P.Liquid(rid) }
func (r Registry) HashToRuntimeID(h uint32) (uint32, bool) {
	i, ok := r.P.byHash[h]
	return i, ok
}
func (r Registry) RuntimeIDToHash(rid uint32) (uint32, bool) {
	if int(rid) >= len(r.P.States) {
		return 0, false
	}
	return r.P.States[rid].Hash, true
}
