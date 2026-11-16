package bridge

import (
	"bytes"
	"fmt"
	"math"
	"slices"

	"delta/core/internal/blocks"
	"delta/core/internal/javamap"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world/chunk"
	jchunk "github.com/ezchr/go-mcjava/chunk"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// ChunkPos is a chunk column position (x, z in chunks).
type ChunkPos [2]int32

// World is the part of the server's dimension the player has: the chunk columns it sent, held in
// palette indices (Dragonfly's chunk package with the server's palette as registry), shown to the
// Java client once complete. A column goes to Java only once all its sub chunks are in: the client
// never gets a chunk with holes to fall through. One World per Bedrock dimension the player is in.
type World struct {
	pal  *blocks.Palette
	reg  blocks.Registry
	bdim int32      // Bedrock dimension id
	r    cube.Range // Bedrock block range of the dimension

	cols    map[ChunkPos]*column
	pending map[ChunkPos]*pendingColumn // sub chunk request mode, waiting for sub chunks

	// publisher is the area the server keeps chunks of: centre (block) and radius (blocks).
	pubCentre protocol.BlockPos
	pubRadius int32
}

// column is one complete column.
type column struct {
	c      *chunk.Chunk
	be     map[cube.Pos]map[string]any // block entity NBT the server sent
	queued bool                        // waiting in the send queue
	sent   bool                        // the Java client has it
	batch  uint64                      // the chunk batch it went in (Player.batchSeq)
}

// bedrockRange is the block range of a Bedrock dimension.
func bedrockRange(dim int32) cube.Range {
	switch dim {
	case 1:
		return cube.Range{0, 127}
	case 2:
		return cube.Range{0, 255}
	}
	return cube.Range{-64, 319}
}

// javaDimension is the Java dimension shown for a Bedrock one.
func javaDimension(dim int32) string {
	switch dim {
	case 1:
		return DimNether
	case 2:
		return DimEnd
	}
	return DimOverworld
}

func newWorld(pal *blocks.Palette, dim int32) *World {
	return &World{pal: pal, reg: blocks.Registry{P: pal}, bdim: dim, r: bedrockRange(dim),
		cols: map[ChunkPos]*column{}, pending: map[ChunkPos]*pendingColumn{}}
}

// leaveWorld forgets the current world on the Java client: its chunks are unloaded (needed when
// the client stays in the same Java dimension, harmless otherwise). p.mu is held.
func (p *Player) leaveWorld() {
	if p.inWaiting {
		for x := int32(-waitingRadius); x <= waitingRadius; x++ {
			for z := int32(-waitingRadius); z <= waitingRadius; z++ {
				p.sendForgetChunk(x, z)
			}
		}
		p.inWaiting = false
	}
	if p.world == nil {
		return
	}
	for pos, col := range p.world.cols {
		if col.sent {
			p.sendForgetChunk(pos[0], pos[1])
		}
	}
	p.world = nil
}

func init() {
	OnBedrock(packet.IDLevelChunk, (*Player).handleLevelChunk)
	OnBedrock(packet.IDSubChunk, (*Player).handleSubChunk)
	OnBedrock(packet.IDUpdateBlock, func(p *Player, pk *packet.UpdateBlock) {
		p.updateBlocks([]blockChange{{pk.Position, pk.NewBlockRuntimeID, uint8(pk.Layer)}})
	})
	OnBedrock(packet.IDUpdateBlockSynced, func(p *Player, pk *packet.UpdateBlockSynced) {
		p.updateBlocks([]blockChange{{pk.Position, pk.NewBlockRuntimeID, uint8(pk.Layer)}})
	})
	OnBedrock(packet.IDUpdateSubChunkBlocks, func(p *Player, pk *packet.UpdateSubChunkBlocks) {
		changes := make([]blockChange, 0, len(pk.Blocks)+len(pk.Extra))
		for _, e := range pk.Blocks {
			changes = append(changes, blockChange{e.BlockPos, e.BlockRuntimeID, 0})
		}
		for _, e := range pk.Extra {
			changes = append(changes, blockChange{e.BlockPos, e.BlockRuntimeID, 1})
		}
		p.updateBlocks(changes)
	})
	OnBedrock(packet.IDBlockActorData, func(p *Player, pk *packet.BlockActorData) {
		w := p.world
		pos := cube.Pos{int(pk.Position[0]), int(pk.Position[1]), int(pk.Position[2])}
		if col := w.cols[chunkOf(pos)]; col != nil {
			col.be[pos] = pk.NBTData
		}
		for _, f := range blockEntityHooks {
			f(p, pos, pk.NBTData)
		}
	})
	OnBedrock(packet.IDNetworkChunkPublisherUpdate, (*Player).publisherUpdate)
	OnJava(v777.ServerboundPlayChunkBatchReceived, func(p *Player, r *wireReader) error {
		rate := float64(r.Float32())
		if rate != rate || rate < 0.01 {
			rate = 0.01
		}
		p.chunkRate = min(rate, 64)
		p.batchInFlight = false
		p.batchAcked = p.batchSeq
		return r.Err
	})
}

// blockEntityHooks see block entity data the server sent (BlockActorData), after it was stored
// in the column: the place for sign text, banners, skulls... (later milestones).
var blockEntityHooks []func(p *Player, pos cube.Pos, data map[string]any)

func chunkOf(pos cube.Pos) ChunkPos { return ChunkPos{int32(pos[0] >> 4), int32(pos[2] >> 4)} }

// handleLevelChunk decodes a chunk, or in sub chunk request mode its biomes, and requests its
// sub chunks.
func (p *Player) handleLevelChunk(pk *packet.LevelChunk) {
	w, r := p.world, p.rem
	limit, requestMode := pk.SubChunkLimit.Value()
	r.stats.levelChunks.Add(1)
	if pk.Dimension != w.bdim {
		return
	}
	pos := ChunkPos{pk.Position[0], pk.Position[1]}
	count := int(pk.SubChunkCount)
	if requestMode {
		r.stats.requestMode.Add(1)
		count = 0
	}
	c, be, biomeErr, err := w.decodeChunk(pk.RawPayload, count)
	if biomeErr != nil && r.stats.biomeErrs.Add(1) <= 3 {
		r.log.Warn("chunk biomes not readable; blocks kept", "pos", pos, "err", biomeErr)
	}
	if err != nil {
		if r.stats.decodeErrs.Add(1) <= 5 {
			r.log.Warn("chunk not readable", "pos", pos, "count", pk.SubChunkCount, "len", len(pk.RawPayload), "err", err)
		}
		return
	}
	if !requestMode {
		p.putColumn(pos, c, be)
		return
	}
	n := len(c.Sub())
	if limit >= 0 && int(limit) < n {
		n = int(limit)
	}
	base := int8(w.r[0] >> 4)
	pc := &pendingColumn{c: c, be: map[cube.Pos]map[string]any{}, waiting: map[int8]bool{}}
	ys := make([]int8, 0, n)
	for i := 0; i < n; i++ {
		y := base + int8(i)
		pc.waiting[y] = true
		ys = append(ys, y)
	}
	w.pending[pos] = pc
	if n == 0 {
		p.finishPending(pos)
		return
	}
	p.requestSubChunks(pos, ys)
}

// decodeChunk decodes a LevelChunk payload of count sub chunks into palette indices.
func (w *World) decodeChunk(data []byte, count int) (*chunk.Chunk, map[cube.Pos]map[string]any, error, error) {
	buf := bytes.NewBuffer(data)
	c, biomeErr, err := chunk.NetworkDecodeLenient(w.reg, buf, count, w.r)
	if err != nil {
		return nil, nil, nil, err
	}
	w.toIndices(c.Sub())
	if n, err := buf.ReadByte(); err == nil { // border blocks
		buf.Next(int(n))
	}
	if biomeErr != nil {
		return c, map[cube.Pos]map[string]any{}, biomeErr, nil
	}
	return c, readBlockEntities(buf), nil, nil
}

// toIndices renumbers decoded sub chunks from server runtime ids to palette indices.
func (w *World) toIndices(subs []*chunk.SubChunk) {
	if !w.pal.Hashed {
		// Indices are the runtime ids; only out-of-range ids (none from a sane server) need fixing.
		n := uint32(len(w.pal.States))
		for _, sub := range subs {
			for _, l := range sub.Layers() {
				l.Palette().Replace(func(v uint32) uint32 {
					if v >= n {
						return w.pal.Air
					}
					return v
				})
			}
		}
		return
	}
	for _, sub := range subs {
		for _, l := range sub.Layers() {
			l.Palette().Replace(w.pal.ServerToIndex)
		}
	}
}

// readBlockEntities reads block entity NBT compounds until the end of buf.
func readBlockEntities(buf *bytes.Buffer) map[cube.Pos]map[string]any {
	out := map[cube.Pos]map[string]any{}
	dec := nbt.NewDecoderWithEncoding(buf, nbt.NetworkLittleEndian)
	for buf.Len() > 0 {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		out[cube.Pos{nbtInt(m["x"]), nbtInt(m["y"]), nbtInt(m["z"])}] = m
	}
	return out
}

func nbtInt(v any) int {
	switch v := v.(type) {
	case int32:
		return int(v)
	case int64:
		return int(v)
	case int16:
		return int(v)
	case uint8:
		return int(v)
	case int8:
		return int(v)
	}
	return 0
}

// putColumn stores a complete column and queues it for the Java client.
func (p *Player) putColumn(pos ChunkPos, c *chunk.Chunk, be map[cube.Pos]map[string]any) {
	w := p.world
	p.rem.stats.columns.Add(1)
	c.InvalidateHeightMap()
	col := w.cols[pos]
	if col == nil {
		col = &column{}
		w.cols[pos] = col
	}
	col.c, col.be = c, be
	col.queued = true // a column the client has is sent again whole
	p.columnArrived(pos)
}

// sendChunkBatch sends the queued columns nearest the player, at most the rate the client asked
// for, with one batch waiting for its acknowledgement at a time. p.mu is held.
func (p *Player) sendChunkBatch() {
	w := p.world
	if w == nil || p.batchInFlight || p.inWaiting {
		return
	}
	var queue []ChunkPos
	for pos, col := range w.cols {
		if col.queued {
			queue = append(queue, pos)
		}
	}
	if len(queue) == 0 {
		return
	}
	cx, cz := p.centre[0], p.centre[1]
	dist := func(c ChunkPos) int32 { return (c[0]-cx)*(c[0]-cx) + (c[1]-cz)*(c[1]-cz) }
	slices.SortFunc(queue, func(a, b ChunkPos) int { return int(dist(a) - dist(b)) })
	n := max(1, int(p.chunkRate))
	if len(queue) > n {
		queue = queue[:n]
	}
	p.send(v777.ClientboundPlayChunkBatchStart, p.pk())
	p.batchSeq++
	sent := 0
	for _, pos := range queue {
		col := w.cols[pos]
		col.queued = false
		if err := p.sendColumn(pos, col); err != nil {
			p.log.Error("encode chunk", "pos", pos, "err", err)
			continue
		}
		col.sent, col.batch = true, p.batchSeq
		sent++
	}
	w2 := p.pk()
	w2.VarInt(int32(sent))
	p.send(v777.ClientboundPlayChunkBatchFinished, w2)
	p.batchInFlight = true
}

// javaEncoder is the chunk encoder for the player's client version.
func (p *Player) javaEncoder() *jchunk.Encoder {
	if p.enc == nil {
		v := p.ver()
		if v.Native() {
			p.enc = jchunk.NewEncoder(jchunk.VanillaBlockStates, jchunk.VanillaBiomes)
		} else {
			p.enc = jchunk.NewEncoder(v.BlockStates, v.Biomes)
			p.enc.LongMasks = v.Protocol < 777 // 26.3 switched light masks to bytes
		}
	}
	return p.enc
}

// biomeID is the Java biome id (for the client's version) of a Bedrock biome id.
func (p *Player) biomeID(b uint32) uint32 {
	id := uint32(javamap.BiomeID(int(b)))
	v := p.ver()
	if v.Native() {
		return id
	}
	if s := v.Synced("minecraft:worldgen/biome", int32(id)); s >= 0 {
		return uint32(s)
	}
	return uint32(v.RegistryID("minecraft:worldgen/biome", "minecraft:plains"))
}

// sendColumn lights a column and sends it as level_chunk_with_light.
func (p *Player) sendColumn(pos ChunkPos, col *column) error {
	w := p.world
	c := col.c
	chunk.LightArea([]*chunk.Chunk{c}, int(pos[0]), int(pos[1])).Fill()
	java, wet := w.pal.JavaFor(p.ver())
	alt := w.pal.VineAltFor(p.ver())
	n := dimSections(p.javaDim)
	jc := &p.col
	jc.X, jc.Z = pos[0], pos[1]
	if len(jc.Sections) != n {
		jc.Sections = make([]jchunk.Section, n)
		jc.SkyLight = make([]jchunk.Light, n+2)
		jc.BlockLight = make([]jchunk.Light, n+2)
		for i := range jc.SkyLight {
			jc.SkyLight[i].Data = make([]byte, 2048)
			jc.BlockLight[i].Data = make([]byte, 2048)
		}
	}
	jc.BlockEntities = jc.BlockEntities[:0]
	subs := c.Sub()
	r := c.Range()
	// The Java dimension may start lower or higher than the Bedrock one (it does not for the three
	// vanilla dimensions, but a server may send fewer sections).
	off := (r[0] - dimMinY(p.javaDim)) >> 4
	var heights [256]uint16
	lastFilled := -1
	air := java[w.pal.Air]
	for i := range jc.Sections {
		jc.Sections[i].BlockLayout, jc.Sections[i].BiomeLayout = nil, nil
	}
	plains := p.biomeID(1)
	for j := 0; j < n; j++ {
		sec := &jc.Sections[j]
		i := j - off
		if i < 0 || i >= len(subs) {
			sec.Fill(air, plains)
			sec.BlockCount, sec.FluidCount = 0, 0
			jc.SkyLight[j+1].State, jc.BlockLight[j+1].State = jchunk.LightAbsent, jchunk.LightAbsent
			continue
		}
		sub := subs[i]
		baseY := r[0] + i*16
		var nonAir, fluid int16
		if sub.Empty() {
			for k := range sec.Blocks {
				sec.Blocks[k] = air
			}
		} else {
			layer := sub.Layer(0)
			var water *chunk.PalettedStorage
			if len(sub.Layers()) > 1 {
				water = sub.Layer(1)
			}
			for y := byte(0); y < 16; y++ {
				for z := byte(0); z < 16; z++ {
					for x := byte(0); x < 16; x++ {
						idx := layer.At(x, y, z)
						isWet := water != nil && w.pal.IsWater(water.At(x, y, z))
						if w.pal.IsDoor(idx) {
							idx = w.doorIndexAt(col, idx, cube.Pos{int(pos[0])<<4 | int(x), baseY + int(y), int(pos[1])<<4 | int(z)})
						}
						state := java[idx]
						if isWet {
							state = wet[idx]
						}
						if w.pal.IsVine(idx) {
							state = w.vineState(col, idx, cube.Pos{int(pos[0])<<4 | int(x), baseY + int(y), int(pos[1])<<4 | int(z)}, state, alt)
						}
						sec.Blocks[jchunk.BlockIndex(int(x), int(y), int(z))] = state
						if !w.pal.IsAir(idx) {
							nonAir++
							h := uint16(baseY + int(y) - dimMinY(p.javaDim) + 1)
							if h > heights[int(z)<<4|int(x)] {
								heights[int(z)<<4|int(x)] = h
							}
							if be := w.pal.BlockEntity(idx); be != "" || w.pal.BlockEntityState(idx) {
								bp := cube.Pos{int(pos[0])<<4 | int(x), baseY + int(y), int(pos[1])<<4 | int(z)}
								d := col.be[bp]
								if d != nil {
									sec.Blocks[jchunk.BlockIndex(int(x), int(y), int(z))] = p.beStateFor(state, idx, isWet, bp, d)
								}
								if typ := p.ver().BuiltinID("minecraft:block_entity_type", be); be != "" && typ >= 0 {
									jc.BlockEntities = append(jc.BlockEntities, jchunk.BlockEntity{
										XZ: uint8(x)<<4 | uint8(z), Y: int16(baseY + int(y)), Type: typ, NBT: p.beNBT(be, d),
									})
								}
							}
						}
						if w.pal.Liquid(idx) || isWet {
							fluid++
						}
					}
				}
			}
		}
		sec.BlockCount, sec.FluidCount = nonAir, fluid
		for y := 0; y < 4; y++ {
			for z := 0; z < 4; z++ {
				for x := 0; x < 4; x++ {
					b := c.Biome(uint8(x*4+2), int16(baseY+y*4+2), uint8(z*4+2))
					sec.Biomes[jchunk.BiomeIndex(x, y, z)] = p.biomeID(b)
				}
			}
		}
		if nonAir > 0 {
			lastFilled = j
		}
		if !fillLight(&jc.BlockLight[j+1], sub.BlockLight) {
			jc.BlockLight[j+1].State = jchunk.LightAbsent
		}
	}
	// Sky light like vanilla: up to one section above the highest section with blocks.
	for j := 0; j < n; j++ {
		i := j - off
		if i >= 0 && i < len(subs) && j <= lastFilled+1 {
			fillLight(&jc.SkyLight[j+1], subs[i].SkyLight)
		} else {
			jc.SkyLight[j+1].State = jchunk.LightAbsent
		}
	}
	jc.SkyLight[0].State, jc.BlockLight[0].State = jchunk.LightAbsent, jchunk.LightAbsent
	jc.SkyLight[n+1].State, jc.BlockLight[n+1].State = jchunk.LightAbsent, jchunk.LightAbsent
	data := jchunk.PackHeightmap(p.hm[:0], &heights, n*16)
	p.hm = data
	jc.Heightmaps = append(jc.Heightmaps[:0],
		jchunk.Heightmap{Type: jchunk.WorldSurface, Data: data},
		jchunk.Heightmap{Type: jchunk.MotionBlocking, Data: data},
		jchunk.Heightmap{Type: jchunk.MotionBlockingNoLeaves, Data: data},
	)
	out := p.pk()
	if err := p.javaEncoder().Encode(out, jc); err != nil {
		return err
	}
	p.send(v777.ClientboundPlayLevelChunkWithLight, out)
	for _, f := range chunkSentHooks {
		f(p, pos, col)
	}
	return nil
}

// chunkSentHooks run after a column was sent to the Java client (block entity data, item frames).
var chunkSentHooks []func(p *Player, pos ChunkPos, col *column)

// fillLight copies a sub chunk's light into a Java nibble array and reports whether any of it is
// non-zero.
func fillLight(l *jchunk.Light, get func(x, y, z byte) uint8) bool {
	l.State = jchunk.LightData
	d := l.Data
	var any byte
	for y := byte(0); y < 16; y++ {
		for z := byte(0); z < 16; z++ {
			for x := byte(0); x < 16; x += 2 {
				i := jchunk.BlockIndex(int(x), int(y), int(z))
				v := get(x, y, z)&0xf | get(x+1, y, z)<<4
				d[i>>1] = v
				any |= v
			}
		}
	}
	return any != 0
}

// blockChange is one block a server packet changes (rid in the server's numbering).
type blockChange struct {
	pos   protocol.BlockPos
	rid   uint32
	layer uint8
}

// updateBlocks applies block changes to the stored columns and shows those of sent columns.
func (p *Player) updateBlocks(changes []blockChange) {
	w := p.world
	type secKey [3]int32
	sections := map[secKey][]uint64{}
	var single []blockChange
	extra := 0 // door halves and vines redrawn with a change
	for _, ch := range changes {
		pos := cube.Pos{int(ch.pos[0]), int(ch.pos[1]), int(ch.pos[2])}
		if pos.OutOfBounds(w.r) || ch.layer > 1 {
			continue
		}
		col := w.cols[chunkOf(pos)]
		if col == nil {
			continue
		}
		idx := w.pal.ServerToIndex(ch.rid)
		x, y, z := uint8(pos[0]&15), int16(pos[1]), uint8(pos[2]&15)
		col.c.SetBlock(x, y, z, ch.layer, idx)
		if ch.layer == 0 && w.pal.BlockEntity(idx) == "" && !w.pal.BlockEntityState(idx) {
			delete(col.be, pos)
		}
		if !col.sent {
			continue
		}
		state := p.javaStateAt(col, pos)
		for _, dy := range []int{-1, 1} { // a door's other half shows part of this one's state, a vine its tip or body
			o := pos.Add(cube.Pos{0, dy, 0})
			if o.OutOfBounds(w.r) {
				continue
			}
			if nb := col.c.Block(x, int16(o[1]), z, 0); !w.pal.IsDoor(nb) && !w.vineDependsOn(nb, dy) {
				continue
			}
			ok := secKey{int32(o[0] >> 4), int32(o[1] >> 4), int32(o[2] >> 4)}
			sections[ok] = append(sections[ok], uint64(p.javaStateAt(col, o))<<12|uint64(x)<<8|uint64(z)<<4|uint64(o[1]&15))
			extra++
		}
		k := secKey{int32(pos[0] >> 4), int32(pos[1] >> 4), int32(pos[2] >> 4)}
		sections[k] = append(sections[k], uint64(state)<<12|uint64(x)<<8|uint64(z)<<4|uint64(pos[1]&15))
		single = append(single, ch)
	}
	if len(single) == 1 && extra == 0 {
		ch := single[0]
		for _, e := range sections {
			p.sendBlockUpdateRaw(int(ch.pos[0]), int(ch.pos[1]), int(ch.pos[2]), uint32(e[0]>>12))
		}
		return
	}
	for k, entries := range sections {
		wr := p.pk()
		wr.Int64(int64(uint64(uint32(k[0])&0x3fffff)<<42 | uint64(uint32(k[2])&0x3fffff)<<20 | uint64(uint32(k[1])&0xfffff)))
		wr.VarInt(int32(len(entries)))
		for _, e := range entries {
			wr.VarLong(int64(e))
		}
		p.send(v777.ClientboundPlaySectionBlocksUpdate, wr)
	}
}

// sendBlockUpdateRaw sends block_update with a state already in the client's numbering.
func (p *Player) sendBlockUpdateRaw(x, y, z int, state uint32) {
	w := p.pk()
	w.Position(x, y, z)
	w.VarInt(int32(state))
	p.send(v777.ClientboundPlayBlockUpdate, w)
}

// publisherUpdate is the area the server keeps chunks of: columns outside it are forgotten.
func (p *Player) publisherUpdate(pk *packet.NetworkChunkPublisherUpdate) {
	w := p.world
	if debugChunks {
		p.log.Debug("publisher update", "pos", pk.Position, "radius", pk.Radius)
	}
	w.pubCentre, w.pubRadius = pk.Position, int32(pk.Radius)
	cx, cz, rad := pk.Position[0]>>4, pk.Position[2]>>4, int32(pk.Radius>>4)+3
	keep := func(pos ChunkPos) bool {
		dx, dz := pos[0]-cx, pos[1]-cz
		return dx*dx+dz*dz <= rad*rad
	}
	for pos, col := range w.cols {
		if keep(pos) {
			continue
		}
		if col.sent {
			p.sendForgetChunk(pos[0], pos[1])
		}
		delete(w.cols, pos)
		for _, f := range chunkForgetHooks {
			f(p, pos)
		}
	}
	for pos := range w.pending {
		if !keep(pos) {
			delete(w.pending, pos)
		}
	}
}

// chunkForgetHooks run when a column is dropped.
var chunkForgetHooks []func(p *Player, pos ChunkPos)

// Block returns the palette index at pos (layer 0) and whether its column is in.
func (w *World) Block(pos cube.Pos) (uint32, bool) {
	col := w.cols[chunkOf(pos)]
	if col == nil || pos.OutOfBounds(w.r) {
		return w.pal.Air, col != nil
	}
	return col.c.Block(uint8(pos[0]&15), int16(pos[1]), uint8(pos[2]&15), 0), true
}

// shown reports whether the Java client has the columns under a player box (width 0.6) at pos:
// sent in a chunk batch it acknowledged.
func (p *Player) shown(pos mgl64.Vec3) bool {
	w := p.world
	if w == nil {
		return false
	}
	for _, d := range [4][2]float64{{-0.3, -0.3}, {-0.3, 0.3}, {0.3, -0.3}, {0.3, 0.3}} {
		x, z := int32(math.Floor(pos[0]+d[0]))>>4, int32(math.Floor(pos[2]+d[1]))>>4
		col := w.cols[ChunkPos{x, z}]
		if col == nil || !col.sent || col.batch > p.batchAcked {
			return false
		}
	}
	return true
}

// HasColumn reports whether the column holding the block x, z is in.
func (w *World) HasColumn(x, z int) bool {
	_, ok := w.cols[ChunkPos{int32(x >> 4), int32(z >> 4)}]
	return ok
}

// standingPos is where a player put at pos stands: on the ground below if above the world, or
// moved up out of the blocks it is in (v1's spawn search).
func (w *World) standingPos(pos mgl64.Vec3) mgl64.Vec3 {
	x, z := int(math.Floor(pos[0])), int(math.Floor(pos[2]))
	solid := func(y int) bool {
		b, _ := w.Block(cube.Pos{x, y, z})
		return w.pal.FullSolid(b)
	}
	if pos[1] > float64(w.r.Max()+1) {
		for y := w.r.Max(); y >= w.r.Min(); y-- {
			if solid(y) {
				return mgl64.Vec3{pos[0], float64(y + 1), pos[2]}
			}
		}
		return mgl64.Vec3{pos[0], float64(w.r.Min() + 1), pos[2]}
	}
	y := int(math.Floor(pos[1]))
	if !solid(y) && !solid(y+1) {
		return pos
	}
	for up := 1; up <= 8; up++ {
		if !solid(y+up) && !solid(y+up+1) {
			return mgl64.Vec3{pos[0], float64(y + up), pos[2]}
		}
	}
	return pos
}

func (w *World) String() string { return fmt.Sprintf("world(dim %d, %d columns)", w.bdim, len(w.cols)) }
