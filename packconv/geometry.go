package packconv

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Bedrock geometry (models/**/*.json: format 1.12+ "minecraft:geometry" lists and the legacy
// 1.8/1.10 "geometry.name[:parent]" keys) turned into Java model elements.
//
// Axes: Bedrock geometry is Java's entity model space with y up: x and z are the same, so a
// Bedrock model faces -z with its right side at +x (rendered at yaw 0 it faces +z, the world's
// south, with its right side at the world's -x... the entity renderer turns it half a turn).
// Java block/item model space is the world's, seen from the model's front: a point (x, y, z) of
// a Bedrock model is (-x, y, z) in Java model space, and a Bedrock rotation (rx, ry, rz) is
// (-rx, -ry, rz) there, applied in Z, Y, X order (Java's euler element rotation:
// Matrix4f.rotationZYX). This is what Blockbench's Bedrock codec does.
//
// Box UV (vanilla entity model layout) strips, left to right: the model's right side (Java
// east), front (north), left side (west), back (south); above the front strip the top, above
// the left strip the bottom. In Java face UV terms the top is turned 180 degrees and the bottom
// mirrored in u (derived from ModelPart.Cube's polygons against FaceInfo's vertex order; the
// same as Blockbench).

type geometry struct {
	id         string
	texW, texH float64
	bones      []geoBone
	polyMesh   bool // has poly_mesh bones (not converted)
}

type geoBone struct {
	Name        string          `json:"name"`
	Parent      string          `json:"parent"`
	Pivot       [3]float64      `json:"pivot"`
	Rotation    [3]float64      `json:"rotation"`
	BindPose    *[3]float64     `json:"bind_pose_rotation"`
	Mirror      bool            `json:"mirror"`
	Inflate     float64         `json:"inflate"`
	NeverRender bool            `json:"neverRender"`
	Cubes       []geoCube       `json:"cubes"`
	PolyMesh    json.RawMessage `json:"poly_mesh"`
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

// parseGeometries adds the geometries in one geometry file to out (by identifier).
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
			out[g.Description.Identifier] = newGeometry(g.Description.Identifier, g.Description.TexW, g.Description.TexH, g.Bones)
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
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, "geometry.") {
			continue
		}
		var g legacy
		if json.Unmarshal(top[k], &g) != nil {
			continue
		}
		id, parent, _ := strings.Cut(k, ":")
		out[id] = newGeometry(id, g.TexW, g.TexH, g.Bones)
		if parent != "" {
			parents[id] = parent
		}
	}
	for _, id := range keys {
		id, _, _ = strings.Cut(id, ":")
		parent, ok := parents[id]
		if !ok {
			continue
		}
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
			have[strings.ToLower(b.Name)] = true
		}
		var bones []geoBone
		for _, b := range p.bones {
			if !have[strings.ToLower(b.Name)] {
				bones = append(bones, b)
			}
		}
		g.bones = append(bones, g.bones...)
		delete(parents, id)
	}
}

func newGeometry(id string, w, h float64, bones []geoBone) *geometry {
	g := &geometry{id: id, texW: w, texH: h, bones: bones}
	for _, b := range bones {
		if len(b.PolyMesh) > 0 && string(b.PolyMesh) != "null" {
			g.polyMesh = true
		}
	}
	return g
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
	if math.Abs(math.Cos(y)) > 1e-6 {
		x = math.Atan2(m[2][1], m[2][2])
		z = math.Atan2(m[1][0], m[0][0])
	} else {
		x = math.Atan2(-m[1][2], m[1][1])
		z = 0
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

var identAffine = affine{r: ident}

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

// conv is a Bedrock point in Java model space.
func conv(p [3]float64) [3]float64 { return [3]float64{-p[0], p[1], p[2]} }

// pose overrides bone rotations and adds offsets (static animation values, Bedrock terms).
type pose struct {
	rot    map[string][3]float64 // bone (lower case) -> rotation added to the bone's own
	pos    map[string][3]float64 // bone -> translation (Bedrock pixels)
	scale  map[string]float64    // bone -> uniform scale
	skip   map[string]bool       // bones whose own rotation is ignored
	hidden map[string]bool       // bones not drawn (with their children)
}

// boneTransforms is each bone's model transform (Java model space), by lower-case name.
func (g *geometry) boneTransforms(ps *pose) map[string]affine {
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
		t := identAffine
		if p := byName[strings.ToLower(b.Parent)]; p != nil && p != b && depth < 64 {
			t = boneT(p, depth+1)
		}
		piv := conv(b.Pivot)
		rot := b.Rotation
		if ps != nil {
			if ps.skip[key] {
				rot = [3]float64{}
			}
			if r, ok := ps.rot[key]; ok {
				rot = [3]float64{rot[0] + r[0], rot[1] + r[1], rot[2] + r[2]}
			}
			if d, ok := ps.pos[key]; ok {
				t = t.then(affine{r: ident, t: conv(d)})
			}
			if s, ok := ps.scale[key]; ok && s > 0 && s != 1 {
				sm := mat3{{s, 0, 0}, {0, s, 0}, {0, 0, s}}
				t = t.then(rotAbout(piv, sm))
			}
		}
		t = t.then(rotAbout(piv, javaRot(rot)))
		cache[key] = t
		return t
	}
	out := map[string]affine{}
	for i := range g.bones {
		out[strings.ToLower(g.bones[i].Name)] = boneT(&g.bones[i], 0)
	}
	return out
}

// boneParentTransform is the transform of a bone's parent (identity for a root bone).
func (g *geometry) bone(name string) *geoBone {
	for i := range g.bones {
		if strings.EqualFold(g.bones[i].Name, name) {
			return &g.bones[i]
		}
	}
	return nil
}

// cubeInst is one cube in Java model space before placement: its box (from lo to hi) and the
// transform from box space to model space.
type cubeInst struct {
	lo, hi [3]float64
	t      affine
	faces  [6]face
	bone   string // lower case
}

// element is a Java model element in Java model space pixels.
type element struct {
	from, to [3]float64
	rotated  bool
	origin   [3]float64
	rx, ry   float64
	rz       float64
	faces    [6]face // north, east, south, west, up, down
}

type face struct {
	ok  bool
	uv  [4]float64 // texture pixels (scaled to 0..16 when written)
	rot int        // face uv rotation, degrees
	mat string     // material instance name ("" the default, "*")
}

var faceNames = [6]string{"north", "east", "south", "west", "up", "down"}

// cubes lists a geometry's cubes (bones never rendered left out). bones limits them to those
// bones (lower case; nil: all).
func (g *geometry) cubes(ps *pose, bones map[string]bool) []cubeInst {
	bt := g.boneTransforms(ps)
	var out []cubeInst
	for i := range g.bones {
		b := &g.bones[i]
		key := strings.ToLower(b.Name)
		if b.NeverRender || (bones != nil && !bones[key]) || g.hiddenBone(key, ps) {
			continue
		}
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
			t := bt[key]
			if c.Rotation != ([3]float64{}) {
				piv := [3]float64{-c.Origin[0] - c.Size[0]/2, c.Origin[1] + c.Size[1]/2, c.Origin[2] + c.Size[2]/2}
				if c.Pivot != nil {
					piv = conv(*c.Pivot)
				}
				t = t.then(rotAbout(piv, javaRot(c.Rotation)))
			}
			f := cubeFaces(c, mirror)
			has := false
			for _, x := range f {
				has = has || x.ok
			}
			if !has {
				continue
			}
			out = append(out, cubeInst{lo: lo, hi: hi, t: t, faces: f, bone: key})
		}
	}
	return out
}

// hiddenBone reports whether a bone or one of its parents is hidden by ps.
func (g *geometry) hiddenBone(key string, ps *pose) bool {
	if ps == nil || len(ps.hidden) == 0 {
		return false
	}
	for depth := 0; key != "" && depth < 64; depth++ {
		if ps.hidden[key] {
			return true
		}
		b := g.bone(key)
		if b == nil {
			return false
		}
		key = strings.ToLower(b.Parent)
	}
	return false
}

// bounds is the box around every cube corner after the transforms.
func cubeBounds(cs []cubeInst) (lo, hi [3]float64) {
	lo = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, c := range cs {
		for i := 0; i < 8; i++ {
			q := c.lo
			if i&1 != 0 {
				q[0] = c.hi[0]
			}
			if i&2 != 0 {
				q[1] = c.hi[1]
			}
			if i&4 != 0 {
				q[2] = c.hi[2]
			}
			p := c.t.apply(q)
			for a := 0; a < 3; a++ {
				lo[a] = min(lo[a], p[a])
				hi[a] = max(hi[a], p[a])
			}
		}
	}
	return lo, hi
}

// place turns cubes into elements placed by p -> k * post(p) about nothing: the final point of a
// model point p is post.r*(k*p) + post.t, where p already went through the cube's own transform.
func place(cs []cubeInst, post affine, k float64) []element {
	var out []element
	for _, c := range cs {
		r := post.r.mul(c.t.r)
		pt := post.r.apply(c.t.t)
		t := [3]float64{k*pt[0] + post.t[0], k*pt[1] + post.t[1], k*pt[2] + post.t[2]}
		var lo, hi, ctr [3]float64
		for a := 0; a < 3; a++ {
			lo[a], hi[a] = k*c.lo[a], k*c.hi[a]
			ctr[a] = (lo[a] + hi[a]) / 2
		}
		rc := r.apply(ctr)
		var d [3]float64
		for a := 0; a < 3; a++ {
			d[a] = t[a] + rc[a] - ctr[a]
		}
		e := element{faces: c.faces}
		for a := 0; a < 3; a++ {
			e.from[a], e.to[a] = lo[a]+d[a], hi[a]+d[a]
		}
		if !isIdent(r) {
			e.rotated = true
			for a := 0; a < 3; a++ {
				e.origin[a] = ctr[a] + d[a]
			}
			e.rx, e.ry, e.rz = eulerZYX(r)
		}
		out = append(out, e)
	}
	return out
}

// elements converts a geometry's cubes as they are (Java model space, not fitted).
func (g *geometry) elements() []element {
	return place(g.cubes(nil, nil), identAffine, 1)
}

// cubeFaces is the UV of a cube's faces in texture pixels, in Java face terms.
func cubeFaces(c geoCube, mirror bool) [6]face {
	var f [6]face
	sx, sy, sz := c.Size[0], c.Size[1], c.Size[2]
	var box [2]float64
	if len(c.UV) > 0 && c.UV[0] == '{' {
		var per map[string]struct {
			UV     [2]float64  `json:"uv"`
			UVSize *[2]float64 `json:"uv_size"`
			UVRot  float64     `json:"uv_rotation"`
			Mat    string      `json:"material_instance"`
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
			} else { // the face's own size
				switch i {
				case 0, 2:
					w, h = sx, sy
				case 1, 3:
					w, h = sz, sy
				default:
					w, h = sx, sz
				}
			}
			u, v := p.UV[0], p.UV[1]
			if i >= 4 { // up/down: turned 180 degrees
				f[i] = face{ok: true, uv: [4]float64{u + w, v + h, u, v}}
			} else {
				f[i] = face{ok: true, uv: [4]float64{u, v, u + w, v + h}}
			}
			f[i].rot = ((int(p.UVRot)%360)/90*90 + 360) % 360
			f[i].mat = p.Mat
		}
		dropFlat(&f, sx, sy, sz)
		return f
	}
	if len(c.UV) > 0 && c.UV[0] == '[' {
		if json.Unmarshal(c.UV, &box) != nil {
			return f
		}
	}
	// Box UV sizes are whole pixels (the game floors them).
	fx, fy, fz := math.Floor(sx+1e-6), math.Floor(sy+1e-6), math.Floor(sz+1e-6)
	u, v := box[0], box[1]
	f[1] = face{ok: true, uv: [4]float64{u, v + fz, u + fz, v + fz + fy}}                      // east: the model's right side
	f[0] = face{ok: true, uv: [4]float64{u + fz, v + fz, u + fz + fx, v + fz + fy}}            // north: front
	f[3] = face{ok: true, uv: [4]float64{u + fz + fx, v + fz, u + 2*fz + fx, v + fz + fy}}     // west: left side
	f[2] = face{ok: true, uv: [4]float64{u + 2*fz + fx, v + fz, u + 2*fz + 2*fx, v + fz + fy}} // south: back
	f[4] = face{ok: true, uv: [4]float64{u + fz + fx, v + fz, u + fz, v}}                      // up: turned 180
	f[5] = face{ok: true, uv: [4]float64{u + fz + 2*fx, v, u + fz + fx, v + fz}}               // down: mirrored in u
	if mirror {
		f[1], f[3] = f[3], f[1]
		for i := range f {
			f[i].uv[0], f[i].uv[2] = f[i].uv[2], f[i].uv[0]
		}
	}
	dropFlat(&f, sx, sy, sz)
	return f
}

// dropFlat handles planes (a size of 0): faces with no area are dropped, and a plane shows both
// sides (Bedrock draws its back face; Java culls it), so a missing opposite face is added as the
// mirror image of the one there.
func dropFlat(f *[6]face, sx, sy, sz float64) {
	const eps = 1e-6
	if sx < eps || sy < eps {
		f[0].ok, f[2].ok = false, false
	}
	if sz < eps || sy < eps {
		f[1].ok, f[3].ok = false, false
	}
	if sx < eps || sz < eps {
		f[4].ok, f[5].ok = false, false
	}
	for i := range f {
		if f[i].ok && f[i].uv[0] == f[i].uv[2] && f[i].uv[1] == f[i].uv[3] {
			f[i].ok = false
		}
	}
	pairs := []struct {
		a, b int
		flat bool
	}{{0, 2, sz < eps}, {1, 3, sx < eps}, {4, 5, sy < eps}}
	for _, p := range pairs {
		if !p.flat {
			continue
		}
		if f[p.a].ok && !f[p.b].ok {
			f[p.b] = mirrorFace(p.b, f[p.a])
		} else if f[p.b].ok && !f[p.a].ok {
			f[p.a] = mirrorFace(p.a, f[p.b])
		}
	}
}

// mirrorFace is face src seen from behind (the opposite face of a plane).
func mirrorFace(dst int, src face) face {
	out := src
	if dst < 4 { // side faces: mirrored in u
		out.uv[0], out.uv[2] = src.uv[2], src.uv[0]
	} else { // up/down: mirrored in v
		out.uv[1], out.uv[3] = src.uv[3], src.uv[1]
	}
	return out
}

// fit maps elements into Java's -16..32 model box. Entities (centre true) are centred on 8 and
// scaled down when bigger than the box; it returns the scale applied (<= 1) and the centre of the
// elements before it, in Java model space pixels. Blocks are moved from Bedrock's block origin
// (x/z centred on 0) to 0..16 and clamped.
func fit(els []element, entity bool) (k float64, centre [3]float64) {
	if len(els) == 0 {
		return 1, centre
	}
	lo, hi := elementBounds(els)
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

// elementBounds is the box around the elements' from/to (rotations ignored: Java checks from/to).
func elementBounds(els []element) (lo, hi [3]float64) {
	lo = [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi = [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, e := range els {
		for a := 0; a < 3; a++ {
			lo[a] = min(lo[a], e.from[a], e.to[a])
			hi[a] = max(hi[a], e.from[a], e.to[a])
		}
	}
	return lo, hi
}

// inJavaBox reports whether every element's from/to is within -16..32.
func inJavaBox(els []element) bool {
	for _, e := range els {
		for a := 0; a < 3; a++ {
			for _, v := range []float64{e.from[a], e.to[a]} {
				if v < -16-1e-6 || v > 32+1e-6 {
					return false
				}
			}
		}
	}
	return true
}

// fitScale is the largest k <= 1 with which points scaled about centre c (then moved to target)
// stay within -16+margin..32-margin.
func fitScale(lo, hi [3]float64, c, target [3]float64, margin float64) float64 {
	k := 1.0
	for a := 0; a < 3; a++ {
		for _, v := range []float64{lo[a], hi[a]} {
			d := v - c[a]
			if d < 0 {
				if lim := -16 + margin - target[a]; target[a]+d < -16+margin {
					k = min(k, lim/d)
				}
			} else if d > 0 {
				if lim := 32 - margin - target[a]; target[a]+d > 32-margin {
					k = min(k, lim/d)
				}
			}
		}
	}
	return max(k, 1e-3)
}

// modelJSON writes elements as a Java model using texture tex (texW x texH pixels).
func modelJSON(els []element, tex string, texW, texH float64) map[string]any {
	return modelJSONTex(els, func(string) string { return tex }, texW, texH)
}

// modelJSONTex writes elements as a Java model; texOf gives the texture of a face's material
// instance ("" none: the face is left out). UVs are in texW x texH pixels.
func modelJSONTex(els []element, texOf func(mat string) string, texW, texH float64) map[string]any {
	if texW <= 0 {
		texW = 16
	}
	if texH <= 0 {
		texH = 16
	}
	refs := map[string]string{} // texture id -> "#n"
	textures := map[string]any{}
	ref := func(tex string) string {
		if r, ok := refs[tex]; ok {
			return r
		}
		r := fmt.Sprint(len(refs))
		refs[tex] = "#" + r
		textures[r] = tex
		if len(refs) == 1 {
			textures["particle"] = tex
		}
		return "#" + r
	}
	list := []any{}
	for _, e := range els {
		faces := map[string]any{}
		for i, f := range e.faces {
			if !f.ok {
				continue
			}
			tex := texOf(f.mat)
			if tex == "" {
				continue
			}
			fc := map[string]any{
				"uv":      []float64{r4(f.uv[0] * 16 / texW), r4(f.uv[1] * 16 / texH), r4(f.uv[2] * 16 / texW), r4(f.uv[3] * 16 / texH)},
				"texture": ref(tex),
			}
			if f.rot != 0 {
				fc["rotation"] = f.rot
			}
			faces[faceNames[i]] = fc
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
			default: // several axes: the euler form (Java 26.2+)
				rot["x"], rot["y"], rot["z"] = x, y, z
			}
			el["rotation"] = rot
		}
		list = append(list, el)
	}
	return map[string]any{"textures": textures, "elements": list}
}

func r4(v float64) float64 {
	v = math.Round(v*10000) / 10000
	if v == 0 {
		return 0 // no -0
	}
	return v
}
