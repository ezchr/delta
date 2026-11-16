// Package anim evaluates Bedrock entity animations (animations/*.json, animation controllers and
// a client entity's scripts) for a model shown bone by bone on Java item displays.
//
// The parts: a Library keeps the animation and controller files of a conversion (compressed) and
// finds them by id; a Rig is one entity type's bones, scripts, controllers and animations,
// compiled; a State is one entity's running animation, stepped with what is known about the
// entity (Inputs) and read as one pose per display (PartPose); Plan decides which bones get a
// display of their own.
//
// Space: everything here is in Java model space, in pixels: a Bedrock model point (x, y, z) is
// (-x, y, z), a Bedrock rotation (rx, ry, rz) in degrees is the matrix Rz(rz) Ry(-ry) Rx(-rx),
// and an animation's position (px, py, pz) moves a bone by (-px, py, pz) in its parent's frame.
package anim

import "math"

// Mat is a 3x3 matrix (row major).
type Mat [3][3]float64

// Ident is the identity matrix.
var Ident = Mat{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (a Mat) mul(b Mat) Mat {
	var m Mat
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			m[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j]
		}
	}
	return m
}

func (a Mat) apply(v [3]float64) [3]float64 {
	return [3]float64{
		a[0][0]*v[0] + a[0][1]*v[1] + a[0][2]*v[2],
		a[1][0]*v[0] + a[1][1]*v[1] + a[1][2]*v[2],
		a[2][0]*v[0] + a[2][1]*v[1] + a[2][2]*v[2],
	}
}

func (a Mat) transpose() Mat {
	return Mat{{a[0][0], a[1][0], a[2][0]}, {a[0][1], a[1][1], a[2][1]}, {a[0][2], a[1][2], a[2][2]}}
}

// rot is a Bedrock rotation (degrees) as a matrix in Java model space: Rz(z) Ry(-y) Rx(-x).
func rot(r [3]float64) Mat {
	if r == ([3]float64{}) {
		return Ident
	}
	sx, cx := math.Sincos(-r[0] * math.Pi / 180)
	sy, cy := math.Sincos(-r[1] * math.Pi / 180)
	sz, cz := math.Sincos(r[2] * math.Pi / 180)
	return Mat{
		{cz * cy, cz*sy*sx - sz*cx, cz*sy*cx + sz*sx},
		{sz * cy, sz*sy*sx + cz*cx, sz*sy*cx - cz*sx},
		{-sy, cy * sx, cy * cx},
	}
}

// Xform is the map p -> R (S * p) + T (S scales each axis before the rotation).
type Xform struct {
	R Mat
	S [3]float64
	T [3]float64
}

// IdentX is the identity transform.
var IdentX = Xform{R: Ident, S: [3]float64{1, 1, 1}}

// Apply transforms a point.
func (x Xform) Apply(p [3]float64) [3]float64 {
	q := x.R.apply([3]float64{x.S[0] * p[0], x.S[1] * p[1], x.S[2] * p[2]})
	return [3]float64{q[0] + x.T[0], q[1] + x.T[1], q[2] + x.T[2]}
}

// Uniform is the transform's scale as one number (the geometric mean of the three).
func (x Xform) Uniform() float64 {
	if x.S[0] == x.S[1] && x.S[1] == x.S[2] {
		return x.S[0]
	}
	return math.Cbrt(math.Abs(x.S[0] * x.S[1] * x.S[2]))
}

// then is x after l (l is applied to the point first). A parent scale that is not uniform is
// exact for where the child's origin goes and taken as uniform for the child's own shape.
func (x Xform) then(l Xform) Xform {
	k := x.Uniform()
	t := x.R.apply([3]float64{x.S[0] * l.T[0], x.S[1] * l.T[1], x.S[2] * l.T[2]})
	return Xform{R: x.R.mul(l.R), S: [3]float64{k * l.S[0], k * l.S[1], k * l.S[2]},
		T: [3]float64{t[0] + x.T[0], t[1] + x.T[1], t[2] + x.T[2]}}
}

// Rel is the transform from n's frame to b's: inverse(b) after n. ok is false when b has no
// inverse (a scale of 0).
func Rel(b, n Xform) (Xform, bool) {
	k := b.Uniform()
	if math.Abs(k) < 1e-6 || math.Abs(b.S[0]) < 1e-6 || math.Abs(b.S[1]) < 1e-6 || math.Abs(b.S[2]) < 1e-6 {
		return IdentX, false
	}
	rt := b.R.transpose()
	d := rt.apply([3]float64{n.T[0] - b.T[0], n.T[1] - b.T[1], n.T[2] - b.T[2]})
	return Xform{R: rt.mul(n.R), S: [3]float64{n.S[0] / k, n.S[1] / k, n.S[2] / k},
		T: [3]float64{d[0] / b.S[0], d[1] / b.S[1], d[2] / b.S[2]}}, true
}

// quat is m as a unit quaternion (x, y, z, w).
func quat(m Mat) [4]float64 {
	var q [4]float64
	tr := m[0][0] + m[1][1] + m[2][2]
	switch {
	case tr > 0:
		s := math.Sqrt(tr+1) * 2
		q = [4]float64{(m[2][1] - m[1][2]) / s, (m[0][2] - m[2][0]) / s, (m[1][0] - m[0][1]) / s, s / 4}
	case m[0][0] > m[1][1] && m[0][0] > m[2][2]:
		s := math.Sqrt(1+m[0][0]-m[1][1]-m[2][2]) * 2
		q = [4]float64{s / 4, (m[0][1] + m[1][0]) / s, (m[0][2] + m[2][0]) / s, (m[2][1] - m[1][2]) / s}
	case m[1][1] > m[2][2]:
		s := math.Sqrt(1+m[1][1]-m[0][0]-m[2][2]) * 2
		q = [4]float64{(m[0][1] + m[1][0]) / s, s / 4, (m[1][2] + m[2][1]) / s, (m[0][2] - m[2][0]) / s}
	default:
		s := math.Sqrt(1+m[2][2]-m[0][0]-m[1][1]) * 2
		q = [4]float64{(m[0][2] + m[2][0]) / s, (m[1][2] + m[2][1]) / s, s / 4, (m[1][0] - m[0][1]) / s}
	}
	n := math.Sqrt(q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3])
	if n < 1e-12 || math.IsNaN(n) {
		return [4]float64{0, 0, 0, 1}
	}
	for i := range q {
		q[i] /= n
	}
	if q[3] < 0 {
		for i := range q {
			q[i] = -q[i]
		}
	}
	return q
}

// QuatMat is the rotation matrix of a quaternion (x, y, z, w).
func QuatMat(q [4]float64) Mat {
	x, y, z, w := q[0], q[1], q[2], q[3]
	return Mat{
		{1 - 2*(y*y+z*z), 2 * (x*y - z*w), 2 * (x*z + y*w)},
		{2 * (x*y + z*w), 1 - 2*(x*x+z*z), 2 * (y*z - x*w)},
		{2 * (x*z - y*w), 2 * (y*z + x*w), 1 - 2*(x*x+y*y)},
	}
}

// PartPose is what one display shows: the item display's translation (blocks), left rotation
// (quaternion x, y, z, w) and scale, for an entity of scale 1 that faces south (yaw 0; the
// display entity's own yaw turns it with the mob).
//
// A display draws its item model's point m (0..16) at T + L (S * Ry180 (m - 8) / 16). A part's
// model has the part's pivot at 8 and was shrunk by K, so for the bone's transform p -> R (s *
// p) + t and its posed pivot c: T = Ry180 c / 16, L = Ry180 R Ry180, S = s / K.
type PartPose struct {
	T [3]float32
	Q [4]float32
	S [3]float32
}

// partPose is the pose of a part whose bone has transform g and bind pivot piv, shrunk by k.
func partPose(g Xform, piv [3]float64, k float64) PartPose {
	c := g.Apply(piv)
	q := quat(g.R)
	if k <= 0 {
		k = 1
	}
	return PartPose{
		T: [3]float32{float32(-c[0] / 16), float32(c[1] / 16), float32(-c[2] / 16)},
		Q: [4]float32{float32(-q[0]), float32(q[1]), float32(-q[2]), float32(q[3])},
		S: [3]float32{float32(g.S[0] / k), float32(g.S[1] / k), float32(g.S[2] / k)},
	}
}

// Near reports whether two poses are the same to the eye: less than a 50th of a pixel and a
// 10th of a degree apart.
func (a PartPose) Near(b PartPose) bool {
	for i := 0; i < 3; i++ {
		if d := a.T[i] - b.T[i]; d > 0.00125 || d < -0.00125 {
			return false
		}
		if d := a.S[i] - b.S[i]; d > 0.002 || d < -0.002 {
			return false
		}
	}
	dot := a.Q[0]*b.Q[0] + a.Q[1]*b.Q[1] + a.Q[2]*b.Q[2] + a.Q[3]*b.Q[3]
	if dot < 0 {
		dot = -dot
	}
	return dot > 0.9999996
}
