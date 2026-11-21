package javapack

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"math"
	"sort"
	"strings"
)

// Armour drawn with a geometry of its own and no classic texture: the layer texture is made from
// the geometry. Each part of Java's armour model (head, body, right arm, right leg) takes the
// texture of the cube of the Bedrock geometry that fills the same box best, face by face. Extra
// cubes (horns, shoulder pads) cannot be drawn on Java and are left out.

// stdPart is a box of the classic armour model: Bedrock geometry position and its UV in the
// 64x32 layer texture.
type stdPart struct {
	alts []geoCube // where the add-on's cube may be (right side first)
	uv   [2]float64
}

var (
	partHead = stdPart{alts: []geoCube{{Origin: [3]float64{-4, 24, -4}, Size: [3]float64{8, 8, 8}}}, uv: [2]float64{0, 0}}
	partBody = stdPart{alts: []geoCube{{Origin: [3]float64{-4, 12, -2}, Size: [3]float64{8, 12, 4}}}, uv: [2]float64{16, 16}}
	partArm  = stdPart{alts: []geoCube{{Origin: [3]float64{-8, 12, -2}, Size: [3]float64{4, 12, 4}},
		{Origin: [3]float64{4, 12, -2}, Size: [3]float64{4, 12, 4}}}, uv: [2]float64{40, 16}}
	partLeg = stdPart{alts: []geoCube{{Origin: [3]float64{-4, 0, -2}, Size: [3]float64{4, 12, 4}},
		{Origin: [3]float64{0, 0, -2}, Size: [3]float64{4, 12, 4}}}, uv: [2]float64{0, 16}}
)

func slotParts(slot string) []stdPart {
	switch slot {
	case "head":
		return []stdPart{partHead}
	case "chest":
		return []stdPart{partBody, partArm}
	case "legs":
		return []stdPart{partBody, partLeg}
	case "feet":
		return []stdPart{partLeg}
	}
	return nil
}

// synthLayer makes a 64x32 (times the source's texel density) layer texture for an armour piece
// from its attachable's geometry and texture. ok is false if no part found a cube.
func (b *builder) synthLayer(a *attachable, slot string) ([]byte, bool) {
	g := b.geos[pick(a.geometry)]
	if g == nil {
		return nil, false
	}
	data, tga, found := b.findTextureIn(a.root, texDefault(a.textures))
	if !found {
		return nil, false
	}
	var src image.Image
	var err error
	if tga {
		src, err = decodeTGA(data)
	} else {
		src, err = png.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, false
	}
	sb := src.Bounds()
	tw, th := g.texW, g.texH
	if tw <= 0 || th <= 0 {
		tw, th = float64(sb.Dx()), float64(sb.Dy())
	}
	dens := math.Max(1, math.Round(float64(sb.Dx())/tw))
	sx, sy := float64(sb.Dx())/tw, float64(sb.Dy())/th
	dst := image.NewNRGBA(image.Rect(0, 0, int(64*dens), int(32*dens)))
	drew := false
	for _, part := range slotParts(slot) {
		std, cubes := partCubes(g, part)
		for n, pc := range cubes {
			drew = true
			c := pc.c
			from := cubeFaces(c, pc.mirror)
			to := cubeFaces(geoCube{Size: std.Size, UV: boxUV(part.uv)}, false)
			for i := 0; i < 4; i++ { // side faces; tops and bottoms only from cubes covering them
				if !from[i].ok || !to[i].ok {
					continue
				}
				s, d, ok := subRects(from[i].uv, to[i].uv, c, std, i)
				if ok {
					blit(dst, d, dens, src, s, sx, sy, n > 0)
				}
			}
			for i := 4; i < 6; i++ {
				if from[i].ok && to[i].ok && pc.flat {
					blit(dst, to[i].uv, dens, src, from[i].uv, sx, sy, n > 0)
				}
			}
		}
	}
	if !drew {
		return nil, false
	}
	var buf bytes.Buffer
	if png.Encode(&buf, dst) != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func boxUV(uv [2]float64) []byte {
	b, _ := json.Marshal(uv)
	return b
}

type partCube struct {
	c            geoCube
	mirror, flat bool // flat: it covers the part's whole top and bottom
	cov          float64
}

// partCubes are the add-on cubes inside one of part's boxes (with 1.5 pixels to spare), unturned
// with their bones, the one covering most first; from the first box that has any.
func partCubes(g *geometry, part stdPart) (geoCube, []partCube) {
	byName := map[string]*geoBone{}
	for i := range g.bones {
		byName[strings.ToLower(g.bones[i].Name)] = &g.bones[i]
	}
	// A part turned a little (a shoulder pad tilted 5 degrees) is drawn as if straight: an
	// add-on's armour often has nothing on the arm but such parts (the sleeve cube is see-through),
	// and leaving them out left Java's arms bare. One turned further is left out.
	const slight = 12.0
	turned := func(b *geoBone) bool {
		var sum [3]float64
		for d := 0; b != nil && d < 64; d++ {
			for a := 0; a < 3; a++ {
				sum[a] += b.Rotation[a]
			}
			b = byName[strings.ToLower(b.Parent)]
		}
		return math.Abs(sum[0]) > slight || math.Abs(sum[1]) > slight || math.Abs(sum[2]) > slight
	}
	cubeTurned := func(r [3]float64) bool {
		return math.Abs(r[0]) > slight || math.Abs(r[1]) > slight || math.Abs(r[2]) > slight
	}
	for _, alt := range part.alts {
		var out []partCube
		for i := range g.bones {
			bn := &g.bones[i]
			if bn.NeverRender || turned(bn) {
				continue
			}
			for _, cb := range bn.Cubes {
				if cubeTurned(cb.Rotation) || len(cb.UV) == 0 {
					continue
				}
				in, cov := true, 1.0
				var axis [3]float64
				for a := 0; a < 3; a++ {
					l0, l1 := alt.Origin[a], alt.Origin[a]+alt.Size[a]
					c0, c1 := cb.Origin[a], cb.Origin[a]+cb.Size[a]
					if c0 < l0-1.5 || c1 > l1+1.5 {
						in = false
						break
					}
					axis[a] = math.Max(0, math.Min(c1, l1)-math.Max(c0, l0)) / alt.Size[a]
					cov *= axis[a]
				}
				if !in || cov < 0.05 {
					continue
				}
				m := bn.Mirror
				if cb.Mirror != nil {
					m = *cb.Mirror
				}
				out = append(out, partCube{c: cb, mirror: m, cov: cov, flat: axis[0] > 0.9 && axis[2] > 0.9})
			}
		}
		if len(out) > 0 {
			sort.SliceStable(out, func(i, j int) bool { return out[i].cov > out[j].cov })
			return alt, out
		}
	}
	return geoCube{}, nil
}

// faceAxes are, for the side faces (north, east, south, west in cubeFaces' order), the Bedrock
// axis the texture's u runs along and its sign (box UV: the strips wrap around the cube).
var faceAxes = [4]struct {
	axis int
	sign float64
}{{0, 1}, {2, -1}, {0, -1}, {2, 1}}

// subRects are the parts of a side face's source (cube) and destination (part) rectangles where
// the cube and the part's box overlap.
func subRects(s, d [4]float64, c, std geoCube, face int) ([4]float64, [4]float64, bool) {
	span := func(axis int, sign float64, box geoCube, lo, hi float64) (f0, f1 float64) {
		b0, b1 := box.Origin[axis], box.Origin[axis]+box.Size[axis]
		if b1 <= b0 {
			return 0, 1
		}
		if sign > 0 {
			return (lo - b0) / (b1 - b0), (hi - b0) / (b1 - b0)
		}
		return (b1 - hi) / (b1 - b0), (b1 - lo) / (b1 - b0)
	}
	cut := func(r [4]float64, uf0, uf1, vf0, vf1 float64) [4]float64 {
		return [4]float64{r[0] + uf0*(r[2]-r[0]), r[1] + vf0*(r[3]-r[1]), r[0] + uf1*(r[2]-r[0]), r[1] + vf1*(r[3]-r[1])}
	}
	fa := faceAxes[face]
	ulo := math.Max(c.Origin[fa.axis], std.Origin[fa.axis])
	uhi := math.Min(c.Origin[fa.axis]+c.Size[fa.axis], std.Origin[fa.axis]+std.Size[fa.axis])
	vlo := math.Max(c.Origin[1], std.Origin[1])
	vhi := math.Min(c.Origin[1]+c.Size[1], std.Origin[1]+std.Size[1])
	if uhi <= ulo || vhi <= vlo {
		return s, d, false
	}
	// v runs from the top (high y) down: sign -1 on y.
	su0, su1 := span(fa.axis, fa.sign, c, ulo, uhi)
	sv0, sv1 := span(1, -1, c, vlo, vhi)
	du0, du1 := span(fa.axis, fa.sign, std, ulo, uhi)
	dv0, dv1 := span(1, -1, std, vlo, vhi)
	return cut(s, su0, su1, sv0, sv1), cut(d, du0, du1, dv0, dv1), true
}

// blit copies the texture rectangle s (texture units, scaled by sx, sy to pixels) of src onto
// rectangle d (layer units, times dens) of dst, nearest pixel; reversed spans flip. over: only
// where the source has pixels.
func blit(dst *image.NRGBA, d [4]float64, dens float64, src image.Image, s [4]float64, sx, sy float64, over bool) {
	x0, x1 := math.Min(d[0], d[2])*dens, math.Max(d[0], d[2])*dens
	y0, y1 := math.Min(d[1], d[3])*dens, math.Max(d[1], d[3])*dens
	if x1-x0 < 0.5 || y1-y0 < 0.5 {
		return
	}
	sb := src.Bounds()
	for y := int(math.Round(y0)); y < int(math.Round(y1)); y++ {
		for x := int(math.Round(x0)); x < int(math.Round(x1)); x++ {
			fx := (float64(x) + 0.5 - d[0]*dens) / ((d[2] - d[0]) * dens)
			fy := (float64(y) + 0.5 - d[1]*dens) / ((d[3] - d[1]) * dens)
			u := (s[0] + fx*(s[2]-s[0])) * sx
			v := (s[1] + fy*(s[3]-s[1])) * sy
			px, py := sb.Min.X+int(math.Floor(u)), sb.Min.Y+int(math.Floor(v))
			if px < sb.Min.X || py < sb.Min.Y || px >= sb.Max.X || py >= sb.Max.Y {
				continue
			}
			if !image.Pt(x, y).In(dst.Bounds()) {
				continue
			}
			if over {
				if _, _, _, a := src.At(px, py).RGBA(); a == 0 {
					continue // later cubes only paint over where they have pixels
				}
			}
			dst.Set(x, y, src.At(px, py))
		}
	}
}
