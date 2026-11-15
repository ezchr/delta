package javapack

import (
	"encoding/json"
	"math"
	"strings"
)

// Bedrock geometry (models/**/*.geo.json, format 1.12+ and the legacy 1.8/1.10 layout) turned
// into Java model elements.
//
// Axes: Bedrock geometry is mirrored in x against the world (a Bedrock model faces -z with its
// right side at -x), so a point (x, y, z) of a Bedrock model is (-x, y, z) in Java model space,
// and a Bedrock rotation (rx, ry, rz) is (-rx, -ry, rz) there, applied in Z, Y, X order (the
// order Java's euler element rotation uses: Matrix4f.rotationZYX). Box UV strips are, left to
// right: the right side (east once the model faces north), front (north), left (west), back
// (south); top and bottom above the front and left strips, both turned 180 degrees in Java's
// face UV terms.

type geometry struct {
	id         string
	texW, texH float64
	bones      []geoBone
}

type geoBone struct {
	Name        string     `json:"name"`
	Parent      string     `json:"parent"`
	Pivot       [3]float64 `json:"pivot"`
	Rotation    [3]float64 `json:"rotation"`
	Mirror      bool       `json:"mirror"`
	Inflate     float64    `json:"inflate"`
	NeverRender bool       `json:"neverRender"`
	Cubes       []geoCube  `json:"cubes"`
}

type geoCube struct {
	Origin   [3]float64      `json:"origin"`
	Size     [3]float64      `json:"size"`
	Inflate  *float64        `json:"inflate"`
	Pivot    *[3]float64     `json:"pivot"`
	Rotation [3]float64      `json:"rotation"`
	Mirror   *bool           `json:"mirror"`
	UV       json.RawMessage `json:"uv"`
}

// parseGeometries adds the geometries in one .geo.json file to out (by identifier).
func parseGeometries(b []byte, out map[string]*geometry) {
	var top map[string]json.RawMessage
	if json.Unmarshal(stripJSON(b), &top) != nil {
		return
	}
	if raw, ok := top["minecraft:geometry"]; ok {
		var list []struct {
			Description struct {
				Identifier string  `json:"identifier"`
				TexW       float64 `json:"texture_width"`
				TexH       float64 `json:"texture_height"`
			} `json:"description"`
			Bones []geoBone `json:"bones"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return
		}
		for _, g := range list {
			if g.Description.Identifier == "" {
				continue
			}
			out[g.Description.Identifier] = &geometry{id: g.Description.Identifier, texW: g.Description.TexW, texH: g.Description.TexH, bones: g.Bones}
		}
		return
	}
	// Legacy: "geometry.name" or "geometry.name:geometry.parent" keys.
	type legacy struct {
		TexW  float64   `json:"texturewidth"`
		TexH  float64   `json:"textureheight"`
		Bones []geoBone `json:"bones"`
	}
	parents := map[string]string{}
	for k, raw := range top {
		if !strings.HasPrefix(k, "geometry.") {
			continue
		}
		var g legacy
		if json.Unmarshal(raw, &g) != nil {
			continue
		}
		id, parent, _ := strings.Cut(k, ":")
		out[id] = &geometry{id: id, texW: g.TexW, texH: g.TexH, bones: g.Bones}
		if parent != "" {
			parents[id] = parent
		}
	}
	for id, parent := range parents {
		g, p := out[id], out[parent]
		if p == nil {
			continue
		}
		if g.texW == 0 {
			g.texW, g.texH = p.texW, p.texH
		}
		// A child replaces its parent's bones by name and adds its own.
		have := map[string]bool{}
		for _, b := range g.bones {
			have[b.Name] = true
		}
		var bones []geoBone
		for _, b := range p.bones {
			if !have[b.Name] {
				bones = append(bones, b)
			}
		}
		g.bones = append(bones, g.bones...)
	}
}

// mat3 is a 3x3 rotation matrix (row major).
type mat3 [3][3]float64

var ident = mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (a mat3) mul(b mat3) mat3 {
	var m mat3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			m[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j]
		}
	}
	return m
}

func (a mat3) apply(v [3]float64) [3]float64 {
	return [3]float64{
		a[0][0]*v[0] + a[0][1]*v[1] + a[0][2]*v[2],
		a[1][0]*v[0] + a[1][1]*v[1] + a[1][2]*v[2],
		a[2][0]*v[0] + a[2][1]*v[1] + a[2][2]*v[2],
	}
}

func rotX(d float64) mat3 {
	s, c := math.Sincos(d * math.Pi / 180)
	return mat3{{1, 0, 0}, {0, c, -s}, {0, s, c}}
}
func rotY(d float64) mat3 {
	s, c := math.Sincos(d * math.Pi / 180)
	return mat3{{c, 0, s}, {0, 1, 0}, {-s, 0, c}}
}
func rotZ(d float64) mat3 {
	s, c := math.Sincos(d * math.Pi / 180)
	return mat3{{c, -s, 0}, {s, c, 0}, {0, 0, 1}}
}

// javaRot is a Bedrock rotation (degrees) as a rotation matrix in Java model space.
func javaRot(r [3]float64) mat3 {
	if r == ([3]float64{}) {
		return ident
	}
	return rotZ(r[2]).mul(rotY(-r[1])).mul(rotX(-r[0]))
}

// eulerZYX splits m = Rz(z) Ry(y) Rx(x) into its angles in degrees.
func eulerZYX(m mat3) (x, y, z float64) {
	sy := -m[2][0]
	sy = max(-1, min(1, sy))
	y = math.Asin(sy)
	if math.Abs(sy) < 0.99999 {
		x = math.Atan2(m[2][1], m[2][2])
		z = math.Atan2(m[1][0], m[0][0])
	} else {
		x = 0
		z = math.Atan2(-m[0][1], m[1][1])
	}
	const d = 180 / math.Pi
	return x * d, y * d, z * d
}

func isIdent(m mat3) bool {
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			want := 0.0
			if i == j {
				want = 1
			}
			if math.Abs(m[i][j]-want) > 1e-6 {
				return false
			}
		}
	}
	return true
}

// affine is v -> r*v + t.
type affine struct {
	r mat3
	t [3]float64
}

func (a affine) then(b affine) affine { // a after b
	bt := a.r.apply(b.t)
	return affine{r: a.r.mul(b.r), t: [3]float64{bt[0] + a.t[0], bt[1] + a.t[1], bt[2] + a.t[2]}}
}

func (a affine) apply(v [3]float64) [3]float64 {
	w := a.r.apply(v)
	return [3]float64{w[0] + a.t[0], w[1] + a.t[1], w[2] + a.t[2]}
}

// rotAbout is a rotation r about point p.
func rotAbout(p [3]float64, r mat3) affine {
	rp := r.apply(p)
	return affine{r: r, t: [3]float64{p[0] - rp[0], p[1] - rp[1], p[2] - rp[2]}}
}

// element is a Java model element in Java model space pixels (not yet fitted to -16..32).
type element struct {
	from, to [3]float64
	rotated  bool
	origin   [3]float64
	rx, ry   float64
	rz       float64
	faces    [6]face // north, east, south, west, up, down
}

type face struct {
	ok bool
	uv [4]float64 // texture pixels (scaled to 0..16 when written)
}

var faceNames = [6]string{"north", "east", "south", "west", "up", "down"}

// elements converts a geometry's cubes. Bones that are never rendered are left out.
func (g *geometry) elements() []element {
	byName := map[string]*geoBone{}
	for i := range g.bones {
		byName[strings.ToLower(g.bones[i].Name)] = &g.bones[i]
	}
	cache := map[string]affine{}
	var boneT func(b *geoBone, depth int) affine
	boneT = func(b *geoBone, depth int) affine {
		key := strings.ToLower(b.Name)
		if t, ok := cache[key]; ok {
			return t
		}
		t := affine{r: ident}
		if p := byName[strings.ToLower(b.Parent)]; p != nil && p != b && depth < 64 {
			t = boneT(p, depth+1)
		}
		piv := [3]float64{-b.Pivot[0], b.Pivot[1], b.Pivot[2]}
		t = t.then(rotAbout(piv, javaRot(b.Rotation)))
		cache[key] = t
		return t
	}
	var out []element
	for i := range g.bones {
		b := &g.bones[i]
		if b.NeverRender {
			continue
		}
		bt := boneT(b, 0)
		for _, c := range b.Cubes {
			inf := b.Inflate
			if c.Inflate != nil {
				inf = *c.Inflate
			}
			mirror := b.Mirror
			if c.Mirror != nil {
				mirror = *c.Mirror
			}
			lo := [3]float64{-(c.Origin[0] + c.Size[0]) - inf, c.Origin[1] - inf, c.Origin[2] - inf}
			hi := [3]float64{-c.Origin[0] + inf, c.Origin[1] + c.Size[1] + inf, c.Origin[2] + c.Size[2] + inf}
			t := bt
			if c.Rotation != ([3]float64{}) {
				piv := [3]float64{-c.Origin[0] - c.Size[0]/2, c.Origin[1] + c.Size[1]/2, c.Origin[2] + c.Size[2]/2}
				if c.Pivot != nil {
					piv = [3]float64{-c.Pivot[0], c.Pivot[1], c.Pivot[2]}
				}
				t = t.then(rotAbout(piv, javaRot(c.Rotation)))
			}
			e := element{faces: cubeFaces(c, mirror)}
			if isIdent(t.r) {
				e.from, e.to = t.apply(lo), t.apply(hi)
			} else {
				ctr := [3]float64{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
				o := t.apply(ctr)
				for k := 0; k < 3; k++ {
					e.from[k] = lo[k] - ctr[k] + o[k]
					e.to[k] = hi[k] - ctr[k] + o[k]
				}
				e.rotated, e.origin = true, o
				e.rx, e.ry, e.rz = eulerZYX(t.r)
			}
			out = append(out, e)
		}
	}
	return out
}

// cubeFaces is the UV of a cube's faces in texture pixels, in Java face terms.
func cubeFaces(c geoCube, mirror bool) [6]face {
	var f [6]face
	sx, sy, sz := c.Size[0], c.Size[1], c.Size[2]
	var box [2]float64
	if len(c.UV) > 0 && c.UV[0] == '[' {
		if json.Unmarshal(c.UV, &box) != nil {
			return f
		}
	} else if len(c.UV) > 0 && c.UV[0] == '{' {
		var per map[string]struct {
			UV     [2]float64  `json:"uv"`
			UVSize *[2]float64 `json:"uv_size"`
		}
		if json.Unmarshal(c.UV, &per) != nil {
			return f
		}
		for i, n := range faceNames {
			p, ok := per[n]
			if !ok {
				continue
			}
			w, h := 0.0, 0.0
			if p.UVSize != nil {
				w, h = p.UVSize[0], p.UVSize[1]
			}
			u, v := p.UV[0], p.UV[1]
			if i >= 4 { // up/down: turned 180 degrees
				f[i] = face{true, [4]float64{u + w, v + h, u, v}}
			} else {
				f[i] = face{true, [4]float64{u, v, u + w, v + h}}
			}
		}
		return f
	}
	u, v := box[0], box[1]
	u1, u2, u3, u4 := u+sz, u+sz+sx, u+sz+sx+sz, u+2*sz+2*sx
	v1, v2 := v+sz, v+sz+sy
	f[0] = face{true, [4]float64{u1, v1, u2, v2}}     // north (front)
	f[1] = face{true, [4]float64{u, v1, u1, v2}}      // east (right side)
	f[2] = face{true, [4]float64{u3, v1, u4, v2}}     // south (back)
	f[3] = face{true, [4]float64{u2, v1, u3, v2}}     // west (left side)
	f[4] = face{true, [4]float64{u2, v1, u1, v}}      // up
	f[5] = face{true, [4]float64{u2 + sx, v1, u2, v}} // down
	if mirror {
		for i := 0; i < 4; i++ {
			f[i].uv[0], f[i].uv[2] = f[i].uv[2], f[i].uv[0]
		}
		f[1], f[3] = f[3], f[1]
		for i := 4; i < 6; i++ {
			f[i].uv[0], f[i].uv[2] = f[i].uv[2], f[i].uv[0]
		}
	}
	// Faces with no area (a plane's edges) are dropped.
	if sx == 0 || sy == 0 {
		f[0].ok, f[2].ok = sx != 0 && sy != 0, sx != 0 && sy != 0
	}
	if sz == 0 || sy == 0 {
		f[1].ok, f[3].ok = sz != 0 && sy != 0, sz != 0 && sy != 0
	}
	if sx == 0 || sz == 0 {
		f[4].ok, f[5].ok = sx != 0 && sz != 0, sx != 0 && sz != 0
	}
	return f
}

// fit maps elements into Java's -16..32 model box. Entities (centre true) are centred on 8 and
// scaled down when bigger than the box; it returns the scale applied (<= 1) and the centre of the
// elements before it, in Java model space pixels. Blocks are moved from Bedrock's block origin
// (x/z centred on 0) to 0..16 and clamped.
func fit(els []element, entity bool) (k float64, centre [3]float64) {
	if len(els) == 0 {
		return 1, centre
	}
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, e := range els {
		for a := 0; a < 3; a++ {
			lo[a] = min(lo[a], e.from[a], e.to[a])
			hi[a] = max(hi[a], e.from[a], e.to[a])
		}
	}
	k = 1
	var shift [3]float64
	if entity {
		ext := max(hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2])
		if ext > 46 {
			k = 46 / ext
		}
		for a := 0; a < 3; a++ {
			centre[a] = (lo[a] + hi[a]) / 2
		}
	} else {
		shift = [3]float64{8, 0, 8}
	}
	m := func(v [3]float64) [3]float64 {
		var o [3]float64
		for a := 0; a < 3; a++ {
			if entity {
				o[a] = 8 + (v[a]-centre[a])*k
			} else {
				o[a] = max(-16, min(32, v[a]+shift[a]))
			}
		}
		return o
	}
	for i := range els {
		e := &els[i]
		e.from, e.to = m(e.from), m(e.to)
		for a := 0; a < 3; a++ {
			if e.from[a] > e.to[a] {
				e.from[a], e.to[a] = e.to[a], e.from[a]
			}
		}
		if e.rotated {
			e.origin = m(e.origin)
		}
	}
	return k, centre
}

// modelJSON writes elements as a Java model using texture tex (texW x texH pixels).
func modelJSON(els []element, tex string, texW, texH float64) map[string]any {
	if texW <= 0 {
		texW = 16
	}
	if texH <= 0 {
		texH = 16
	}
	var list []any
	for _, e := range els {
		faces := map[string]any{}
		for i, f := range e.faces {
			if !f.ok {
				continue
			}
			faces[faceNames[i]] = map[string]any{
				"uv":      []float64{r4(f.uv[0] * 16 / texW), r4(f.uv[1] * 16 / texH), r4(f.uv[2] * 16 / texW), r4(f.uv[3] * 16 / texH)},
				"texture": "#0",
			}
		}
		if len(faces) == 0 {
			continue
		}
		el := map[string]any{
			"from":  []float64{r4(e.from[0]), r4(e.from[1]), r4(e.from[2])},
			"to":    []float64{r4(e.to[0]), r4(e.to[1]), r4(e.to[2])},
			"faces": faces,
		}
		if e.rotated {
			rot := map[string]any{"origin": []float64{r4(e.origin[0]), r4(e.origin[1]), r4(e.origin[2])}}
			x, y, z := r4(e.rx), r4(e.ry), r4(e.rz)
			switch {
			case y == 0 && z == 0:
				rot["axis"], rot["angle"] = "x", x
			case x == 0 && z == 0:
				rot["axis"], rot["angle"] = "y", y
			case x == 0 && y == 0:
				rot["axis"], rot["angle"] = "z", z
			default: // several axes: the euler form (26.3)
				rot["x"], rot["y"], rot["z"] = x, y, z
			}
			el["rotation"] = rot
		}
		list = append(list, el)
	}
	return map[string]any{
		"textures": map[string]any{"0": tex, "particle": tex},
		"elements": list,
	}
}

func r4(v float64) float64 { return math.Round(v*10000) / 10000 }
