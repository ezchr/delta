package render

import (
	"encoding/json"
	"fmt"
	"image"
	"strings"
)

// JavaModel is a Java block/item model with its parents resolved.
type JavaModel struct {
	Textures map[string]string
	Elements []JavaElement
	// Generated is set for item/generated and item/handheld models (a flat sprite of layer0).
	Generated bool
}

// JavaElement is one model element.
type JavaElement struct {
	From, To [3]float64
	Rotation *struct {
		Origin [3]float64 `json:"origin"`
		Axis   string     `json:"axis"`
		Angle  float64    `json:"angle"`
		X      float64    `json:"x"`
		Y      float64    `json:"y"`
		Z      float64    `json:"z"`
	} `json:"rotation"`
	Faces map[string]struct {
		UV       *[4]float64 `json:"uv"`
		Texture  string      `json:"texture"`
		Rotation int         `json:"rotation"`
	} `json:"faces"`
}

// cubeElement is a full cube element with textures #down.. #east (block/cube).
func cubeElement() []JavaElement {
	var e JavaElement
	e.From, e.To = [3]float64{0, 0, 0}, [3]float64{16, 16, 16}
	_ = json.Unmarshal([]byte(`{"down":{"texture":"#down"},"up":{"texture":"#up"},"north":{"texture":"#north"},
		"south":{"texture":"#south"},"west":{"texture":"#west"},"east":{"texture":"#east"}}`), &e.Faces)
	return []JavaElement{e}
}

// LoadJavaModel reads a model by id ("ns:item/x") through load (which returns a model file's
// JSON by id), following parents. Built-in parents: block/cube, block/cube_all, item/generated,
// item/handheld, block/block.
func LoadJavaModel(id string, load func(id string) ([]byte, bool)) (*JavaModel, error) {
	m := &JavaModel{Textures: map[string]string{}}
	for depth := 0; depth < 16 && id != ""; depth++ {
		switch strings.TrimPrefix(id, "minecraft:") {
		case "block/cube":
			if m.Elements == nil {
				m.Elements = cubeElement()
			}
			return m, nil
		case "block/cube_all":
			for _, f := range []string{"down", "up", "north", "south", "west", "east"} {
				if _, ok := m.Textures[f]; !ok {
					m.Textures[f] = "#all"
				}
			}
			if m.Elements == nil {
				m.Elements = cubeElement()
			}
			return m, nil
		case "item/generated", "item/handheld":
			m.Generated = m.Elements == nil
			return m, nil
		case "block/block":
			return m, nil
		}
		data, ok := load(id)
		if !ok {
			return m, fmt.Errorf("model %s not found", id)
		}
		var f struct {
			Parent   string            `json:"parent"`
			Textures map[string]string `json:"textures"`
			Elements []JavaElement     `json:"elements"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			return m, fmt.Errorf("model %s: %w", id, err)
		}
		for k, v := range f.Textures {
			if _, ok := m.Textures[k]; !ok {
				m.Textures[k] = v
			}
		}
		if m.Elements == nil && f.Elements != nil {
			m.Elements = f.Elements
		}
		id = f.Parent
	}
	return m, nil
}

// resolveTex follows #references to a texture id.
func (m *JavaModel) resolveTex(ref string) string {
	for i := 0; i < 8 && strings.HasPrefix(ref, "#"); i++ {
		ref = m.Textures[strings.TrimPrefix(ref, "#")]
	}
	return ref
}

// javaFaceVerts are FaceInfo's corner order per face: each corner picks min (0) or max (1) of
// x, y, z. Corner i gets texture coordinate (u0 for i 0,1 else u1; v0 for i 0,3 else v1).
var javaFaceVerts = map[string][4][3]int{
	"down":  {{0, 0, 1}, {0, 0, 0}, {1, 0, 0}, {1, 0, 1}},
	"up":    {{0, 1, 0}, {0, 1, 1}, {1, 1, 1}, {1, 1, 0}},
	"north": {{1, 1, 0}, {1, 0, 0}, {0, 0, 0}, {0, 1, 0}},
	"south": {{0, 1, 1}, {0, 0, 1}, {1, 0, 1}, {1, 1, 1}},
	"west":  {{0, 1, 0}, {0, 0, 0}, {0, 0, 1}, {0, 1, 1}},
	"east":  {{1, 1, 1}, {1, 0, 1}, {1, 0, 0}, {1, 1, 0}},
}

// defaultUV is a face's UV when the model gives none (from the element's position).
func defaultUV(face string, f, t [3]float64) [4]float64 {
	switch face {
	case "down":
		return [4]float64{f[0], 16 - t[2], t[0], 16 - f[2]}
	case "up":
		return [4]float64{f[0], f[2], t[0], t[2]}
	case "north":
		return [4]float64{16 - t[0], 16 - t[1], 16 - f[0], 16 - f[1]}
	case "south":
		return [4]float64{f[0], 16 - t[1], t[0], 16 - f[1]}
	case "west":
		return [4]float64{f[2], 16 - t[1], t[2], 16 - f[1]}
	}
	return [4]float64{16 - t[2], 16 - t[1], 16 - f[2], 16 - f[1]} // east
}

// Quads builds the model's faces; tex returns a texture image by id.
func (m *JavaModel) Quads(tex func(id string) *image.NRGBA) []Quad {
	if m.Generated {
		t := tex(m.resolveTex("#layer0"))
		if t == nil {
			return nil
		}
		w, h := float64(t.Bounds().Dx()), float64(t.Bounds().Dy())
		// The sprite stands in the x/y plane, facing north (and south), as item/generated does.
		return []Quad{
			{P: [4]Vec{{16, 16, 7.5}, {16, 0, 7.5}, {0, 0, 7.5}, {0, 16, 7.5}}, UV: [4][2]float64{{0, 0}, {0, h}, {w, h}, {w, 0}}, Tex: t},
			{P: [4]Vec{{0, 16, 8.5}, {0, 0, 8.5}, {16, 0, 8.5}, {16, 16, 8.5}}, UV: [4][2]float64{{0, 0}, {0, h}, {w, h}, {w, 0}}, Tex: t},
		}
	}
	var out []Quad
	for _, e := range m.Elements {
		rot := Ident
		var origin Vec
		if r := e.Rotation; r != nil {
			origin = Vec(r.Origin)
			switch r.Axis {
			case "x":
				rot = RotX(r.Angle)
			case "y":
				rot = RotY(r.Angle)
			case "z":
				rot = RotZ(r.Angle)
			default:
				rot = RotZ(r.Z).Mul(RotY(r.Y)).Mul(RotX(r.X))
			}
		}
		for name, fc := range e.Faces {
			verts, ok := javaFaceVerts[name]
			if !ok {
				continue
			}
			t := tex(m.resolveTex(fc.Texture))
			if t == nil {
				continue
			}
			uv := defaultUV(name, e.From, e.To)
			if fc.UV != nil {
				uv = *fc.UV
			}
			sx, sy := float64(t.Bounds().Dx())/16, float64(t.Bounds().Dy())/16 // (an animated strip: pass its first frame)
			var q Quad
			q.Tex = t
			for i, c := range verts {
				p := Vec{pick(c[0], e.From[0], e.To[0]), pick(c[1], e.From[1], e.To[1]), pick(c[2], e.From[2], e.To[2])}
				q.P[i] = rot.Apply(p.Sub(origin)).Add(origin)
				j := (i + fc.Rotation/90) % 4
				u := uv[2]
				if j == 0 || j == 1 {
					u = uv[0]
				}
				v := uv[3]
				if j == 0 || j == 3 {
					v = uv[1]
				}
				q.UV[i] = [2]float64{u * sx, v * sy}
			}
			q.N = q.P[1].Sub(q.P[0]).Cross(q.P[2].Sub(q.P[0]))
			out = append(out, q)
		}
	}
	return out
}

func pick(c int, lo, hi float64) float64 {
	if c == 0 {
		return lo
	}
	return hi
}
