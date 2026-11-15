package javapack

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"testing"
)

func cfb8Encrypt(key, data []byte) []byte {
	block, _ := aes.NewCipher(key)
	reg := make([]byte, 16)
	copy(reg, key[:16])
	enc := make([]byte, 16)
	out := make([]byte, len(data))
	for i, c := range data {
		block.Encrypt(enc, reg)
		out[i] = c ^ enc[0]
		copy(reg, reg[1:])
		reg[15] = out[i]
	}
	return out
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestEncryptedPack builds an encrypted pack as marketplace/server packs are (contents.json with a
// 256 byte header, per-file keys) inside a top folder and reads it back.
func TestEncryptedPack(t *testing.T) {
	packKey := []byte("0123456789abcdef0123456789abcdef")
	fileKey := []byte("fedcba9876543210fedcba9876543210")
	lang := []byte("item.x:y.name=Secret Thing\n")
	list, _ := json.Marshal(map[string]any{"version": 1, "content": []map[string]string{
		{"path": "texts/en_US.lang", "key": string(fileKey)}, {"path": "manifest.json"}}})
	hdr := make([]byte, 256)
	binary.LittleEndian.PutUint32(hdr[4:], contentsMagic)
	hdr[16] = 4
	copy(hdr[17:], "abcd")
	contents := append(hdr, cfb8Encrypt(packKey, list)...)
	z := zipOf(t, map[string][]byte{
		"Pack/manifest.json":     []byte(`{"header": {"name": "Enc", "uuid": "x"}}`),
		"Pack/contents.json":     contents,
		"Pack/texts/en_US.lang":  cfb8Encrypt(fileKey, lang),
		"Pack/textures/a.png":    []byte("x"),
		"Other/notinpack/b.json": []byte("{}"),
	})
	s, err := ReadSource(bytes.NewReader(z), int64(len(z)), "u", "1.0.0", string(packKey))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.File("texts/en_US.lang")
	if !ok || !bytes.Equal(got, lang) {
		t.Fatalf("lang = %q, want %q", got, lang)
	}
	if s.Name != "Enc" {
		t.Fatalf("name %q", s.Name)
	}
	if _, ok := s.File("notinpack/b.json"); ok {
		t.Fatal("file outside the pack root read")
	}
}

func TestTGA(t *testing.T) {
	// 2x2, 32 bit, bottom-up, raw: pixels in file order bottom-left, bottom-right, top-left, top-right.
	raw := []byte{0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 2, 0, 32, 8}
	px := [][4]byte{{0, 0, 255, 255}, {0, 255, 0, 255}, {255, 0, 0, 255}, {10, 20, 30, 40}} // BGRA
	for _, p := range px {
		raw = append(raw, p[:]...)
	}
	img, err := decodeTGA(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[image.Point]color.NRGBA{{0, 1}: {255, 0, 0, 255}, {1, 1}: {0, 255, 0, 255}, {0, 0}: {0, 0, 255, 255}, {1, 0}: {30, 20, 10, 40}}
	for p, c := range want {
		if got := img.(*image.NRGBA).NRGBAAt(p.X, p.Y); got != c {
			t.Errorf("pixel %v = %v, want %v", p, got, c)
		}
	}
	// The same as RLE data: one raw packet of 4 pixels.
	rle := []byte{0, 0, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 2, 0, 32, 8, 0x03}
	for _, p := range px {
		rle = append(rle, p[:]...)
	}
	img2, err := decodeTGA(rle)
	if err != nil {
		t.Fatal(err)
	}
	for p, c := range want {
		if got := img2.(*image.NRGBA).NRGBAAt(p.X, p.Y); got != c {
			t.Errorf("rle pixel %v = %v, want %v", p, got, c)
		}
	}
}

// TestGeometry checks the axes: a cube on Bedrock's +x side lands on Java's -x side, and box UV
// puts the front strip on the north face.
func TestGeometry(t *testing.T) {
	g := map[string]*geometry{}
	parseGeometries([]byte(`{"format_version":"1.12.0","minecraft:geometry":[{"description":{"identifier":"geometry.t","texture_width":64,"texture_height":32},
		"bones":[{"name":"b","pivot":[0,0,0],"cubes":[{"origin":[2,0,-1],"size":[4,2,6],"uv":[0,0]}]},
		{"name":"r","parent":"b","pivot":[0,0,0],"rotation":[0,90,0],"cubes":[{"origin":[0,0,0],"size":[2,2,2],"uv":[0,0]}]}]}]}`), g)
	els := g["geometry.t"].elements()
	if len(els) != 2 {
		t.Fatalf("%d elements", len(els))
	}
	e := els[0]
	if e.from != [3]float64{-6, 0, -1} || e.to != [3]float64{-2, 2, 5} || e.rotated {
		t.Fatalf("box %v %v", e.from, e.to)
	}
	// size 4x2x6: north (front) = [d, d] .. [d+w, d+h] = [6,6,10,8].
	if e.faces[0].uv != [4]float64{6, 6, 10, 8} {
		t.Fatalf("north uv %v", e.faces[0].uv)
	}
	r := els[1]
	if !r.rotated || r.ry > -89.9 || r.ry < -90.1 {
		t.Fatalf("rotation %v %v %v", r.rx, r.ry, r.rz)
	}
}

