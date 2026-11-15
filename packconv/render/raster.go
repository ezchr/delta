// Package render is a small software rasteriser for checking converted models offline: it
// draws Java block/item models (their elements, as the Java client builds faces) and Bedrock
// geometry (its cubes, as the game's entity model cubes are textured) with their textures,
// orthographically, to images. The Bedrock side is implemented from the entity cube layout on
// its own, not with the converter's face mapping, so a sheet of both shows conversion mistakes
// (mirrored or turned UVs, wrong faces, misplaced pivots).
package render

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// Vec is a 3D vector.
type Vec [3]float64

func (a Vec) Add(b Vec) Vec     { return Vec{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func (a Vec) Sub(b Vec) Vec     { return Vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a Vec) Mul(k float64) Vec { return Vec{a[0] * k, a[1] * k, a[2] * k} }
func (a Vec) Dot(b Vec) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func (a Vec) Cross(b Vec) Vec {
	return Vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func (a Vec) Len() float64 { return math.Sqrt(a.Dot(a)) }

// Mat is a 3x3 matrix (row major).
type Mat [3][3]float64

// Ident is the identity matrix.
var Ident = Mat{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (a Mat) Mul(b Mat) Mat {
	var m Mat
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			m[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j]
		}
	}
	return m
}

func (a Mat) Apply(v Vec) Vec {
	return Vec{a[0][0]*v[0] + a[0][1]*v[1] + a[0][2]*v[2], a[1][0]*v[0] + a[1][1]*v[1] + a[1][2]*v[2], a[2][0]*v[0] + a[2][1]*v[1] + a[2][2]*v[2]}
}

// RotX/RotY/RotZ are right-handed rotations by degrees.
func RotX(d float64) Mat {
	s, c := math.Sincos(d * math.Pi / 180)
	return Mat{{1, 0, 0}, {0, c, -s}, {0, s, c}}
}
func RotY(d float64) Mat {
	s, c := math.Sincos(d * math.Pi / 180)
	return Mat{{c, 0, s}, {0, 1, 0}, {-s, 0, c}}
}
func RotZ(d float64) Mat {
	s, c := math.Sincos(d * math.Pi / 180)
	return Mat{{c, -s, 0}, {s, c, 0}, {0, 0, 1}}
}

// Quad is a textured quad: four corners in model space (Java model space: x east, y up, z
// south) with a texture coordinate each (texture pixels).
type Quad struct {
	P   [4]Vec
	UV  [4][2]float64
	Tex *image.NRGBA
	// N is the face's outward normal: the face is not drawn from behind (as the Java client
	// culls back faces). Zero: drawn from both sides.
	N Vec
}

// View is a camera: the model is turned by Yaw (about Y) then Pitch (about X) and looked at
// along -z.
type View struct {
	Yaw, Pitch float64
}

// Views are the two sheet views: from the front left above and from the back right above
// (Java model space: the front is north, -z).
var Views = []View{{Yaw: 225, Pitch: 30}, {Yaw: 45, Pitch: 30}, {Yaw: 135, Pitch: -35}}

// Diff is the share of pixels that differ clearly between two renders of the same size (the
// background counts: a missing face differs too).
func Diff(a, b *image.NRGBA) float64 {
	if a.Bounds() != b.Bounds() {
		return 1
	}
	n, d := 0, 0
	for i := 0; i+3 < len(a.Pix); i += 4 {
		n++
		dr := int(a.Pix[i]) - int(b.Pix[i])
		dg := int(a.Pix[i+1]) - int(b.Pix[i+1])
		db := int(a.Pix[i+2]) - int(b.Pix[i+2])
		if dr*dr+dg*dg+db*db > 40*40 {
			d++
		}
	}
	return float64(d) / float64(max(n, 1))
}

// Draw renders quads to a size x size image, fitted to bounds lo..hi (model space).
func Draw(quads []Quad, v View, size int, lo, hi Vec) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.NRGBA{40, 44, 52, 255}}, image.Point{}, draw.Src)
	zbuf := make([]float64, size*size)
	for i := range zbuf {
		zbuf[i] = math.Inf(-1)
	}
	rot := RotX(v.Pitch).Mul(RotY(v.Yaw))
	c := lo.Add(hi).Mul(0.5)
	// Fit: the bounding sphere.
	radius := hi.Sub(lo).Len() / 2
	if radius <= 0 {
		radius = 8
	}
	scale := float64(size) * 0.46 / radius
	light := Vec{0.35, 0.8, 0.5}.Mul(1 / Vec{0.35, 0.8, 0.5}.Len())
	project := func(p Vec) Vec {
		q := rot.Apply(p.Sub(c))
		return Vec{float64(size)/2 + q[0]*scale, float64(size)/2 - q[1]*scale, q[2]}
	}
	for _, q := range quads {
		if q.Tex == nil {
			continue
		}
		if q.N != (Vec{}) && rot.Apply(q.N)[2] <= 1e-9 {
			continue
		}
		n := q.P[1].Sub(q.P[0]).Cross(q.P[2].Sub(q.P[0]))
		if n.Len() < 1e-9 {
			n = q.P[2].Sub(q.P[0]).Cross(q.P[3].Sub(q.P[0]))
		}
		var shade float64 = 1
		if l := n.Len(); l > 0 {
			nn := rot.Apply(n.Mul(1 / l))
			_ = nn
			shade = 0.55 + 0.45*math.Abs(n.Mul(1/l).Dot(light))
		}
		var s [4]Vec
		for i := range s {
			s[i] = project(q.P[i])
		}
		tri(img, zbuf, s[0], s[1], s[2], q.UV[0], q.UV[1], q.UV[2], q.Tex, shade)
		tri(img, zbuf, s[0], s[2], s[3], q.UV[0], q.UV[2], q.UV[3], q.Tex, shade)
	}
	return img
}

// tri rasterises one triangle with perspective-free (affine) texture mapping.
func tri(img *image.NRGBA, zbuf []float64, a, b, c Vec, ta, tb, tc [2]float64, tex *image.NRGBA, shade float64) {
	size := img.Bounds().Dx()
	minX := int(math.Floor(min(a[0], b[0], c[0])))
	maxX := int(math.Ceil(max(a[0], b[0], c[0])))
	minY := int(math.Floor(min(a[1], b[1], c[1])))
	maxY := int(math.Ceil(max(a[1], b[1], c[1])))
	minX, minY = max(minX, 0), max(minY, 0)
	maxX, maxY = min(maxX, size-1), min(maxY, size-1)
	area := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
	if math.Abs(area) < 1e-12 {
		return
	}
	tw, th := tex.Bounds().Dx(), tex.Bounds().Dy()
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			w0 := ((b[0]-px)*(c[1]-py) - (b[1]-py)*(c[0]-px)) / area
			w1 := ((c[0]-px)*(a[1]-py) - (c[1]-py)*(a[0]-px)) / area
			w2 := 1 - w0 - w1
			if w0 < -1e-9 || w1 < -1e-9 || w2 < -1e-9 {
				continue
			}
			z := w0*a[2] + w1*b[2] + w2*c[2]
			if z <= zbuf[y*size+x] {
				continue
			}
			u := w0*ta[0] + w1*tb[0] + w2*tc[0]
			v := w0*ta[1] + w1*tb[1] + w2*tc[1]
			tx := int(math.Floor(u))
			ty := int(math.Floor(v))
			tx = min(max(tx, 0), tw-1)
			ty = min(max(ty, 0), th-1)
			col := tex.NRGBAAt(tx, ty)
			if col.A < 128 {
				continue
			}
			zbuf[y*size+x] = z
			img.SetNRGBA(x, y, color.NRGBA{uint8(float64(col.R) * shade), uint8(float64(col.G) * shade), uint8(float64(col.B) * shade), 255})
		}
	}
}

// Bounds is the box around the quads' corners.
func Bounds(quads []Quad) (lo, hi Vec) {
	lo = Vec{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi = Vec{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, q := range quads {
		for _, p := range q.P {
			for a := 0; a < 3; a++ {
				lo[a], hi[a] = min(lo[a], p[a]), max(hi[a], p[a])
			}
		}
	}
	if math.IsInf(lo[0], 1) {
		return Vec{}, Vec{16, 16, 16}
	}
	return lo, hi
}
