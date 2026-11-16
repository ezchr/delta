package bridge

import (
	"delta/core/internal/blocks"

	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"testing"

	jchunk "github.com/ezchr/go-mcjava/chunk"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Chunks: LevelChunk/SubChunk packets recorded from BDS 1.26.60 (option capture_dir, sub chunk
// request mode, hashed block ids, the overworld around a spawn) run through the translator, and
// every Java column that comes out decoded as a 26.3 client reads it and compared block by
// block with the palette's mapping.

// loadCapture reads testdata/chunks_overworld.bin.gz (id, length, payload per packet).
func loadCapture(t *testing.T) []packet.Packet {
	t.Helper()
	f, err := os.Open("testdata/chunks_overworld.bin.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var out []packet.Packet
	for len(data) >= 8 {
		id, n := binary.LittleEndian.Uint32(data), binary.LittleEndian.Uint32(data[4:])
		body := data[8 : 8+n]
		data = data[8+n:]
		var pk packet.Packet
		switch id {
		case packet.IDLevelChunk:
			pk = &packet.LevelChunk{}
		case packet.IDSubChunk:
			pk = &packet.SubChunk{}
		default:
			t.Fatalf("unexpected packet %d in capture", id)
		}
		pk.Marshal(protocol.NewReader(bytes.NewBuffer(body), 0, false))
		out = append(out, pk)
	}
	return out
}

func TestCapturedChunks(t *testing.T) {
	pks := loadCapture(t)
	p, take := soundPlayer(t, version.Newest)
	p.rem.log = cfg.Log
	p.world = newWorld(p.rem.pal, 0)
	p.javaDim = javaDimension(0)
	levels, subs := 0, 0
	for _, pk := range pks {
		switch pk := pk.(type) {
		case *packet.LevelChunk:
			levels++
			p.handleLevelChunk(pk)
		case *packet.SubChunk:
			subs++
			p.handleSubChunk(pk)
		}
	}
	take() // whatever handling sent so far
	w := p.world
	if len(w.cols) < 9 {
		t.Fatalf("%d complete columns from %d LevelChunk and %d SubChunk packets (pending %d)", len(w.cols), levels, subs, len(w.pending))
	}
	if n := p.rem.pal.UnknownIDs(); n != 0 {
		t.Errorf("%d unknown runtime ids in a vanilla capture", n)
	}
	java, wet := w.pal.JavaFor(p.ver())
	v := p.ver()
	dec := jchunk.NewDecoder(v.BlockStates, v.Biomes)
	checked, blocksChecked, fallback := 0, 0, 0
	for pos, col := range w.cols {
		if err := p.sendColumn(pos, col); err != nil {
			t.Fatalf("column %v: %v", pos, err)
		}
		var got *jchunk.Column
		for _, o := range take() {
			if o.id != v777.ClientboundPlayLevelChunkWithLight {
				continue
			}
			got = &jchunk.Column{}
			if err := dec.Decode(wire.NewReader(o.w.B), got); err != nil {
				t.Fatalf("column %v: the Java client could not decode it: %v", pos, err)
			}
		}
		if got == nil {
			t.Fatalf("column %v: no level_chunk_with_light sent", pos)
		}
		if got.X != pos[0] || got.Z != pos[1] {
			t.Fatalf("column %v sent as %d %d", pos, got.X, got.Z)
		}
		if len(got.Heightmaps) == 0 {
			t.Errorf("column %v: no heightmaps", pos)
		}
		r := col.c.Range()
		off := (r[0] - dimMinY(p.javaDim)) >> 4
		for i, sub := range col.c.Sub() {
			sec := got.Sections[i+off]
			var nonAir int
			for y := byte(0); y < 16; y++ {
				for z := byte(0); z < 16; z++ {
					for x := byte(0); x < 16; x++ {
						idx := sub.Layer(0).At(x, y, z)
						if w.pal.IsDoor(idx) || w.pal.IsVine(idx) || w.pal.BlockEntityState(idx) || w.pal.BlockEntity(idx) != "" {
							continue // drawn from neighbours or block entities (doors.go, vines.go, chests.go, blockentities.go)
						}
						want := java[idx]
						if len(sub.Layers()) > 1 && w.pal.IsWater(sub.Layer(1).At(x, y, z)) {
							want = wet[idx]
						}
						if !w.pal.IsAir(idx) {
							nonAir++
						}
						if want == java[w.pal.Air] && !w.pal.IsAir(idx) {
							fallback++
						}
						if g := sec.Blocks[jchunk.BlockIndex(int(x), int(y), int(z))]; g != want {
							t.Fatalf("column %v section %d (%d %d %d): Java state %d, want %d (%s)", pos, i, x, y, z, g, want, w.pal.States[idx].Name)
						}
						blocksChecked++
					}
				}
			}
			if nonAir > 0 && sec.BlockCount == 0 {
				t.Errorf("column %v section %d: %d blocks but block count 0", pos, i, nonAir)
			}
		}
		checked++
	}
	t.Logf("%d LevelChunk + %d SubChunk packets -> %d columns, %d blocks compared, %d pending, %d non-air shown as air",
		levels, subs, checked, blocksChecked, len(w.pending), fallback)
}

// TestPaletteMapping: the vanilla 1.26.60 block states map to Java 26.3 states (a few samples
// checked by name and property), and few states fall back to stone.
func TestPaletteMapping(t *testing.T) {
	p, _ := soundPlayer(t, version.Newest)
	pal := p.rem.pal
	t.Logf("%d states, %d without a Java mapping", len(pal.States), pal.Unmapped)
	if pal.Unmapped > 64 {
		t.Errorf("%d of %d vanilla states have no Java mapping", pal.Unmapped, len(pal.States))
	}
	java, _ := pal.JavaFor(version.Newest)
	reg := blocks.Registry{P: pal}
	for _, c := range []struct {
		bedrock string
		props   map[string]any
		java    string
		prop    [2]string // a Java property and its expected value ("" = not checked)
	}{
		{"minecraft:stone", nil, "minecraft:stone", [2]string{}},
		{"minecraft:grass_block", nil, "minecraft:grass_block", [2]string{"snowy", "false"}},
		{"minecraft:oak_log", map[string]any{"pillar_axis": "x"}, "minecraft:oak_log", [2]string{"axis", "x"}},
		{"minecraft:water", map[string]any{"liquid_depth": int32(0)}, "minecraft:water", [2]string{"level", "0"}},
		{"minecraft:air", nil, "minecraft:air", [2]string{}},
	} {
		props := c.props
		if props == nil {
			var ok bool
			if props, ok = reg.DefaultProperties(c.bedrock); !ok {
				t.Fatalf("no %s", c.bedrock)
			}
		}
		i, ok := pal.Lookup(c.bedrock, props)
		if !ok {
			t.Fatalf("no %s %v in the palette", c.bedrock, props)
		}
		state := uint32(java[i])
		b := blocks.JavaBlockOf(state)
		if b == nil || b.Name != c.java {
			t.Errorf("%s %v -> %v, want %s", c.bedrock, props, b, c.java)
			continue
		}
		if c.prop[0] != "" {
			if v, _ := b.Get(state, c.prop[0]); v != c.prop[1] {
				t.Errorf("%s %v -> %s[%s=%s], want %s", c.bedrock, props, b.Name, c.prop[0], v, c.prop[1])
			}
		}
	}
}
