package main

// Holding the use key like a vanilla 26.3 client (Minecraft.handleKeybinds, LocalPlayer,
// LivingEntity): while the key is down and the player is not using an item, startUseItem every
// 4 ticks (rightClickDelay) sends use_item; a food the client can eat (FoodData.needsFood unless
// it is always edible) also starts using it on the client (LocalPlayer.startedUsingItem). Using
// ends on entity event 9 (USE_ITEM_COMPLETE: the client finishes the item itself), on the
// server's living flags (data 8, bit 0 off: stopUsingItem), or when the held item is another
// item or empty; the key released while using sends player_action RELEASE_USE_ITEM. The Java
// client never finishes eating on its own timing: useItemRemaining only reaches 0 on a server.
//
// Steps: usedown (press and hold, counters reset) | useup (release) | useexpect N (the held
// stack shrank N times since usedown; logs USEEXPECT ... true/false).

import (
	"log"
	"strings"
	"time"

	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
)

type useState struct {
	down       bool
	using      bool
	delay      int
	usedItem   int32
	ticksUsing int
	sends      int
	eaten      int
	lastHeld   bslot
}

var buse = &useState{}

// alwaysEdible are consumables a client uses with a full hunger bar.
var alwaysEdible = map[string]bool{"golden_apple": true, "enchanted_golden_apple": true, "chorus_fruit": true,
	"honey_bottle": true, "potion": true, "milk_bucket": true, "suspicious_stew": true, "ominous_bottle": true}

// usedItems start using whatever the hunger (the bot's subset; a crossbow only while not charged,
// which the bot does not track; a bow needs arrows).
var usedItems = map[string]bool{"bow": true, "crossbow": true, "trident": true, "shield": true, "spyglass": true}

// hungerFood are the foods that need hunger (the bot's subset).
var hungerFood = map[string]bool{"cooked_beef": true, "beef": true, "bread": true, "apple": true, "cooked_porkchop": true,
	"carrot": true, "baked_potato": true, "cooked_chicken": true, "cookie": true, "melon_slice": true, "golden_carrot": true}

func init() {
	tickHooks = append(tickHooks, useTick)
	packetHooks = append(packetHooks, usePacket)
	extraSteps["usedown"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		buse.down, buse.sends, buse.eaten = true, 0, 0
		b.mu.Unlock()
		return 0
	}
	extraSteps["useup"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		buse.down = false
		b.mu.Unlock()
		return 0
	}
	extraSteps["useexpect"] = func(b *bot, a []string) time.Duration {
		n := invAtoi(a, 1)
		b.mu.Lock()
		log.Printf("USEEXPECT eaten %d: %v (eaten %d, use_item sent %d, using %v, food %d)", n, buse.eaten == n, buse.eaten, buse.sends, buse.using, b.food)
		b.mu.Unlock()
		return 0
	}
}

func heldName(s bslot) string {
	if s.empty() {
		return ""
	}
	return strings.TrimPrefix(jitem.Name(s.id), "minecraft:")
}

// useTick is one client tick of the use key (before the movement of the tick).
func useTick(b *bot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := buse
	if u.delay > 0 {
		u.delay--
	}
	held := binv.inv[36+binv.held]
	// The held stack shrinking is an item used up (the server's slot update or the client's own
	// finish on event 9).
	if !u.lastHeld.empty() && (held.id == u.lastHeld.id && held.n < u.lastHeld.n || held.empty()) {
		u.eaten++
		log.Printf("USE held %s -> %s (eaten %d, food %d)", u.lastHeld, held, u.eaten, b.food)
	}
	u.lastHeld = held
	if u.using {
		u.ticksUsing++
		switch {
		case held.empty() || held.id != u.usedItem:
			u.using = false
			log.Printf("USE stop: held item changed")
		case !u.down:
			u.using = false
			w := &wire.Writer{}
			w.VarInt(6) // 26.3 RELEASE_USE_ITEM
			w.Position(0, 0, 0)
			w.Byte(0)
			w.VarInt(0)
			go b.write(v777.ServerboundPlayPlayerAction, w)
			log.Printf("USE release after %d ticks", u.ticksUsing)
		}
	}
	if u.down && u.delay == 0 && !u.using {
		u.delay = 4
		w := &wire.Writer{}
		w.VarInt(0)
		w.VarInt(binv.nextSeq())
		w.Float32(b.yaw)
		w.Float32(b.pitch)
		go b.write(v777.ServerboundPlayUseItem, w)
		u.sends++
		n := heldName(held)
		if alwaysEdible[n] || usedItems[n] || hungerFood[n] && b.food < 20 {
			u.using, u.usedItem, u.ticksUsing = true, held.id, 0
		}
		log.Printf("USE use_item #%d (held %s, food %d): using %v", u.sends, held, b.food, u.using)
	}
}

// usePacket reads the bridge's packets about the player's own item use (entity id 1).
func usePacket(b *bot, id int32, body []byte) {
	u := buse
	r := wire.NewReader(body)
	switch id {
	case v777.ClientboundPlayEntityEvent:
		if r.Int32() != 1 || r.Byte() != 9 {
			return
		}
		log.Printf("USE event 9 (complete) using %v", u.using)
		if u.using {
			// completeUsingItem: finishUsingItem shrinks the stack on the client too.
			s := &binv.inv[36+binv.held]
			if !s.empty() {
				s.n--
			}
			u.using = false
		}
	case v777.ClientboundPlaySetEntityData:
		if r.VarInt() != 1 {
			return
		}
		for r.Err == nil {
			idx := r.Byte()
			if idx == 0xff {
				return
			}
			ser := r.VarInt()
			if idx == 8 && ser == 0 {
				serverUsing := r.Byte()&1 != 0
				log.Printf("USE living flags: server using %v (client using %v)", serverUsing, u.using)
				if serverUsing && !u.using {
					held := binv.inv[36+binv.held]
					u.using, u.usedItem, u.ticksUsing = !held.empty(), held.id, 0
				} else if !serverUsing && u.using {
					u.using = false
				}
				continue
			}
			if _, ok := readData(r, ser); !ok {
				return
			}
		}
	}
}
