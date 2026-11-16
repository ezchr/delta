package render

import (
	"encoding/json"
	"image"
	"math"
	"strings"
)

// Bedrock geometry drawn the way the game textures entity model cubes. Corners are worked out in
// Bedrock model space (y up) and the box UV layout follows the entity model cube: polygons and
// texture coordinates per corner as Minecraft's ModelPart.Cube builds them (its y axis points
// down, so its "down" polygon is the top). The result is shown in Java model space (x negated),
// the converter's frame, so the two renders can be put side by side.

// BedrockBone is a geometry bone.
type BedrockBone struct {
	Name        string        `json:"name"`
	Parent      string        `json:"parent"`
	Pivot       [3]float64    `json:"pivot"`
	Rotation    [3]float64    `json:"rotation"`
	Mirror      bool          `json:"mirror"`
	Inflate     float64       `json:"inflate"`
	NeverRender bool          `json:"neverRender"`
	Cubes       []BedrockCube `json:"cubes"`
}

// BedrockCube is a geometry cube.
type BedrockCube struct {
	Origin   [3]float64      `json:"origin"`
	Size     [3]float64      `json:"size"`
	Inflate  *float64        `json:"inflate"`
	Pivot    *[3]float64     `json:"pivot"`
	Rotation [3]float64      `json:"rotation"`
	Mirror   *bool           `json:"mirror"`
	UV       json.RawMessage `json:"uv"`
}

// BedrockGeometry is one geometry.
type BedrockGeometry struct {
	TexW, TexH float64
	Bones      []BedrockBone
}

// ParseBedrockGeometry finds geometry id in a geometry file (1.12+ or legacy layout; a legacy
// child's parent bones must be in the same file).
func ParseBedrockGeometry(data []byte, id string) (*BedrockGeometry, bool) {
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return nil, false
	}
	if raw, ok := top["minecraft:geometry"]; ok {
		var list []struct {
			Description struct {
				Identifier string  `json:"identifier"`
				TexW       float64 `json:"texture_width"`
				TexH       float64 `json:"texture_height"`
			} `json:"description"`
			Bones []BedrockBone `json:"bones"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return nil, false
		}
		for _, g := range list {
			if g.Description.Identifier == id {
				return &BedrockGeometry{TexW: g.Description.TexW, TexH: g.Description.TexH, Bones: g.Bones}, true
			}
		}
		return nil, false
	}
	for k, raw := range top {
		name, parent, _ := strings.Cut(k, ":")
		if name != id {
			continue
		}
		var g struct {
			TexW  float64       `json:"texturewidth"`
			TexH  float64       `json:"textureheight"`
			Bones []BedrockBone `json:"bones"`
		}
		if json.Unmarshal(raw, &g) != nil {
			return nil, false
		}
		out := &BedrockGeometry{TexW: g.TexW, TexH: g.TexH, Bones: g.Bones}
		if parent != "" {
			if p, ok := ParseBedrockGeometry(data, parent); ok {
				have := map[string]bool{}
				for _, b := range out.Bones {
					have[strings.ToLower(b.Name)] = true
				}
				var bones []BedrockBone
				for _, b := range p.Bones {
					if !have[strings.ToLower(b.Name)] {
						bones = append(bones, b)
					}
				}
				out.Bones = append(bones, out.Bones...)
				if out.TexW == 0 {
					out.TexW, out.TexH = p.TexW, p.TexH
				}
			}
		}
		return out, true
	}
	return nil, false
}

// bedrockRot is a Bedrock rotation as a matrix in Bedrock model space (the converter's Java
// rotation conjugated by the x mirror).
func bedrockRot(r [3]float64) Mat {
	return RotZ(-r[2]).Mul(RotY(r[1])).Mul(RotX(-r[0]))
}

type affine struct {
	m Mat
	t Vec
}

func (a affine) apply(v Vec) Vec      { return a.m.Apply(v).Add(a.t) }
func (a affine) then(b affine) affine { return affine{a.m.Mul(b.m), a.m.Apply(b.t).Add(a.t)} }
func about(p Vec, m Mat) affine       { return affine{m, p.Sub(m.Apply(p))} }

// Options limit what is drawn.
type Options struct {
	Bones map[string]bool // only these bones (lower case); nil all
	Skip  map[string]bool // bones whose rotation is ignored
	Post  Mat             // turned by this after (zero: identity)
	// Pose is an animation pose by bone (lower case), in the animation's own terms: Rot is added
	// to the bone's rotation, Pos moves it, Scale scales it about its pivot.
	Pose map[string]BonePose
}

// Quads builds the geometry's faces in Java model space (x negated). tex is the texture.
func (g *BedrockGeometry) Quads(tex *image.NRGBA, o Options) []Quad {
	byName := map[string]*BedrockBone{}
	for i := range g.Bones {
		byName[strings.ToLower(g.Bones[i].Name)] = &g.Bones[i]
	}
	cache := map[string]affine{}
	var boneT func(b *BedrockBone, d int) affine
	boneT = func(b *BedrockBone, d int) affine {
		k := strings.ToLower(b.Name)
		if t, ok := cache[k]; ok {
			return t
		}
		t := affine{m: Ident}
		if p := byName[strings.ToLower(b.Parent)]; p != nil && p != b && d < 64 {
			t = boneT(p, d+1)
		}
		r := b.Rotation
		if o.Skip[k] {
			r = [3]float64{}
		}
		if bp, ok := o.Pose[k]; ok {
			r = [3]float64{r[0] + bp.Rot[0], r[1] + bp.Rot[1], r[2] + bp.Rot[2]}
			t = t.then(affine{m: Ident, t: Vec(bp.Pos)})
			t = t.then(about(Vec(b.Pivot), bedrockRot(r)))
			t = t.then(about(Vec(b.Pivot), Mat{{bp.Scale[0], 0, 0}, {0, bp.Scale[1], 0}, {0, 0, bp.Scale[2]}}))
			cache[k] = t
			return t
		}
		t = t.then(about(Vec(b.Pivot), bedrockRot(r)))
		cache[k] = t
		return t
	}
	post := o.Post
	if post == (Mat{}) {
		post = Ident
	}
	tw, th := g.TexW, g.TexH
	if tw <= 0 {
		tw = float64(tex.Bounds().Dx())
	}
	if th <= 0 {
		th = float64(tex.Bounds().Dy())
	}
	ku, kv := float64(tex.Bounds().Dx())/tw, float64(tex.Bounds().Dy())/th
	var out []Quad
	for i := range g.Bones {
		b := &g.Bones[i]
		if b.NeverRender || (o.Bones != nil && !o.Bones[strings.ToLower(b.Name)]) {
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
			x0, y0, z0 := c.Origin[0]-inf, c.Origin[1]-inf, c.Origin[2]-inf
			x1, y1, z1 := c.Origin[0]+c.Size[0]+inf, c.Origin[1]+c.Size[1]+inf, c.Origin[2]+c.Size[2]+inf
			if mirror {
				x0, x1 = x1, x0
			}
			t := bt
			if c.Rotation != ([3]float64{}) {
				piv := Vec{c.Origin[0] + c.Size[0]/2, c.Origin[1] + c.Size[1]/2, c.Origin[2] + c.Size[2]/2}
				if c.Pivot != nil {
					piv = Vec(*c.Pivot)
				}
				t = t.then(about(piv, bedrockRot(c.Rotation)))
			}
			v := [8]Vec{{x0, y1, z0}, {x1, y1, z0}, {x1, y0, z0}, {x0, y0, z0}, {x0, y1, z1}, {x1, y1, z1}, {x1, y0, z1}, {x0, y0, z1}}
			type poly struct {
				name string
				idx  [4]int
			}
			polys := []poly{
				{"up", [4]int{5, 4, 0, 1}}, {"down", [4]int{2, 3, 7, 6}}, {"east", [4]int{0, 4, 7, 3}},
				{"north", [4]int{1, 0, 3, 2}}, {"west", [4]int{5, 1, 2, 6}}, {"south", [4]int{4, 5, 6, 7}},
			}
			params := map[string][4]float64{}
			if len(c.UV) > 0 && c.UV[0] == '{' {
				var per map[string]struct {
					UV     [2]float64  `json:"uv"`
					UVSize *[2]float64 `json:"uv_size"`
				}
				_ = json.Unmarshal(c.UV, &per)
				for name, f := range per {
					w, h := 0.0, 0.0
					if f.UVSize != nil {
						w, h = f.UVSize[0], f.UVSize[1]
					} else {
						switch name {
						case "north", "south":
							w, h = c.Size[0], c.Size[1]
						case "east", "west":
							w, h = c.Size[2], c.Size[1]
						default:
							w, h = c.Size[0], c.Size[2]
						}
					}
					params[name] = [4]float64{f.UV[0], f.UV[1], f.UV[0] + w, f.UV[1] + h}
				}
			} else {
				var uv [2]float64
				_ = json.Unmarshal(c.UV, &uv)
				u, vv := uv[0], uv[1]
				w, h, d := math.Floor(c.Size[0]+1e-6), math.Floor(c.Size[1]+1e-6), math.Floor(c.Size[2]+1e-6)
				f4, f5, f6, f7, f8, f9 := u, u+d, u+d+w, u+d+2*w, u+2*d+w, u+2*d+2*w
				f10, f11, f12 := vv, vv+d, vv+d+h
				params = map[string][4]float64{
					"up": {f5, f10, f6, f11}, "down": {f6, f11, f7, f10}, "east": {f4, f11, f5, f12},
					"north": {f5, f11, f6, f12}, "west": {f6, f11, f8, f12}, "south": {f8, f11, f9, f12},
				}
			}
			for _, p := range polys {
				pr, ok := params[p.name]
				if !ok || (pr[0] == pr[2] && pr[1] == pr[3]) {
					continue
				}
				var q Quad
				q.Tex = tex
				// Polygon corners 0..3 get (u2,v1) (u1,v1) (u1,v2) (u2,v2).
				uvs := [4][2]float64{{pr[2], pr[1]}, {pr[0], pr[1]}, {pr[0], pr[3]}, {pr[2], pr[3]}}
				var fc Vec
				for k, vi := range p.idx {
					w := t.apply(v[vi])
					w = post.Apply(Vec{-w[0], w[1], w[2]})
					q.P[k] = w
					fc = fc.Add(w.Mul(0.25))
					q.UV[k] = [2]float64{uvs[k][0] * ku, uvs[k][1] * kv}
				}
				// Outward normal: the polygon's side of the cube (a mirrored cube swaps the x
				// sides). Planes too: each side of a plane is its own face.
				n := map[string]Vec{"up": {0, 1, 0}, "down": {0, -1, 0}, "north": {0, 0, -1}, "south": {0, 0, 1},
					"east": {-1, 0, 0}, "west": {1, 0, 0}}[p.name]
				if mirror {
					n[0] = -n[0]
				}
				n = t.m.Apply(n)
				q.N = post.Apply(Vec{-n[0], n[1], n[2]})
				_ = fc
				out = append(out, q)
			}
		}
	}
	return out
}

// BonePose is one bone's animated values (Bedrock animation terms).
type BonePose struct {
	Rot, Pos, Scale [3]float64
}
