package molang

import (
	"math"
	"strings"
	"testing"
)

type host map[Query]float64

func (h host) Query(q Query, args []float64) float64 {
	if q == QProperty && len(args) == 1 {
		switch s, _ := StringOf(args[0]); s {
		case "ns:mode":
			return Intern("fly")
		case "ns:size":
			return 1.5
		}
		return 0
	}
	return h[q]
}

func TestEval(t *testing.T) {
	syms := NewSymbols()
	env := NewEnv(host{QGroundSpeed: 2, QIsOnGround: 1}, 1)
	env.AnimTime = 0.5
	cases := []struct {
		src  string
		want float64
	}{
		{"1+2*3", 7},
		{"(1+2)*3", 9},
		{"-2.5+math.sin(q.anim_time*90*1)*1", -2.5 + math.Sin(45*math.Pi/180)},
		{"math.cos(Query.Anim_Time*180)", 0},
		{"q.ground_speed > 0.5 || math.abs(q.vertical_speed) > 0.5", 1},
		{"q.is_on_ground ? 3 : 4", 3},
		{"!q.is_on_ground ? 3 : (q.is_in_water ? 5 : 6)", 6},
		{"v.unset ?? 7", 7},
		{"1 - (v.unset ?? 0)", 1},
		{"v.a = 3; v.b = v.a * 2; return v.b + 1;", 7},
		{"v.b", 6},
		{"v.b ?? 9", 6},
		{"t.x = 4; return t.x * v.a;", 12},
		{"q.property('ns:mode') == 'fly'", 1},
		{"q.property('ns:mode') == 'walk'", 0},
		{"q.property('ns:size') * 2", 3},
		{"math.clamp(q.target_x_rotation, -15, 15) * 0.5", 0},
		{"math.mod(7, 4) + math.pow(2, 3) + math.min(1, 2) + math.max(1, 2)", 3 + 8 + 1 + 2},
		{"1 / 0", 0},
		{"math.lerp(2, 4, 0.25)", 2.5},
		{"q.is_on_ground ? 5", 5},
		{"2 > 1 && 3 >= 3 && 1 != 2 && 1 == 1 && 0 <= 0", 1},
		{"1e2 + 1.5f", 101.5},
		{"math.atan2(1, 1)", 45},
		{"v.pose_type = q.is_riding ? 0 : (q.is_sleeping ? 2 : (v.a > 2 ? 4 : 1)); return v.pose_type;", 4},
		{"", 0},
		{"   ;; ", 0},
	}
	for _, c := range cases {
		x, err := Compile(c.src, syms)
		if err != nil {
			t.Errorf("%q: %v", c.src, err)
			continue
		}
		if got := x.Eval(env); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%q = %v, want %v", c.src, got, c.want)
		}
		if !x.Known() {
			t.Errorf("%q: unknown %v", c.src, x.Unknown)
		}
	}
}

func TestUnknownAndErrors(t *testing.T) {
	x, err := Compile("q.is_item_name_any('slot.weapon.mainhand', 0, 'minecraft:stick') ? 20 : 0", nil)
	if err != nil || x.Known() {
		t.Fatalf("unknown query: err %v, unknown %v", err, x.Unknown)
	}
	if v := x.Eval(NewEnv(nil, 1)); v != 0 {
		t.Fatalf("unknown query evaluates to %v", v)
	}
	for _, src := range []string{"(1+2", "v.a = ", "loop(10, {v.x = v.x + 1;})", "geometry.default", "array.skins[q.variant]",
		"1 +* 2", "q.foo(", "'abc", "v.x -> q.y", "1 2", "5 = 3", "foo", strings.Repeat("(", 5000)} {
		if _, err := Compile(src, nil); err == nil {
			t.Errorf("%q compiled", clip(src))
		}
	}
}

func TestConstAndRandom(t *testing.T) {
	x, _ := Compile("2*math.pi", nil)
	if !x.Const || math.Abs(x.Val-2*math.Pi) > 1e-12 {
		t.Fatalf("constant folding: %+v", x)
	}
	x, _ = Compile("math.random(2, 5)", nil)
	if x.Const || !x.Random {
		t.Fatal("math.random is constant")
	}
	env := NewEnv(nil, 7)
	for i := 0; i < 100; i++ {
		if v := x.Eval(env); v < 2 || v > 5 {
			t.Fatalf("random %v", v)
		}
	}
	x, _ = Compile("math.random_integer(1, 3)", nil)
	seen := map[float64]bool{}
	for i := 0; i < 200; i++ {
		seen[x.Eval(env)] = true
	}
	if len(seen) != 3 || !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("random_integer values %v", seen)
	}
}

func BenchmarkEval(b *testing.B) {
	x, _ := Compile("-10+math.sin(q.anim_time*90*0.7-60)*6", nil)
	env := NewEnv(nil, 1)
	for i := 0; i < b.N; i++ {
		env.AnimTime = float64(i) * 0.01
		x.Eval(env)
	}
}
