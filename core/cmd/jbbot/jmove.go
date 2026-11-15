package main

// Accurate Java 26.3 player movement for movement tests (the default physicsTick is a rough
// approximation): LocalPlayer.aiStep's sprint start/stop, LivingEntity.aiStep (velocity cut-offs,
// jumpFromGround with noJumpDelay and the sprint-jump boost), travelInAir (moveRelative with the
// movement_speed attribute + the sprint modifier on the ground, 0.02/0.026 in the air, gravity
// and friction from the attributes the bridge sends), Entity.move/collide with block boxes (full
// cubes, slabs) and step-up, and the packets in a client tick's order: player_input (when the keys
// changed), player_command START/STOP_SPRINTING, move_player_pos_rot, client_tick_end.
//
// Steps: jrun SECONDS [jump] [walk] [yaw]: hold forward (sprint key unless walk), with jump held if
// "jump"; jstat: corrections since the last jrun started. Flag -jlog logs every tick.

import (
	"encoding/json"
	"flag"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	jchunk "github.com/df-mc/dragonfly/server/java/protocol/chunk"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
)

// jsimTick is called in physicsTick after ridingTick (main.go); true: it was a jrun tick.
var jsimTick func(b *bot) bool

var jlog = flag.Bool("jlog", false, "jrun: log every tick")

// jsim is the accurate movement state (one bot per process).
var jsim struct {
	active                                  bool
	keys                                    byte // forward 1, jump 0x10, sprint key 0x40
	sentKeys                                byte
	sprinting                               bool // Java isSprinting
	sentSprint                              bool
	noJumpDelay                             int
	flying, flyReq                          bool
	flySpeed                                float32
	flyAt                                   int
	jumpUntil                               int
	errTick                                 int
	errDZ                                   float64
	jumpFrom                                int     // hold jump from this tick of the run on (j=N)
	turn                                    float32 // yaw change per tick (t=DEG)
	strafe                                  float32 // xxa: 1 left, -1 right (s=l|r)
	hColl, minorHColl                       bool
	speedAttr, jumpAttr, gravAttr, stepAttr float64

	ticks, jumps       int
	startRel, startAbs int
	startPos           [3]float64
	lastTickPos        [3]float64
}

// blockShape: 0 none, 1 full, 2 bottom slab, 3 top slab.
var blockShape []byte

func init() {
	jsim.speedAttr, jsim.jumpAttr, jsim.gravAttr, jsim.stepAttr, jsim.flySpeed = 0.1, 0.42, 0.08, 0.6, 0.05
	extraSteps["jrun"] = jrunStep
	extraSteps["jstat"] = func(b *bot, a []string) time.Duration { jstat(b, "JSTAT"); return 0 }
	packetHooks = append(packetHooks, jsimPacket)
	jsimTick = jsimPhysics
}

func loadShapes() {
	data, err := os.ReadFile(*blocksRep)
	if err != nil {
		return
	}
	var rep map[string]struct {
		States []struct {
			ID         int               `json:"id"`
			Properties map[string]string `json:"properties"`
		} `json:"states"`
	}
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	blockShape = make([]byte, jchunk.VanillaBlockStates)
	for n, bl := range rep {
		slab := strings.HasSuffix(n, "_slab")
		for _, s := range bl.States {
			if s.ID >= len(blockShape) || solidState == nil || !solidState[s.ID] {
				continue
			}
			blockShape[s.ID] = 1
			if slab {
				switch s.Properties["type"] {
				case "bottom":
					blockShape[s.ID] = 2
				case "top":
					blockShape[s.ID] = 3
				}
			}
		}
	}
}

func jrunStep(b *bot, a []string) time.Duration {
	if blockShape == nil {
		loadShapes()
	}
	secs, _ := strconv.ParseFloat(a[1], 64)
	b.mu.Lock()
	jsim.keys = 1 | 0x40
	jsim.jumpFrom, jsim.turn, jsim.strafe, jsim.errTick, jsim.flyAt, jsim.jumpUntil = 0, 0, 0, 0, 0, 0
	for _, w := range a[2:] {
		switch {
		case w == "jump":
			jsim.keys |= 0x10
		case w == "fly": // start flying (player_abilities), Java flight physics until landing
			jsim.flyReq, jsim.flyAt = true, 0
		case strings.HasPrefix(w, "f="): // start flying at that tick of the run
			jsim.flyAt, _ = strconv.Atoi(w[2:])
		case w == "up": // jump key: ascend while flying
			jsim.keys |= 0x10
		case w == "down": // shift: descend while flying
			jsim.keys |= 0x20
		case w == "nofwd":
			jsim.keys &^= 1 | 0x40
		case w == "walk":
			jsim.keys &^= 0x40
		case strings.HasPrefix(w, "j="):
			jsim.keys |= 0x10
			jsim.jumpFrom, _ = strconv.Atoi(w[2:])
		case strings.HasPrefix(w, "jr="): // release jump from that tick on
			jsim.jumpUntil, _ = strconv.Atoi(w[3:])
		case strings.HasPrefix(w, "t="):
			v, _ := strconv.ParseFloat(w[2:], 64)
			jsim.turn = float32(v)
		case strings.HasPrefix(w, "e="): // e=TICK:DZ: send the position of that tick off by DZ (the sim is not)
			f := strings.SplitN(w[2:], ":", 2)
			if len(f) == 2 {
				jsim.errTick, _ = strconv.Atoi(f[0])
				jsim.errDZ, _ = strconv.ParseFloat(f[1], 64)
			}
		case w == "s=l":
			jsim.strafe = 1
		case w == "s=r":
			jsim.strafe = -1
		default:
			if v, err := strconv.ParseFloat(w, 64); err == nil {
				b.yaw = float32(v)
			}
		}
	}
	if !jsim.active && b.onGround && b.vel[1] == 0 {
		b.vel[1] = -jsim.gravAttr * 0.98 // Java standing still keeps falling into the ground
	}
	jsim.active = true
	jsim.ticks, jsim.jumps = 0, 0
	jsim.startRel, jsim.startAbs, jsim.startPos = b.relTP, b.absTP, b.pos
	b.mu.Unlock()
	time.AfterFunc(time.Duration(secs*float64(time.Second)), func() {
		b.mu.Lock()
		jsim.keys = 0
		b.mu.Unlock()
		time.Sleep(1500 * time.Millisecond)
		b.mu.Lock()
		jstat(b, "JRUN done")
		b.mu.Unlock()
	})
	return time.Duration(secs*float64(time.Second)) + 2*time.Second
}

func jstat(b *bot, what string) {
	d := math.Hypot(b.pos[0]-jsim.startPos[0], b.pos[2]-jsim.startPos[2])
	log.Printf("%s: ticks %d jumps %d corrections: relative %d absolute %d, moved %.2f blocks horizontally, now %.3f %.3f %.3f",
		what, jsim.ticks, jsim.jumps, b.relTP-jsim.startRel, b.absTP-jsim.startAbs, d, b.pos[0], b.pos[1], b.pos[2])
}

func jsimPacket(b *bot, id int32, body []byte) {
	if id == v777.ClientboundPlayPlayerAbilities {
		r := &wire.Reader{B: body}
		fl := r.Byte()
		fs := r.Float32()
		if r.Err == nil {
			jsim.flying, jsim.flySpeed = fl&2 != 0, fs
			if jsim.flying && !jsim.active { // the server has us flying: Java flight physics
				if blockShape == nil {
					loadShapes()
				}
				jsim.active = true
			}
		}
		return
	}
	if id != v777.ClientboundPlayUpdateAttributes {
		return
	}
	r := &wire.Reader{B: body}
	if r.VarInt() != 1 {
		return
	}
	for n := r.VarInt(); n > 0 && r.Err == nil; n-- {
		aid := r.VarInt()
		v := r.Float64()
		for m := r.VarInt(); m > 0 && r.Err == nil; m-- {
			r.String(256)
			r.Float64()
			r.Byte()
		}
		if r.Err != nil {
			return
		}
		switch aid {
		case v777.BuiltinID("minecraft:attribute", "minecraft:movement_speed"):
			jsim.speedAttr = v
		case v777.BuiltinID("minecraft:attribute", "minecraft:jump_strength"):
			jsim.jumpAttr = v
		case v777.BuiltinID("minecraft:attribute", "minecraft:gravity"):
			jsim.gravAttr = v
		case v777.BuiltinID("minecraft:attribute", "minecraft:step_height"):
			jsim.stepAttr = v
		}
	}
}

type aabb struct{ min, max [3]float64 }

func (a aabb) move(d [3]float64) aabb {
	for i := range 3 {
		a.min[i] += d[i]
		a.max[i] += d[i]
	}
	return a
}

func (a aabb) expand(d [3]float64) aabb {
	for i := range 3 {
		if d[i] < 0 {
			a.min[i] += d[i]
		} else {
			a.max[i] += d[i]
		}
	}
	return a
}

// boxesIn are the block collision boxes intersecting area.
func (b *bot) boxesIn(area aabb) []aabb {
	var out []aabb
	x0, y0, z0 := int(math.Floor(area.min[0]-1e-7)), int(math.Floor(area.min[1]-1e-7))-1, int(math.Floor(area.min[2]-1e-7))
	x1, y1, z1 := int(math.Floor(area.max[0]+1e-7)), int(math.Floor(area.max[1]+1e-7)), int(math.Floor(area.max[2]+1e-7))
	for x := x0; x <= x1; x++ {
		for z := z0; z <= z1; z++ {
			col := b.chunks[[2]int32{int32(x >> 4), int32(z >> 4)}]
			for y := y0; y <= y1; y++ {
				var shape byte = 1
				if col != nil {
					i := (y - b.minY) >> 4
					if i < 0 || i >= len(col.Sections) {
						continue
					}
					st := col.Sections[i].Blocks[jchunk.BlockIndex(x&15, y&15, z&15)]
					if int(st) < len(blockShape) {
						shape = blockShape[st]
					} else {
						shape = 0
					}
				}
				lo, hi := 0.0, 1.0
				switch shape {
				case 0:
					continue
				case 2:
					hi = 0.5
				case 3:
					lo = 0.5
				}
				box := aabb{[3]float64{float64(x), float64(y) + lo, float64(z)}, [3]float64{float64(x + 1), float64(y) + hi, float64(z + 1)}}
				if box.max[0] > area.min[0] && box.min[0] < area.max[0] && box.max[1] > area.min[1] && box.min[1] < area.max[1] &&
					box.max[2] > area.min[2] && box.min[2] < area.max[2] {
					out = append(out, box)
				}
			}
		}
	}
	return out
}

// collideAxis is Shapes.collide for one axis.
func collideAxis(axis int, box aabb, shapes []aabb, d float64) float64 {
	if math.Abs(d) < 1e-7 {
		return 0
	}
	o1, o2 := (axis+1)%3, (axis+2)%3
	for _, s := range shapes {
		if s.max[o1] <= box.min[o1]+1e-7 || s.min[o1] >= box.max[o1]-1e-7 || s.max[o2] <= box.min[o2]+1e-7 || s.min[o2] >= box.max[o2]-1e-7 {
			continue
		}
		if d > 0 && s.min[axis] >= box.max[axis]-1e-7 {
			d = math.Min(d, s.min[axis]-box.max[axis])
		} else if d < 0 && s.max[axis] <= box.min[axis]+1e-7 {
			d = math.Max(d, s.max[axis]-box.min[axis])
		}
	}
	return d
}

func collideWithShapes(m [3]float64, box aabb, shapes []aabb) [3]float64 {
	if len(shapes) == 0 {
		return m
	}
	order := []int{1, 0, 2}
	if math.Abs(m[0]) < math.Abs(m[2]) {
		order = []int{1, 2, 0}
	}
	var res [3]float64
	for _, axis := range order {
		if m[axis] != 0 {
			res[axis] = collideAxis(axis, box.move(res), shapes, m[axis])
		}
	}
	return res
}

func hdist2(v [3]float64) float64 { return v[0]*v[0] + v[2]*v[2] }

// collide is Entity.collide with step-up.
func (b *bot) collideJ(m [3]float64) [3]float64 {
	box := aabb{[3]float64{b.pos[0] - 0.3, b.pos[1], b.pos[2] - 0.3}, [3]float64{b.pos[0] + 0.3, b.pos[1] + 1.8, b.pos[2] + 0.3}}
	step := m
	if m != ([3]float64{}) {
		step = collideWithShapes(m, box, b.boxesIn(box.expand(m)))
	}
	xC, yC, zC := m[0] != step[0], m[1] != step[1], m[2] != step[2]
	groundAfter := yC && m[1] < 0
	maxUp := jsim.stepAttr
	if maxUp > 0 && (groundAfter || b.onGround) && (xC || zC) {
		grounded := box
		if groundAfter {
			grounded = box.move([3]float64{0, step[1], 0})
		}
		area := grounded.expand([3]float64{m[0], maxUp, m[2]})
		if !groundAfter {
			area = area.expand([3]float64{0, -1e-5, 0})
		}
		cols := b.boxesIn(area)
		skip := float32(step[1])
		var cands []float32
		for _, c := range cols {
			for _, y := range []float64{c.min[1], c.max[1]} {
				rel := float32(y - grounded.min[1])
				if rel < 0 || rel == skip || float64(rel) > maxUp {
					continue
				}
				dup := false
				for _, e := range cands {
					dup = dup || e == rel
				}
				if !dup {
					cands = append(cands, rel)
				}
			}
		}
		for i := range cands {
			for j := i + 1; j < len(cands); j++ {
				if cands[j] < cands[i] {
					cands[i], cands[j] = cands[j], cands[i]
				}
			}
		}
		for _, h := range cands {
			s := collideWithShapes([3]float64{m[0], float64(h), m[2]}, grounded, cols)
			if hdist2(s) > hdist2(step) {
				s[1] -= box.min[1] - grounded.min[1]
				return s
			}
		}
	}
	return step
}

func jsimPhysics(b *bot) bool {
	if !jsim.active {
		return false
	}
	b.mu.Lock()
	if b.dead {
		b.mu.Unlock()
		b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
		return true
	}
	if b.chunks[[2]int32{int32(math.Floor(b.pos[0])) >> 4, int32(math.Floor(b.pos[2])) >> 4}] == nil {
		b.mu.Unlock()
		b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
		return true
	}
	if !b.loadedSent {
		b.loadedSent = true
		b.write(v777.ServerboundPlayPlayerLoaded, &wire.Writer{})
	}
	jsim.ticks++
	keys := jsim.keys
	if keys != 0 && (jsim.ticks < jsim.jumpFrom || jsim.jumpUntil > 0 && jsim.ticks >= jsim.jumpUntil) {
		keys &^= 0x10
	}
	var xxa float32
	if keys != 0 {
		b.yaw += jsim.turn
		xxa = jsim.strafe
		if xxa > 0 {
			keys |= 4
		} else if xxa < 0 {
			keys |= 8
		}
	}
	forward := keys&1 != 0
	jumping := keys&0x10 != 0
	// LocalPlayer.aiStep: sprint start (sprint key, forward impulse, food > 6) and stop.
	if !jsim.sprinting && forward && b.food > 6 && keys&0x40 != 0 && (keys&0x20 == 0 || jsim.flying) { // not while crouching (isMovingSlowly)
		jsim.sprinting = true
	}
	if jsim.sprinting && (!forward || b.food <= 6 || jsim.hColl && !jsim.minorHColl) {
		jsim.sprinting = false
	}
	// LivingEntity.aiStep: velocity cut-offs (players: horizontal length).
	sendAbil := false
	if jsim.flyAt > 0 && jsim.ticks == jsim.flyAt {
		jsim.flyReq, jsim.flyAt = true, 0
	}
	if jsim.flyReq {
		jsim.flyReq, jsim.flying, sendAbil = false, true, true
	}
	flying := jsim.flying
	v := b.vel
	if flying { // LocalPlayer.aiStep: vertical flight input
		var ya float32
		if keys&0x20 != 0 {
			ya--
		}
		if keys&0x10 != 0 {
			ya++
		}
		if ya != 0 {
			v[1] += float64(ya * jsim.flySpeed * 3)
		}
	}
	if v[0]*v[0]+v[2]*v[2] < 9e-6 {
		v[0], v[2] = 0, 0
	}
	if math.Abs(v[1]) < 0.003 {
		v[1] = 0
	}
	var zza float32
	if forward {
		zza = 1
	}
	if xxa != 0 && zza != 0 { // ClientInput: the move vector is normalised
		xxa, zza = xxa*float32(math.Sqrt2/2), zza*float32(math.Sqrt2/2)
	}
	if keys&0x20 != 0 && !jsim.flying { // crouching: LocalPlayer scales the input by sneaking_speed
		xxa, zza = xxa*0.3, zza*0.3
	}
	zza *= 0.98 // applyInput
	xxa *= 0.98
	if jsim.noJumpDelay > 0 {
		jsim.noJumpDelay--
	}
	yawR := float32(b.yaw) * float32(math.Pi/180)
	sin, cos := float64(float32(math.Sin(float64(yawR)))), float64(float32(math.Cos(float64(yawR))))
	jumped := false
	if jumping && (!flying || b.onGround) {
		if b.onGround && jsim.noJumpDelay == 0 {
			v[1] = math.Max(float64(float32(jsim.jumpAttr)), v[1])
			if jsim.sprinting {
				v[0] += -sin * 0.2
				v[2] += cos * 0.2
			}
			jsim.noJumpDelay = 10
			jumped = true
			jsim.jumps++
		}
	} else {
		jsim.noJumpDelay = 0
	}
	// travelInAir.
	wasOnGround := b.onGround
	friction := float32(1)
	if wasOnGround {
		friction = 0.6
	}
	var speed float32
	if wasOnGround {
		s := jsim.speedAttr
		if jsim.sprinting {
			s *= 1.3
		}
		speed = float32(s)
	} else if flying {
		speed = jsim.flySpeed
		if jsim.sprinting {
			speed *= 2
		}
	} else if jsim.sprinting {
		speed = 0.025999999
	} else {
		speed = 0.02
	}
	if ix, iz := float64(xxa), float64(zza); ix*ix+iz*iz >= 1e-7 {
		if l := ix*ix + iz*iz; l > 1 {
			ix, iz = ix/math.Sqrt(l), iz/math.Sqrt(l)
		}
		mx, mz := ix*float64(speed), iz*float64(speed)
		v[0] += mx*cos - mz*sin
		v[2] += mz*cos + mx*sin
	}
	b.vel = v
	origY := v[1]
	m := b.collideJ(v)
	b.pos = [3]float64{b.pos[0] + m[0], b.pos[1] + m[1], b.pos[2] + m[2]}
	xC, zC := math.Abs(v[0]-m[0]) > 1e-5, math.Abs(v[2]-m[2]) > 1e-5
	jsim.hColl = xC || zC
	vC := v[1] != m[1]
	b.onGround = vC && v[1] < 0 // the local player always updates it
	jsim.minorHColl = false
	if jsim.hColl {
		gx, gz := float64(xxa)*cos-float64(zza)*sin, float64(zza)*cos+float64(xxa)*sin
		al, ml := gx*gx+gz*gz, m[0]*m[0]+m[2]*m[2]
		if al >= 1e-5 && ml >= 1e-5 {
			jsim.minorHColl = math.Acos((gx*m[0]+gz*m[2])/math.Sqrt(al*ml)) < 0.13962634
		}
	}
	if xC {
		v[0] = 0
	}
	if zC {
		v[2] = 0
	}
	if vC {
		v[1] = 0
	}
	// Gravity and friction.
	v[1] = (v[1] - jsim.gravAttr) * float64(float32(0.98))
	f := float64(friction * 0.91)
	v[0] *= f
	v[2] *= f
	if flying {
		v[1] = origY * 0.6 // Player.travel while flying
		if b.onGround {
			jsim.flying, sendAbil = false, true
		}
	}
	b.vel = v
	b.hColl = jsim.hColl
	pos, yaw, pitch, onGround, hColl := b.pos, b.yaw, b.pitch, b.onGround, jsim.hColl
	sendKeys := keys
	if jsim.errTick == jsim.ticks {
		pos[2] += jsim.errDZ
	}
	sendSprint := jsim.sprinting
	if *jlog {
		log.Printf("JT %d pos %.4f %.4f %.4f vel %.4f %.4f %.4f ground %v sprint %v jumped %v hcoll %v speed %.4f fly %v", jsim.ticks, pos[0], pos[1], pos[2], v[0], v[1], v[2], onGround, sendSprint, jumped, hColl, jsim.speedAttr, jsim.flying)
	}
	if jsim.keys == 0 && keys == 0 && !sendSprint && !jsim.flying && math.Abs(v[0])+math.Abs(v[2]) < 1e-4 && onGround {
		jsim.active = false
	}
	b.mu.Unlock()

	if sendAbil {
		w := &wire.Writer{}
		if jsim.flying {
			w.Byte(2)
		} else {
			w.Byte(0)
		}
		b.write(v777.ServerboundPlayPlayerAbilities, w)
	}
	if sendKeys != jsim.sentKeys {
		jsim.sentKeys = sendKeys
		w := &wire.Writer{}
		w.Byte(sendKeys)
		b.write(v777.ServerboundPlayPlayerInput, w)
	}
	if sendSprint != jsim.sentSprint {
		jsim.sentSprint = sendSprint
		if sendSprint {
			b.playerCommand(1)
		} else {
			b.playerCommand(2)
		}
	}
	w := &wire.Writer{}
	w.Float64(pos[0])
	w.Float64(pos[1])
	w.Float64(pos[2])
	w.Float32(yaw)
	w.Float32(pitch)
	var fl byte
	if onGround {
		fl |= 1
	}
	if hColl {
		fl |= 2
	}
	w.Byte(fl)
	b.write(v777.ServerboundPlayMovePlayerPosRot, w)
	b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
	return true
}
