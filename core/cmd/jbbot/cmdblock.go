package main

import (
	"log"
	"strings"
	"time"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
)

// Command block checks (2026-11-10): the bot follows what a 26.3 client decides about the command
// block edit screen. It keeps its op level (entity_event 24-28 on itself) and instabuild
// (player_abilities 0x08); step "cbopen X Y Z" is the client's use: when it would open the screen
// (creative + level >= 2) it logs CB GUI OPEN, sends use_item_on and logs CB GUI FILLED when
// block_entity_data for that block comes while it is open (updateGui: the controls turn on) or
// CB GUI CLOSED on container_close. "setcb X Y Z MODE FLAGS COMMAND..." sends set_command_block
// (MODE impulse|repeating|chain, FLAGS 1 track output + 2 conditional + 4 always active), as the
// screen's Done does. "cbstate" logs the op level and abilities.

var bcb = struct {
	op         int
	instabuild bool
	gui        bool
	guiPos     [3]int
}{op: -1}

func init() {
	packetHooks = append(packetHooks, func(b *bot, id int32, body []byte) {
		r := wire.NewReader(body)
		switch id {
		case v777.ClientboundPlayEntityEvent:
			if r.Int32() == 1 {
				if ev := r.Byte(); ev >= 24 && ev <= 28 {
					bcb.op = int(ev) - 24
					log.Printf("CB OP LEVEL %d", bcb.op)
				}
			}
		case v777.ClientboundPlayPlayerAbilities:
			bcb.instabuild = r.Byte()&0x08 != 0
		case v777.ClientboundPlayBlockEntityData:
			x, y, z := r.Position()
			t := r.VarInt()
			if bcb.gui && [3]int{x, y, z} == bcb.guiPos && beTypeName(t) == "command_block" {
				log.Printf("CB GUI FILLED %d %d %d %s", x, y, z, readNBT(r))
			}
		case v777.ClientboundPlayContainerClose:
			if bcb.gui {
				log.Printf("CB GUI CLOSED by the server")
				bcb.gui = false
			}
		}
	})
	extraSteps["cbstate"] = func(b *bot, a []string) time.Duration {
		log.Printf("CB STATE op %d instabuild %v", bcb.op, bcb.instabuild)
		return 0
	}
	extraSteps["cbopen"] = func(b *bot, a []string) time.Duration {
		x, y, z := invAtoi(a, 1), invAtoi(a, 2), invAtoi(a, 3)
		if bcb.instabuild && bcb.op >= 2 {
			bcb.gui, bcb.guiPos = true, [3]int{x, y, z}
			log.Printf("CB GUI OPEN %d %d %d (client: creative, op %d)", x, y, z, bcb.op)
		} else {
			bcb.gui = false
			log.Printf("CB GUI NOT OPENED (instabuild %v, op %d)", bcb.instabuild, bcb.op)
		}
		return extraSteps["useon"](b, []string{"useon", a[1], a[2], a[3], "1"}) + 700*time.Millisecond
	}
	extraSteps["cbclose"] = func(b *bot, a []string) time.Duration {
		bcb.gui = false
		return 0
	}
	extraSteps["setcb"] = func(b *bot, a []string) time.Duration {
		if len(a) < 7 {
			log.Printf("setcb X Y Z MODE FLAGS COMMAND")
			return 0
		}
		mode, ok := map[string]int32{"chain": 0, "sequence": 0, "repeating": 1, "auto": 1, "impulse": 2, "redstone": 2}[a[4]]
		if !ok {
			log.Printf("setcb: unknown mode %s", a[4])
			return 0
		}
		w := &wire.Writer{}
		w.Position(invAtoi(a, 1), invAtoi(a, 2), invAtoi(a, 3))
		w.String(strings.Join(a[6:], " "))
		w.VarInt(mode)
		w.Byte(byte(invAtoi(a, 5)))
		b.write(v777.ServerboundPlaySetCommandBlock, w)
		bcb.gui = false // Done closes the screen
		log.Printf("CB SET %s", strings.Join(a[1:], " "))
		return 1500 * time.Millisecond
	}
}
