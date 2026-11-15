package chunk

import (
	"bytes"
	"fmt"

	"github.com/df-mc/dragonfly/server/block/cube"
)

// NetworkDecodeSubChunk decodes one network encoded sub chunk (as found in a SubChunk packet
// entry) from buf, leaving whatever follows it (block entity NBT) in buf. It returns the sub chunk
// and its index in a chunk of range r. (fork)
func NetworkDecodeSubChunk(br BlockRegistry, buf *bytes.Buffer, r cube.Range) (*SubChunk, int, error) {
	c := &Chunk{r: r, br: br, air: br.AirRuntimeID()}
	var index byte
	sub, err := decodeSubChunk(buf, c, &index, NetworkEncoding)
	if err != nil {
		return nil, 0, err
	}
	if int(index) > (r.Height() >> 4) {
		return nil, 0, fmt.Errorf("invalid sub-chunk index %d for range %v", index, r)
	}
	return sub, int(index), nil
}

// InvalidateHeightMap makes the chunk recalculate its height map: needed after sub chunks were
// replaced through Sub(). (fork)
func (chunk *Chunk) InvalidateHeightMap() { chunk.recalculateHeightMap = true }

// NetworkDecodeLenient decodes a LevelChunk payload like NetworkDecodeBuffer, but as much as it
// can: sub chunks past the range are skipped, and when the biomes can't be read (servers that send
// fewer or other biome sections) the blocks are kept with plain biomes. biomeErr reports that;
// err is for payloads whose blocks can't be read. (fork)
func NetworkDecodeLenient(br BlockRegistry, buf *bytes.Buffer, count int, r cube.Range) (c *Chunk, biomeErr, err error) {
	c = New(br, r)
	if count < 0 {
		return nil, nil, fmt.Errorf("invalid sub-chunk count %d", count)
	}
	for i := 0; i < count; i++ {
		index := uint8(i)
		sub, err := decodeSubChunk(buf, c, &index, NetworkEncoding)
		if err != nil {
			return nil, nil, fmt.Errorf("sub chunk %d of %d: %w", i, count, err)
		}
		if int(index) < len(c.sub) {
			c.sub[index] = sub
		}
	}
	var last *PalettedStorage
	for i := 0; i < len(c.sub); i++ {
		b, err := decodePalettedStorage(buf, NetworkEncoding, BiomePaletteEncoding)
		if err != nil || (b == nil && i == 0) {
			if err == nil {
				err = fmt.Errorf("first biome storage pointed to previous one")
			}
			for j := i; j < len(c.sub); j++ {
				if last != nil {
					c.biomes[j] = last
				}
			}
			return c, fmt.Errorf("biomes %d of %d: %w", i, len(c.sub), err), nil
		}
		if b == nil {
			b = last
		} else {
			last = b
		}
		c.biomes[i] = b
	}
	return c, nil, nil
}
