package packconv

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
)

// Armour: Java draws worn armour from equipment assets (assets/<ns>/equipment/<set>.json) whose
// layers are 64x32 textures in the vanilla armour layout: "humanoid" for helmet, chestplate and
// boots, "humanoid_leggings" for leggings. Bedrock armour attachables with the game's armour
// geometry (geometry.humanoid.armor.*, geometry.player.armor.*) use that same layout, so their
// texture regions are copied. Attachables with their own 3D geometry are flattened: each texel
// of a vanilla armour box face is taken from the base cube of the 3D armour that covers the same
// spot (pads, horns and other extra parts are lost).

type armourPiece struct {
	id   string
	kind string // helmet, chestplate, leggings, boots
	att  *attachable
	item *ItemMapping
}

// Vanilla humanoid armour boxes (Bedrock coordinates: the model's right side +x... see
// geometry.go; the boxes are symmetric but the arm and leg are the right ones, at -x) with their
// box UV origin in the 64x32 layer.
type armourBox struct {
	origin, size [3]float64
	uv           [2]int
}

var (
	boxHead     = armourBox{[3]float64{-4, 24, -4}, [3]float64{8, 8, 8}, [2]int{0, 0}}
	boxBody     = armourBox{[3]float64{-4, 12, -2}, [3]float64{8, 12, 4}, [2]int{16, 16}}
	boxRightArm = armourBox{[3]float64{-8, 12, -2}, [3]float64{4, 12, 4}, [2]int{40, 16}}
	boxRightLeg = armourBox{[3]float64{-4, 0, -2}, [3]float64{4, 12, 4}, [2]int{0, 16}}
)

// regions of each piece in the humanoid layer (x0, y0, x1, y1 in a 64x32 texture).
var armourRegions = map[string][][4]int{
	"helmet":     {{0, 0, 64, 16}},
	"chestplate": {{16, 16, 56, 32}},
	"boots":      {{0, 16, 16, 32}},
}

// armour builds equipment assets for the worn pieces, one per armour set (the pieces whose ids
// share a name without the slot word), and fills in their mapping.
func (b *builder) armour(pieces []*armourPiece) {
	sets := map[string][]*armourPiece{}
	for _, p := range pieces {
		key := setName(p.id)
		// Two pieces of the same kind in a set: keep them apart.
		for _, o := range sets[key] {
			if o.kind == p.kind {
				key = p.id
				break
			}
		}
		sets[key] = append(sets[key], p)
	}
	for _, key := range sortedKeys(sets) {
		ps := sets[key]
		var hum, legs *image.NRGBA
		var notes []string
		for _, p := range ps {
			img, w, h, flat, ok := b.armourLayer(p)
			if !ok {
				b.rep.add(catArmour, p.id, Skipped, fmt.Sprintf("armour texture %q not usable (geometry %s)", p.att.texture, p.att.geometry))
				continue
			}
			if flat {
				notes = append(notes, p.id+": 3D armour flattened to the vanilla shape")
			}
			_ = w
			_ = h
			if p.kind == "leggings" {
				legs = img
				continue
			}
			if hum == nil {
				hum = image.NewNRGBA(img.Bounds())
			}
			if img.Bounds() != hum.Bounds() {
				img = resizeNearest(img, hum.Bounds().Dx(), hum.Bounds().Dy())
			}
			sx, sy := hum.Bounds().Dx()/64, hum.Bounds().Dy()/32
			for _, r := range armourRegions[p.kind] {
				rect := image.Rect(r[0]*sx, r[1]*sy, r[2]*sx, r[3]*sy)
				draw.Draw(hum, rect, img, rect.Min, draw.Src)
			}
		}
		if hum == nil && legs == nil {
			continue
		}
		name := b.safeName("equipment", strings.ReplaceAll(key, ":", "/"))
		layers := map[string]any{}
		flatName := strings.ReplaceAll(name, "/", "_")
		if hum != nil {
			b.files[b.assetPath("textures", "entity/equipment/humanoid/"+flatName+".png")] = encodePNG(hum)
			layers["humanoid"] = []any{map[string]any{"texture": b.ns + ":" + flatName}}
		}
		if legs != nil {
			b.files[b.assetPath("textures", "entity/equipment/humanoid_leggings/"+flatName+".png")] = encodePNG(legs)
			layers["humanoid_leggings"] = []any{map[string]any{"texture": b.ns + ":" + flatName}}
		}
		b.json(b.assetPath("equipment", flatName+".json"), map[string]any{"layers": layers})
		asset := b.ns + ":" + flatName
		for _, p := range ps {
			if p.item.Equipment == nil {
				p.item.Equipment = &EquipmentMapping{Slot: armourSlots[p.kind]}
			}
			if p.kind == "helmet" && p.item.Model == "head" {
				continue // drawn as its 3D model on the head
			}
			p.item.Equipment.Asset = asset
		}
		st := Converted
		if len(notes) > 0 {
			st = Approximated
		}
		ids := make([]string, len(ps))
		for i, p := range ps {
			ids[i] = p.kind
		}
		b.rep.add(catArmour, key, st, fmt.Sprintf("equipment asset %s (%s)", asset, strings.Join(ids, ", "))+noteText(notes))
	}
}

// armourLayer is a piece's layer texture: the attachable's texture when it uses the game's
// armour geometry, else the texture flattened from its 3D geometry.
func (b *builder) armourLayer(p *armourPiece) (img *image.NRGBA, w, h int, flattened, ok bool) {
	src, ok := b.loadImage(p.att.texture)
	if !ok {
		return nil, 0, 0, false, false
	}
	if isArmourGeometry(p.att.geometry) {
		bw, bh := src.Bounds().Dx(), src.Bounds().Dy()
		if bw != 2*bh { // not the 64x32 layout (a 64x64 skin-like texture): the top half
			if bw == bh {
				src = crop(src, image.Rect(0, 0, bw, bw/2))
			} else {
				src = resizeNearest(src, 64, 32)
			}
		}
		return src, 64, 32, false, true
	}
	g := b.geos[p.att.geometry]
	if g == nil {
		return nil, 0, 0, false, false
	}
	tw, th := g.texW, g.texH
	if tw <= 0 || th <= 0 {
		tw, th = float64(src.Bounds().Dx()), float64(src.Bounds().Dy())
	}
	scale := [2]float64{float64(src.Bounds().Dx()) / tw, float64(src.Bounds().Dy()) / th}
	out := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	switch p.kind {
	case "helmet":
		armourPart(out, src, scale, boxHead, baseCubes(g, 8, 8, 0))
	case "chestplate":
		armourPart(out, src, scale, boxBody, baseCubes(g, 8, 4, 0))
		armourPart(out, src, scale, boxRightArm, baseCubes(g, 4, 4, -1))
	case "boots":
		armourPart(out, src, scale, boxRightLeg, baseCubes(g, 4, 4, -1))
	case "leggings":
		armourPart(out, src, scale, boxBody, baseCubes(g, 8, 4, 0))
		armourPart(out, src, scale, boxRightLeg, baseCubes(g, 4, 4, -1))
	}
	return out, 64, 32, true, true
}

// armourCube is an unrotated box-UV cube of a 3D armour geometry.
type armourCube struct {
	origin, size [3]float64
	uv           [2]float64
	inflate      float64
	mirror       bool
}

// baseCubes are a geometry's unrotated box-UV cubes whose x/z size is sx/sz (the armour's base
// boxes). side -1 keeps those on the model's right (x < 0), +1 the left, 0 both.
func baseCubes(g *geometry, sx, sz float64, side int) []armourCube {
	bt := g.boneTransforms(nil)
	var out []armourCube
	for _, bn := range g.bones {
		if !isIdent(bt[strings.ToLower(bn.Name)].r) {
			continue
		}
		for _, c := range bn.Cubes {
			if c.Rotation != ([3]float64{}) || len(c.UV) == 0 || c.UV[0] != '[' {
				continue
			}
			if math.Abs(c.Size[0]-sx) > 0.01 || math.Abs(c.Size[2]-sz) > 0.01 {
				continue
			}
			cx := c.Origin[0] + c.Size[0]/2
			if side != 0 && cx*float64(side) <= 0 {
				continue
			}
			var uv [2]float64
			parseJSON(c.UV, &uv)
			inf := bn.Inflate
			if c.Inflate != nil {
				inf = *c.Inflate
			}
			mir := bn.Mirror
			if c.Mirror != nil {
				mir = *c.Mirror
			}
			out = append(out, armourCube{origin: c.Origin, size: c.Size, uv: uv, inflate: inf, mirror: mir})
		}
	}
	return out
}

// armourPart copies the box-UV block of the cube with exactly the vanilla box's size (the
// outermost when several) to the box's spot; without one, each texel is projected onto the
// cubes covering it.
func armourPart(dst, src *image.NRGBA, scale [2]float64, box armourBox, cubes []armourCube) {
	var exact *armourCube
	for i, c := range cubes {
		if math.Abs(c.size[0]-box.size[0]) < 0.01 && math.Abs(c.size[1]-box.size[1]) < 0.01 && math.Abs(c.size[2]-box.size[2]) < 0.01 {
			if exact == nil || c.inflate > exact.inflate {
				exact = &cubes[i]
			}
		}
	}
	sx, sy, sz := int(box.size[0]), int(box.size[1]), int(box.size[2])
	if exact != nil {
		w, h := 2*(sx+sz), sz+sy
		for row := 0; row < h; row++ {
			for col := 0; col < w; col++ {
				x := int((exact.uv[0] + float64(col)) * scale[0])
				y := int((exact.uv[1] + float64(row)) * scale[1])
				if image.Pt(x, y).In(src.Bounds()) {
					if px := src.NRGBAAt(x, y); px.A > 0 {
						dst.SetNRGBA(box.uv[0]+col, box.uv[1]+row, px)
					}
				}
			}
		}
		return
	}
	for _, fc := range []string{"up", "down", "right", "front", "left", "back"} {
		ru, rv, rw, rh := armourRegion(fc, box.uv, box.size)
		for row := 0; row < rh; row++ {
			for col := 0; col < rw; col++ {
				p := facePoint(fc, box.origin, box.size, float64(col)+0.5, float64(row)+0.5)
				var best *armourCube
				for i, c := range cubes {
					if coversFace(fc, c.origin, c.size, p) && (best == nil || outerness(fc, c) > outerness(fc, *best)) {
						best = &cubes[i]
					}
				}
				if best == nil {
					continue
				}
				fs := [3]float64{math.Floor(best.size[0] + 1e-6), math.Floor(best.size[1] + 1e-6), math.Floor(best.size[2] + 1e-6)}
				su, sv, sw, sh := armourRegion(fc, [2]int{int(best.uv[0]), int(best.uv[1])}, fs)
				fcol, frow := faceCoords(fc, best.origin, best.size, p)
				c := min(int(math.Floor(fcol)), sw-1)
				r := min(int(math.Floor(frow)), sh-1)
				if best.mirror {
					c = sw - 1 - c
				}
				x := int(float64(su+c) * scale[0])
				y := int(float64(sv+r) * scale[1])
				if image.Pt(x, y).In(src.Bounds()) {
					dst.SetNRGBA(ru+col, rv+row, src.NRGBAAt(x, y))
				}
			}
		}
	}
}

func armourRegion(fc string, uv [2]int, size [3]float64) (u, v, w, h int) {
	sx, sy, sz := int(size[0]), int(size[1]), int(size[2])
	switch fc {
	case "up":
		return uv[0] + sz, uv[1], sx, sz
	case "down":
		return uv[0] + sz + sx, uv[1], sx, sz
	case "right":
		return uv[0], uv[1] + sz, sz, sy
	case "front":
		return uv[0] + sz, uv[1] + sz, sx, sy
	case "left":
		return uv[0] + sz + sx, uv[1] + sz, sz, sy
	}
	return uv[0] + 2*sz + sx, uv[1] + sz, sx, sy // back
}

func facePoint(fc string, o, s [3]float64, col, row float64) [3]float64 {
	x1, y1, z1 := o[0]+s[0], o[1]+s[1], o[2]+s[2]
	switch fc {
	case "front":
		return [3]float64{o[0] + col, y1 - row, o[2]}
	case "back":
		return [3]float64{x1 - col, y1 - row, z1}
	case "right":
		return [3]float64{o[0], y1 - row, z1 - col}
	case "left":
		return [3]float64{x1, y1 - row, o[2] + col}
	case "up":
		return [3]float64{o[0] + col, y1, z1 - row}
	}
	return [3]float64{o[0] + col, o[1], z1 - row} // down
}

func faceCoords(fc string, o, s [3]float64, p [3]float64) (float64, float64) {
	x1, y1, z1 := o[0]+s[0], o[1]+s[1], o[2]+s[2]
	switch fc {
	case "front":
		return p[0] - o[0], y1 - p[1]
	case "back":
		return x1 - p[0], y1 - p[1]
	case "right":
		return z1 - p[2], y1 - p[1]
	case "left":
		return p[2] - o[2], y1 - p[1]
	}
	return p[0] - o[0], z1 - p[2] // up, down
}

func coversFace(fc string, o, s [3]float64, p [3]float64) bool {
	axes := map[string][2]int{"front": {0, 1}, "back": {0, 1}, "right": {1, 2}, "left": {1, 2}, "up": {0, 2}, "down": {0, 2}}[fc]
	const eps = 1e-4
	for _, a := range axes {
		if p[a] < o[a]-eps || p[a] > o[a]+s[a]+eps {
			return false
		}
	}
	return true
}

func outerness(fc string, c armourCube) float64 {
	o, s, inf := c.origin, c.size, c.inflate
	switch fc {
	case "front":
		return -(o[2] - inf)
	case "back":
		return o[2] + s[2] + inf
	case "right":
		return -(o[0] - inf)
	case "left":
		return o[0] + s[0] + inf
	case "up":
		return o[1] + s[1] + inf
	}
	return -(o[1] - inf)
}

// loadImage decodes a pack texture (PNG or TGA) by Bedrock path.
func (b *builder) loadImage(p string) (*image.NRGBA, bool) {
	data, tga, ok := b.findTexture(p)
	if !ok {
		return nil, false
	}
	img, err := decodeImage(data, tga)
	if err != nil {
		return nil, false
	}
	return img, true
}

func decodeImage(data []byte, tga bool) (*image.NRGBA, error) {
	var img image.Image
	var err error
	if tga {
		img, err = decodeTGA(data)
	} else {
		img, err = png.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	return toNRGBA(img), nil
}

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func crop(img *image.NRGBA, r image.Rectangle) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

func resizeNearest(img *image.NRGBA, w, h int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	sw, sh := img.Bounds().Dx(), img.Bounds().Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.SetNRGBA(x, y, img.NRGBAAt(x*sw/w, y*sh/h))
		}
	}
	return out
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	_ = enc.Encode(&buf, img)
	return buf.Bytes()
}

var _ = color.NRGBA{}
