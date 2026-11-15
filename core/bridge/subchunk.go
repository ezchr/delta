package bridge

import (
	"bytes"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Sub chunk request mode (v1's chunks.go): LevelChunk carries only the biomes, the client asks for
// the sub chunks and the server answers with SubChunk. Sub chunks the server could not give yet
// (chunk not found, player not found: servers that answer before they loaded the chunk or
// spawned the player) are asked for again, up to subChunkRetries times, while the column is in
// the server's publisher area. A column given up on stays unsent (the Java client treats it as not
// loaded rather than as one with holes); the server sends it again when it has it.

// pendingColumn is a column in sub chunk request mode waiting for sub chunks.
type pendingColumn struct {
	c       *chunk.Chunk
	be      map[cube.Pos]map[string]any
	waiting map[int8]bool // sub chunk Y (absolute, in sub chunks) still to come
	tries   int
}

// subChunkRetries is how often sub chunks the server could not give are asked for again.
const subChunkRetries = 20

// chunkStats counts chunk traffic for the periodic log line.
type chunkStats struct {
	levelChunks, requestMode, subPackets, columns, decodeErrs, biomeErrs, retries, givenUp atomic.Int64
	results                                                                                [8]atomic.Int64
}

// requestSubChunks asks for the sub chunks (absolute Ys) of the column at pos.
func (p *Player) requestSubChunks(pos ChunkPos, ys []int8) {
	w := p.world
	base := int8(w.r[0] >> 4)
	offsets := make([]protocol.SubChunkOffset, 0, len(ys))
	for _, y := range ys {
		offsets = append(offsets, protocol.SubChunkOffset{0, y - base, 0})
	}
	p.Write(&packet.SubChunkRequest{
		Dimension: w.bdim,
		Position:  protocol.SubChunkPos{pos[0], int32(base), pos[1]},
		Offsets:   offsets,
	})
}

// handleSubChunk fills the columns of a SubChunk answer in.
func (p *Player) handleSubChunk(pk *packet.SubChunk) {
	w, r := p.world, p.rem
	r.stats.subPackets.Add(1)
	for _, e := range pk.SubChunkEntries {
		if int(e.Result) < len(r.stats.results) {
			r.stats.results[e.Result].Add(1)
		}
	}
	if pk.Dimension != w.bdim {
		return
	}
	retry := map[ChunkPos][]int8{}
	touched := map[ChunkPos]bool{}
	changed := map[ChunkPos]*pendingColumn{} // sub chunks of columns we already have
	for _, e := range pk.SubChunkEntries {
		pos := ChunkPos{pk.Position[0] + int32(e.Offset[0]), pk.Position[2] + int32(e.Offset[2])}
		y := int8(pk.Position[1]) + e.Offset[1]
		pc, pending := w.pending[pos]
		if !pending {
			pc = changed[pos]
			if pc == nil {
				// A sub chunk of a column we already have: change a copy of it.
				col := w.cols[pos]
				if col == nil {
					continue
				}
				pc = &pendingColumn{c: col.c.Clone(), be: col.be, waiting: map[int8]bool{}}
				changed[pos] = pc
			}
		}
		switch e.Result {
		case protocol.SubChunkResultSuccess:
			if data, ok := e.RawPayload.Value(); ok && len(data) > 0 {
				be, err := w.decodeSubChunk(pc.c, data)
				if err != nil && r.stats.decodeErrs.Add(1) <= 5 {
					r.log.Warn("sub chunk not readable", "pos", pos, "y", y, "err", err)
				}
				for bp, d := range be {
					pc.be[bp] = d
				}
			}
			delete(pc.waiting, y)
		case protocol.SubChunkResultSuccessAllAir, protocol.SubChunkResultIndexOutOfBounds, protocol.SubChunkResultInvalidDimension:
			if e.Result == protocol.SubChunkResultSuccessAllAir {
				if i := int(y) - int(w.r[0]>>4); i >= 0 && i < len(pc.c.Sub()) {
					pc.c.Sub()[i] = chunk.NewSubChunk(w.pal.Air)
				}
			}
			delete(pc.waiting, y)
		default:
			if pending && pc.waiting[y] {
				retry[pos] = append(retry[pos], y)
			}
		}
		if pending {
			touched[pos] = true
		}
	}
	for pos := range touched {
		pc := w.pending[pos]
		if pc == nil {
			continue
		}
		if len(retry[pos]) > 0 && pc.tries < subChunkRetries && w.inPublisher(pos) {
			pc.tries++
			p.retrySubChunks(w, pos, retry[pos])
			continue
		}
		if len(retry[pos]) > 0 {
			delete(w.pending, pos)
			if r.stats.givenUp.Add(1) <= 30 {
				r.log.Info("column given up (the server said not found)", "pos", pos, "missing", len(pc.waiting), "tries", pc.tries, "inServerArea", w.inPublisher(pos))
			}
			continue
		}
		if len(pc.waiting) == 0 {
			p.finishPending(pos)
		}
	}
	for pos, pc := range changed {
		p.putColumn(pos, pc.c, pc.be)
	}
}

// retrySubChunks asks for sub chunks again in a second, if the column is still waiting then.
func (p *Player) retrySubChunks(w *World, pos ChunkPos, ys []int8) {
	p.rem.stats.retries.Add(1)
	rem := p.rem
	p.after(time.Second, "sub chunk retry", func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.rem != rem || p.world != w {
			return
		}
		if _, still := w.pending[pos]; still {
			p.requestSubChunks(pos, ys)
		}
	})
}

// finishPending stores a column whose sub chunks are all in.
func (p *Player) finishPending(pos ChunkPos) {
	w := p.world
	pc := w.pending[pos]
	delete(w.pending, pos)
	if pc == nil {
		return
	}
	p.putColumn(pos, pc.c, pc.be)
}

// decodeSubChunk decodes one sub chunk of a SubChunk entry into c.
func (w *World) decodeSubChunk(c *chunk.Chunk, data []byte) (map[cube.Pos]map[string]any, error) {
	buf := bytes.NewBuffer(data)
	sub, index, err := chunk.NetworkDecodeSubChunk(w.reg, buf, w.r)
	if err != nil {
		return nil, err
	}
	subs := c.Sub()
	if index < 0 || index >= len(subs) {
		return nil, fmt.Errorf("sub chunk index %d out of range", index)
	}
	w.toIndices([]*chunk.SubChunk{sub})
	subs[index] = sub
	c.InvalidateHeightMap()
	return readBlockEntities(buf), nil
}

// inPublisher reports whether the column at pos is in the area the server keeps chunks of (true
// when it did not say).
func (w *World) inPublisher(pos ChunkPos) bool {
	c, rad := w.pubCentre, w.pubRadius
	if rad <= 0 {
		return true
	}
	dx, dz := pos[0]-c[0]>>4, pos[1]-c[2]>>4
	rc := rad>>4 + 1
	return dx*dx+dz*dz <= rc*rc
}

// logChunkStats logs the chunk traffic every 10 seconds while it changes.
func (r *Remote) logChunkStats() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	var last [6]int64
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
		}
		s := &r.stats
		now := [6]int64{s.levelChunks.Load(), s.subPackets.Load(), s.columns.Load(), s.decodeErrs.Load(), s.biomeErrs.Load(), s.retries.Load()}
		if now == last {
			continue
		}
		last = now
		r.log.Debug("chunks", "level", now[0], "requestMode", s.requestMode.Load(), "subPackets", now[1],
			"ok", s.results[protocol.SubChunkResultSuccess].Load(), "air", s.results[protocol.SubChunkResultSuccessAllAir].Load(),
			"notFound", s.results[protocol.SubChunkResultChunkNotFound].Load(), "noPlayer", s.results[protocol.SubChunkResultPlayerNotFound].Load(),
			"columns", now[2], "retries", now[5], "givenUp", s.givenUp.Load(), "decodeErrs", now[3], "biomeErrs", now[4])
	}
}
