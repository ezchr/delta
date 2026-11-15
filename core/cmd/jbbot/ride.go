package main

// Driving (the original bridge M7): while the bot is a passenger (set_passengers lists entity 1) it plays
// a Java client in a vehicle: each tick player_input (the keys), move_player_rot (the look),
// move_vehicle (where its simple vehicle physics put the vehicle) and client_tick_end. The
// bridge's move_vehicle (the server's corrections) snaps the vehicle back, as on Java.
//
// Steps: drive SECONDS [jump TICKS] [left|right] (hold forward; jump: hold the jump key TICKS
// ticks, release, and again every 2 s; the release sends player_command start_riding_jump;
// left/right: hold that key too, which turns a boat) | ridestat. A boat goes where it faces (its
// yaw from the server, turned by left/right), a horse where the bot looks.

import (
	"log"
	"math"
	"strconv"
	"time"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
)

// rideState is the bot's vehicle (guarded by b.mu).
// ridingTick is called first in physicsTick (main.go); true: it was a riding tick.
var ridingTick func(b *bot) bool

var bride struct {
	veh         int32 // 0: not riding
	typ         string
	pos, vel    [3]float64
	yaw         float32
	ground      float64 // the y the vehicle drives on (flat test ground / water)
	set         bool
	corrections int
	sent        int
	turn        float32
	jumpTicks   int // hold jump this many ticks per jump (0: no jumps)
	jumpHeld    int
	nextJump    time.Time
	jumps       int
	maxY        float64
	lastLog     time.Time
	start       [3]float64
	driving     bool
	corrOff     []float64
}

func init() {
	ridingTick = rideTick
	extraHandlers = append(extraHandlers, func(b *bot, id int32, r *wire.Reader) bool {
		if id != v777.ClientboundPlayMoveVehicle {
			return false
		}
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		yaw := r.Float32()
		r.Float32()
		if bride.veh != 0 {
			d := math.Sqrt((x-bride.pos[0])*(x-bride.pos[0]) + (y-bride.pos[1])*(y-bride.pos[1]) + (z-bride.pos[2])*(z-bride.pos[2]))
			bride.corrections++
			bride.corrOff = append(bride.corrOff, d)
			if bride.corrections <= 20 || bride.corrections%20 == 0 {
				log.Printf("RIDE correction %d to %.3f %.3f %.3f yaw %.1f (off %.3f)", bride.corrections, x, y, z, yaw, d)
			}
			bride.pos = [3]float64{x, y, z}
			bride.yaw = yaw
			bride.vel[1] = 0
		}
		return true
	})
	extraSteps["drive"] = func(b *bot, a []string) time.Duration {
		secs, _ := strconv.ParseFloat(rideArg(a, 1), 64)
		b.mu.Lock()
		defer b.mu.Unlock()
		if bride.veh == 0 {
			log.Printf("drive: not riding")
			return 0
		}
		b.keys = 1
		bride.driving = true
		bride.jumpTicks = 0
		for i := 2; i < len(a); i++ {
			switch a[i] {
			case "jump":
				bride.jumpTicks, _ = strconv.Atoi(rideArg(a, i+1))
				bride.nextJump = time.Now().Add(500 * time.Millisecond)
				i++
			case "left":
				b.keys |= 0x04
			case "right":
				b.keys |= 0x08
			}
		}
		bride.start = bride.pos
		log.Printf("RIDE drive %s for %.1f s from %.3f %.3f %.3f", bride.typ, secs, bride.pos[0], bride.pos[1], bride.pos[2])
		return time.Duration(secs * float64(time.Second))
	}
	extraSteps["ridestat"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		defer b.mu.Unlock()
		maxOff := 0.0
		for _, o := range bride.corrOff {
			maxOff = max(maxOff, o)
		}
		d := math.Hypot(bride.pos[0]-bride.start[0], bride.pos[2]-bride.start[2])
		log.Printf("RIDESTAT %s veh %d at %.3f %.3f %.3f: moved %.2f blocks, corrections %d (max off %.3f), move_vehicle sent %d, jumps %d, max height %.2f",
			bride.typ, bride.veh, bride.pos[0], bride.pos[1], bride.pos[2], d, bride.corrections, maxOff, bride.sent, bride.jumps, bride.maxY-bride.ground)
		return 0
	}
}

func rideArg(a []string, i int) string {
	if i < len(a) {
		return a[i]
	}
	return ""
}

// ridePassengers notes the bot getting on or off a vehicle (b.mu held).
func ridePassengers(veh int32, ps []int32) {
	on := false
	for _, p := range ps {
		if p == 1 {
			on = true
		}
	}
	switch {
	case on && bride.veh != veh:
		bride.veh, bride.set, bride.corrections, bride.sent, bride.jumps, bride.corrOff = veh, false, 0, 0, 0, nil
		if e := es.byID[veh]; e != nil {
			bride.typ, bride.pos, bride.yaw = e.typ, e.pos, e.yaw
			bride.ground, bride.maxY = e.pos[1], e.pos[1]
		}
		log.Printf("RIDE on %d %s at %.3f %.3f %.3f", veh, bride.typ, bride.pos[0], bride.pos[1], bride.pos[2])
	case !on && bride.veh == veh:
		log.Printf("RIDE off %d %s", veh, bride.typ)
		bride.veh, bride.driving = 0, false
	}
}

// rideTick is the bot's tick while riding; false: not riding (normal physics).
func rideTick(b *bot) bool {
	b.mu.Lock()
	if bride.veh == 0 {
		b.mu.Unlock()
		return false
	}
	e := es.byID[bride.veh]
	keys := b.keys
	yaw, pitch := b.yaw, b.pitch
	var cmd int32 = -1
	var jumpPower int32
	if bride.driving && e != nil {
		horse := bride.typ != "" && (bride.typ == "horse" || bride.typ == "donkey" || bride.typ == "mule" ||
			bride.typ == "skeleton_horse" || bride.typ == "zombie_horse" || bride.typ == "camel")
		if horse {
			bride.yaw = yaw // a ridden horse faces where the rider looks
		} else {
			// A boat turns with left/right (Java Boat.controlBoat: 1 degree a tick, 0.9 kept).
			if keys&0x04 != 0 {
				bride.turn--
			}
			if keys&0x08 != 0 {
				bride.turn++
			}
			bride.yaw += bride.turn
			bride.turn *= 0.9
		}
		r := float64(bride.yaw) * math.Pi / 180
		dir := [2]float64{-math.Sin(r), math.Cos(r)}
		fwd := 0.0
		if keys&1 != 0 {
			fwd = 1
		}
		if horse {
			speed := 0.225
			if v, ok := e.attrs["movement_speed"]; ok {
				speed = v
			}
			// Ground travel: accelerate by the speed, keep 0.546 of the velocity a tick.
			for i, ax := range [2]int{0, 2} {
				bride.vel[ax] = bride.vel[ax]*0.546 + dir[i]*fwd*speed*0.98
			}
			// Jumps: hold the key, release; the horse jumps on release with the strength.
			if bride.jumpTicks > 0 && time.Now().After(bride.nextJump) {
				if bride.jumpHeld < bride.jumpTicks {
					bride.jumpHeld++
					keys |= 0x10
				} else {
					bride.jumpHeld = 0
					bride.nextJump = time.Now().Add(2 * time.Second)
					if bride.pos[1] <= bride.ground+0.01 {
						js := 0.7
						if v, ok := e.attrs["jump_strength"]; ok {
							js = v
						}
						bride.vel[1] = js
						bride.jumps++
						cmd, jumpPower = 3, 100
					}
				}
			}
			bride.pos[1] += bride.vel[1]
			if bride.pos[1] <= bride.ground {
				bride.pos[1], bride.vel[1] = bride.ground, 0
			} else {
				bride.vel[1] = (bride.vel[1] - 0.08) * 0.98
			}
		} else {
			// Boat on water: 0.04 forward a tick, 0.9 kept.
			for i, ax := range [2]int{0, 2} {
				bride.vel[ax] = bride.vel[ax]*0.9 + dir[i]*fwd*0.04
			}
		}
		bride.pos[0] += bride.vel[0]
		bride.pos[2] += bride.vel[2]
		bride.maxY = max(bride.maxY, bride.pos[1])
		bride.set = true
	}
	pos, vyaw, set := bride.pos, bride.yaw, bride.set
	if set {
		bride.sent++
	}
	if time.Since(bride.lastLog) > time.Second && bride.driving {
		bride.lastLog = time.Now()
		log.Printf("RIDE at %.3f %.3f %.3f vel %.3f %.3f %.3f corrections %d", pos[0], pos[1], pos[2], bride.vel[0], bride.vel[1], bride.vel[2], bride.corrections)
	}
	b.mu.Unlock()

	w := &wire.Writer{}
	w.Byte(keys)
	b.write(v777.ServerboundPlayPlayerInput, w)
	w = &wire.Writer{}
	w.Float32(yaw)
	w.Float32(pitch)
	w.Byte(0)
	b.write(v777.ServerboundPlayMovePlayerRot, w)
	if set {
		w = &wire.Writer{}
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		w.Float32(vyaw)
		w.Float32(0)
		w.Bool(true)
		b.write(v777.ServerboundPlayMoveVehicle, w)
	}
	if cmd >= 0 {
		w = &wire.Writer{}
		w.VarInt(1)
		w.VarInt(cmd)
		w.VarInt(jumpPower)
		b.write(v777.ServerboundPlayPlayerCommand, w)
	}
	b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
	return true
}
