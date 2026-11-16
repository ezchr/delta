package main

// Sleeping like a 26.3 client. The player's own set_entity_data with sleeping_pos (index 14)
// puts the client at the bed (LivingEntity.onSyncedDataUpdated -> setPosToBed: x+0.5,
// y+sleepHeight+0.125, z+0.5, sleepHeight 0.5625 for a bed) if the block there is a bed in its
// world; while sleeping (Player.isImmobile) it takes no move input, falls onto the bed with a
// 0.2-wide box and sends its position as LocalPlayer.sendPosition does. Animate WAKE_UP (0 on
// 26.3) for the player itself ends it (stopSleepInBed; the stand-up spot is not modelled: the
// bot stands on the bed), so does entity data without a sleeping_pos.
//
// Steps: wake (player_command STOP_SLEEPING: the Leave Bed button) | sleepstat (logs the SLEEP
// state: sleeping, bed, position, teleports while asleep).

import (
	"fmt"
	"log"
	"math"
	"time"

	jchunk "github.com/ezchr/go-mcjava/chunk"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

// bedState says which Java states are beds (blocks.json names ending in _bed).
var bedState []bool

// sleepTick is called in physicsTick (main.go) after jsimTick; true: it was a sleeping tick.
var sleepTick func(b *bot) bool

var bsleep struct {
	sleeping  bool
	bed       [3]int
	ticks     int
	teleports int // player_position packets while sleeping
	sinceSent int
	lastSent  [3]float64
	poses     []string
}

func init() {
	packetHooks = append(packetHooks, sleepPacket)
	sleepTick = sleepPhysics
	extraSteps["wake"] = func(b *bot, a []string) time.Duration {
		b.playerCommand(0)
		log.Printf("SLEEP wake: player_command STOP_SLEEPING sent")
		return 1500 * time.Millisecond
	}
	extraSteps["sleepstat"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		defer b.mu.Unlock()
		log.Printf("SLEEP stat: sleeping %v bed %v pos %.4f %.4f %.4f ground %v ticks %d teleports while asleep %d poses %v",
			bsleep.sleeping, bsleep.bed, b.pos[0], b.pos[1], b.pos[2], b.onGround, bsleep.ticks, bsleep.teleports, bsleep.poses)
		return 0
	}
}

func (b *bot) isBed(x, y, z int) bool {
	col := b.chunks[[2]int32{int32(x >> 4), int32(z >> 4)}]
	if col == nil {
		return false
	}
	i := (y - b.minY) >> 4
	if i < 0 || i >= len(col.Sections) {
		return false
	}
	st := col.Sections[i].Blocks[jchunk.BlockIndex(x&15, y&15, z&15)]
	return int(st) < len(bedState) && bedState[st]
}

// sleepPacket reads the bridge's packets about the player's own sleep (entity id 1). b.mu held.
func sleepPacket(b *bot, id int32, body []byte) {
	r := wire.NewReader(body)
	switch id {
	case v777.ClientboundPlaySetEntityData:
		if r.VarInt() != 1 {
			return
		}
		var parts []string
		for r.Err == nil {
			idx := r.Byte()
			if idx == 0xff {
				break
			}
			ser := r.VarInt()
			if idx == 6 && ser == 20 {
				pose := r.VarInt()
				parts = append(parts, fmt.Sprintf("pose=%d", pose))
				bsleep.poses = append(bsleep.poses, fmt.Sprint(pose))
				continue
			}
			if idx == 14 && ser == 11 {
				if !r.Bool() {
					parts = append(parts, "sleeping_pos=none")
					if bsleep.sleeping {
						bsleep.sleeping = false
						log.Printf("SLEEP ended by entity data (pos %.4f %.4f %.4f)", b.pos[0], b.pos[1], b.pos[2])
					}
					continue
				}
				x, y, z := r.Position()
				parts = append(parts, fmt.Sprintf("sleeping_pos=%d,%d,%d", x, y, z))
				bed := b.isBed(x, y, z)
				log.Printf("SLEEP sleeping_pos %d %d %d (a bed in the bot's world: %v)", x, y, z, bed)
				if bed {
					// setPosToBed
					b.pos = [3]float64{float64(x) + 0.5, float64(y) + 0.5625 + 0.125, float64(z) + 0.5}
					b.vel = [3]float64{}
				}
				bsleep.sleeping, bsleep.bed, bsleep.ticks, bsleep.teleports = true, [3]int{x, y, z}, 0, 0
				continue
			}
			v, ok := readData(r, ser)
			if !ok {
				return
			}
			parts = append(parts, fmt.Sprintf("%d=%s", idx, v))
		}
		log.Printf("SELF data %v", parts)
	case v777.ClientboundPlayAnimate:
		if r.VarInt() != 1 {
			return
		}
		action := r.Byte()
		log.Printf("SELF animate %d", action)
		if action == 0 && bsleep.sleeping { // 26.3 WAKE_UP: stopSleepInBed on the client
			bsleep.sleeping = false
			bed := bsleep.bed
			b.pos = [3]float64{float64(bed[0]) + 0.5, float64(bed[1]) + 0.5625, float64(bed[2]) + 0.5}
			log.Printf("SLEEP woken by animate, standing at %.4f %.4f %.4f", b.pos[0], b.pos[1], b.pos[2])
		}
	case v777.ClientboundPlayPlayerPosition:
		if bsleep.sleeping {
			bsleep.teleports++
			log.Printf("SLEEP teleport while asleep (%d)", bsleep.teleports)
		}
	}
}

// sleepPhysics is a client tick while sleeping: no input, gravity onto the bed (0.2 wide box).
func sleepPhysics(b *bot) bool {
	b.mu.Lock()
	if !bsleep.sleeping {
		b.mu.Unlock()
		return false
	}
	bsleep.ticks++
	// Gravity: travel with no input (the box is 0.2 wide: only the block under the centre counts).
	ny := b.pos[1] + b.vel[1]
	bx, bz := int(math.Floor(b.pos[0])), int(math.Floor(b.pos[2]))
	by := int(math.Floor(ny))
	top := math.Inf(-1)
	for y := int(math.Floor(b.pos[1])); y >= by; y-- {
		switch {
		case b.isBed(bx, y, bz):
			top = float64(y) + 0.5625
		case b.solid(bx, y, bz):
			top = float64(y) + 1
		default:
			continue
		}
		break
	}
	b.onGround = false
	if b.vel[1] <= 0 && ny <= top && b.pos[1] >= top-1e-7 {
		ny, b.vel[1], b.onGround = top, 0, true
	}
	b.pos[1] = ny
	if !b.onGround {
		b.vel[1] = (b.vel[1] - 0.08) * 0.98
	}
	pos, yaw, onGround := b.pos, b.yaw, b.onGround
	moved := pos != bsleep.lastSent
	bsleep.sinceSent++
	send := moved || bsleep.sinceSent >= 20
	if send {
		bsleep.lastSent, bsleep.sinceSent = pos, 0
	}
	if bsleep.ticks%20 == 1 && *jlog {
		log.Printf("SLEEP tick %d pos %.4f %.4f %.4f ground %v", bsleep.ticks, pos[0], pos[1], pos[2], onGround)
	}
	b.mu.Unlock()
	if send {
		w := &wire.Writer{}
		w.Float64(pos[0])
		w.Float64(pos[1])
		w.Float64(pos[2])
		w.Float32(yaw)
		w.Float32(0)
		var fl byte
		if onGround {
			fl |= 1
		}
		w.Byte(fl)
		b.write(v777.ServerboundPlayMovePlayerPosRot, w)
	}
	b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
	return true
}
