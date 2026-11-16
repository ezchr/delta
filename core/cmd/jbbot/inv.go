package main

// Inventory, window and block-interaction steps of jbbot (the original bridge milestone 3). The bot keeps
// the windows the bridge shows (container_set_content/set_slot, set_cursor_item,
// set_player_inventory, open_screen), applies its own clicks the way a vanilla client does and
// sends container_click with the changed slots and the cursor, like a client.
//
// Steps: binv | click SLOT BUTTON MODE | drag BUTTON SLOT... | close | hold N | creative SLOT ITEM
// COUNT | expectslot SLOT ITEM COUNT | useon X Y Z FACE [CX CY CZ] | useair | release | digstart X Y Z FACE |
// digend X Y Z FACE | digabort X Y Z FACE | drop | dropall | swap | form BUTTON | sign X Y Z TEXT |
// pitch DEG | pick X Y Z.

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

// extraSteps and extraHandlers are the script steps and packet handlers of this file (main.go
// tries them for steps and packets it does not know).
var (
	extraSteps    = map[string]func(b *bot, args []string) time.Duration{}
	extraHandlers []func(b *bot, id int32, r *wire.Reader) bool
)

type bslot struct{ id, n int32 }

func (s bslot) empty() bool { return s.n <= 0 || s.id == 0 }

func (s bslot) String() string {
	if s.empty() {
		return "-"
	}
	return fmt.Sprintf("%s x%d", strings.TrimPrefix(jitem.Name(s.id), "minecraft:"), s.n)
}

// invState is what the bridge showed the bot.
type invState struct {
	win      int32
	menu     int32
	title    string
	slots    []bslot // the open window (window 0: the 46 inventory slots)
	inv      [46]bslot
	carried  bslot
	held     int
	stateID  int32
	seq      int32
	acks     int
	cracks   int
	dialogs  int
	formID   int32
	signOpen string
}

var binv = &invState{}

func init() {
	extraHandlers = append(extraHandlers, invHandle)
	extraSteps["inv"] = func(b *bot, a []string) time.Duration { binv.dump(); return 0 }
	extraSteps["click"] = invStepClick
	// qclick: a click without waiting for the answer (clicks faster than the server answers).
	extraSteps["qclick"] = func(b *bot, a []string) time.Duration { invStepClick(b, a); return 60 * time.Millisecond }
	// burst S1 S2 ...: quick moves (shift-clicks) of these slots sent back to back, as a client's
	// shift-double-click sends them (one per slot holding the item, in the same frame).
	extraSteps["burst"] = func(b *bot, a []string) time.Duration {
		for _, x := range a[1:] {
			invStepClick(b, []string{"click", x, "0", "1"})
		}
		return 500 * time.Millisecond
	}
	extraSteps["drag"] = invStepDrag
	extraSteps["close"] = func(b *bot, a []string) time.Duration {
		w := &wire.Writer{}
		w.VarInt(binv.win)
		b.write(v777.ServerboundPlayContainerClose, w)
		b.mu.Lock()
		binv.win, binv.slots = 0, binv.inv[:]
		b.mu.Unlock()
		return 500 * time.Millisecond
	}
	extraSteps["hold"] = func(b *bot, a []string) time.Duration {
		n, _ := strconv.Atoi(invArg(a, 1))
		w := &wire.Writer{}
		w.Int16(int16(n))
		b.write(v777.ServerboundPlaySetCarriedItem, w)
		b.mu.Lock()
		binv.held = n
		b.mu.Unlock()
		return 300 * time.Millisecond
	}
	extraSteps["creative"] = func(b *bot, a []string) time.Duration {
		slot, _ := strconv.Atoi(invArg(a, 1))
		id, _ := jitem.ByName("minecraft:" + invArg(a, 2))
		n, _ := strconv.Atoi(invArg(a, 3))
		w := &wire.Writer{}
		w.Int16(int16(slot))
		var s jitem.Stack
		if n > 0 && id > 0 {
			s.ID, s.Count = id, int32(n)
		}
		if s.Empty() {
			jitem.WriteEmpty(w)
		} else {
			s.EncodeUntrusted(w)
		}
		b.write(v777.ServerboundPlaySetCreativeModeSlot, w)
		b.mu.Lock()
		if slot >= 0 && slot < 46 {
			binv.inv[slot] = bslot{s.ID, s.Count}
		}
		b.mu.Unlock()
		return 500 * time.Millisecond
	}
	extraSteps["expectslot"] = func(b *bot, a []string) time.Duration {
		slot, _ := strconv.Atoi(invArg(a, 1))
		want := invArg(a, 2)
		n, _ := strconv.Atoi(invArg(a, 3))
		b.mu.Lock()
		defer b.mu.Unlock()
		var got bslot
		if slot == -1 {
			got = binv.carried
		} else if slot < len(binv.slots) {
			got = binv.slots[slot]
		}
		ok := (want == "-" && got.empty()) || (strings.TrimPrefix(jitem.Name(got.id), "minecraft:") == want && int(got.n) == n)
		log.Printf("EXPECTSLOT %d %s x%d: %v (have %s)", slot, want, n, ok, got)
		return 0
	}
	extraSteps["useon"] = func(b *bot, a []string) time.Duration {
		x, y, z, f := invAtoi(a, 1), invAtoi(a, 2), invAtoi(a, 3), invAtoi(a, 4)
		w := &wire.Writer{}
		w.VarInt(0)
		w.Position(x, y, z)
		w.VarInt(int32(f))
		cx, cy, cz := float32(0.5), float32(0.5), float32(0.5)
		switch f {
		case 0:
			cy = 0
		case 1:
			cy = 1
		case 2:
			cz = 0
		case 3:
			cz = 1
		case 4:
			cx = 0
		case 5:
			cx = 1
		}
		if len(a) > 7 { // the cursor position on the block
			v, _ := strconv.ParseFloat(a[5], 32)
			cx = float32(v)
			v, _ = strconv.ParseFloat(a[6], 32)
			cy = float32(v)
			v, _ = strconv.ParseFloat(a[7], 32)
			cz = float32(v)
		}
		w.Float32(cx)
		w.Float32(cy)
		w.Float32(cz)
		w.Bool(false)
		w.Bool(false)
		w.VarInt(binv.nextSeq())
		b.lookAtPoint(float64(x)+float64(cx), float64(y)+float64(cy), float64(z)+float64(cz))
		go func() { time.Sleep(150 * time.Millisecond); b.write(v777.ServerboundPlayUseItemOn, w) }()
		return 800 * time.Millisecond
	}
	extraSteps["useair"] = func(b *bot, a []string) time.Duration {
		w := &wire.Writer{}
		w.VarInt(0)
		w.VarInt(binv.nextSeq())
		b.mu.Lock()
		w.Float32(b.yaw)
		w.Float32(b.pitch)
		b.mu.Unlock()
		b.write(v777.ServerboundPlayUseItem, w)
		return 300 * time.Millisecond
	}
	action := func(act int32) func(b *bot, a []string) time.Duration {
		return func(b *bot, a []string) time.Duration {
			w := &wire.Writer{}
			w.VarInt(act)
			w.Position(invAtoi(a, 1), invAtoi(a, 2), invAtoi(a, 3))
			w.Byte(byte(invAtoi(a, 4)))
			w.VarInt(binv.nextSeq())
			if act <= 3 {
				b.lookAtPoint(float64(invAtoi(a, 1))+0.5, float64(invAtoi(a, 2))+0.5, float64(invAtoi(a, 3))+0.5)
				go func() { time.Sleep(150 * time.Millisecond); b.write(v777.ServerboundPlayPlayerAction, w) }()
				return 300 * time.Millisecond
			}
			b.write(v777.ServerboundPlayPlayerAction, w)
			if act == 4 || act == 5 { // the client takes the dropped items out of the hand itself
				b.mu.Lock()
				s := &binv.inv[36+binv.held]
				if act == 5 {
					s.n--
				} else {
					s.n = 0
				}
				b.mu.Unlock()
			}
			return 300 * time.Millisecond
		}
	}
	// 26.3 numbering (START, CHANGE_DESTROY_DIRECTION, ABORT, STOP, DROP_ALL, DROP, RELEASE, SWAP).
	extraSteps["digstart"] = action(0)
	extraSteps["digabort"] = action(2)
	extraSteps["digend"] = action(3)
	extraSteps["drop"] = action(5)
	extraSteps["dropall"] = action(4)
	extraSteps["release"] = action(6)
	extraSteps["swap"] = action(7)
	extraSteps["pitch"] = func(b *bot, a []string) time.Duration {
		v, _ := strconv.ParseFloat(invArg(a, 1), 64)
		b.mu.Lock()
		b.pitch = float32(v)
		b.mu.Unlock()
		return 0
	}
	extraSteps["pick"] = func(b *bot, a []string) time.Duration {
		w := &wire.Writer{}
		w.Position(invAtoi(a, 1), invAtoi(a, 2), invAtoi(a, 3))
		w.Bool(false)
		b.write(v777.ServerboundPlayPickItemFromBlock, w)
		return 500 * time.Millisecond
	}
	extraSteps["form"] = func(b *bot, a []string) time.Duration {
		btn, _ := strconv.Atoi(invArg(a, 1))
		pl := &wire.Writer{}
		pl.Byte(text.TagCompound)
		text.IntTag(pl, "f", binv.formID)
		text.IntTag(pl, "b", int32(btn))
		if len(a) > 2 { // custom form values: e<i>=<string>
			for _, kv := range a[2:] {
				k, v, _ := strings.Cut(kv, "=")
				text.StringTag(pl, k, v)
			}
		}
		pl.Byte(text.TagEnd)
		w := &wire.Writer{}
		w.String("delta:form")
		w.VarInt(int32(len(pl.B))) // the payload is length-prefixed, as a Java client sends it
		w.Raw(pl.B)
		b.write(v777.ServerboundPlayCustomClickAction, w)
		return 800 * time.Millisecond
	}
	// Station steps (the original bridge stations): button N (container_button_click: stonecutter recipe,
	// loom pattern, enchantment option), rename TEXT (anvil), trade N (select_trade).
	extraSteps["button"] = func(b *bot, a []string) time.Duration {
		w := &wire.Writer{}
		w.VarInt(binv.win)
		w.VarInt(int32(invAtoi(a, 1)))
		b.write(v777.ServerboundPlayContainerButtonClick, w)
		return 500 * time.Millisecond
	}
	extraSteps["rename"] = func(b *bot, a []string) time.Duration {
		w := &wire.Writer{}
		w.String(strings.Join(a[1:], " "))
		b.write(v777.ServerboundPlayRenameItem, w)
		return 500 * time.Millisecond
	}
	extraSteps["beacon"] = func(b *bot, a []string) time.Duration { // beacon PRIMARY SECONDARY (Java effect ids, -1 none)
		w := &wire.Writer{}
		for _, i := range []int{1, 2} {
			v := invAtoi(a, i)
			w.Bool(v >= 0)
			if v >= 0 {
				w.VarInt(int32(v))
			}
		}
		b.write(v777.ServerboundPlaySetBeacon, w)
		return 700 * time.Millisecond
	}
	extraSteps["pcmd"] = func(b *bot, a []string) time.Duration { // player_command ACTION (5: open vehicle inventory)
		b.playerCommand(int32(invAtoi(a, 1)))
		return 700 * time.Millisecond
	}
	extraSteps["trade"] = func(b *bot, a []string) time.Duration {
		w := &wire.Writer{}
		w.VarInt(int32(invAtoi(a, 1)))
		b.write(v777.ServerboundPlaySelectTrade, w)
		return 700 * time.Millisecond
	}
	extraSteps["sign"] = func(b *bot, a []string) time.Duration {
		w := &wire.Writer{}
		w.Position(invAtoi(a, 1), invAtoi(a, 2), invAtoi(a, 3))
		lines := [4]string{strings.Join(a[4:], " ")}
		for _, l := range lines {
			w.String(l)
		}
		w.VarInt(1) // 26.3: the side after the lines (1 front)
		b.write(v777.ServerboundPlaySignUpdate, w)
		return 700 * time.Millisecond
	}
}

func invArg(a []string, i int) string {
	if i < len(a) {
		return a[i]
	}
	return ""
}

func invAtoi(a []string, i int) int {
	v, _ := strconv.Atoi(invArg(a, i))
	return v
}

// lookAtPoint turns the bot to a point (the client looks at what it clicks).
func (b *bot) lookAtPoint(x, y, z float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	dx, dy, dz := x-b.pos[0], y-(b.pos[1]+1.62), z-b.pos[2]
	b.yaw = float32(math.Atan2(-dx, dz) * 180 / math.Pi)
	b.pitch = float32(-math.Atan2(dy, math.Hypot(dx, dz)) * 180 / math.Pi)
}

func (s *invState) nextSeq() int32 { s.seq++; return s.seq }

func (s *invState) dump() {
	var parts []string
	for i, sl := range s.slots {
		if !sl.empty() {
			parts = append(parts, fmt.Sprintf("%d:%s", i, sl))
		}
	}
	log.Printf("INV window %d (%q, %d slots) held %d carried %s: %s", s.win, s.title, len(s.slots), s.held, s.carried, strings.Join(parts, " "))
}

func invReadSlot(r *wire.Reader) bslot {
	var st jitem.Stack
	st.Decode(r)
	if st.Count > 0 && (st.Has(jitem.CompCustomName) || st.Has(jitem.CompEnchantments) || st.Has(jitem.CompDamage) ||
		st.Has(jitem.CompLore) || st.Has(jitem.CompPotionContents) || st.Has(jitem.CompStoredEnchantments) || st.Has(jitem.CompItemName) ||
		len(st.Raw) > 0) {
		var raw []string
		for _, c := range st.Raw {
			raw = append(raw, fmt.Sprintf("%d:%x", c.Type, c.Data))
		}
		log.Printf("ITEM %s x%d name=%q itemname=%q lore=%d ench=%v stored=%v damage=%d/%d potion=%v raw=%v", jitem.Name(st.ID), st.Count,
			st.CustomName.Text, st.ItemName.Text, len(st.Lore), st.Enchantments, st.StoredEnchantments, st.Damage, st.MaxDamage, st.PotionContents.Potion, raw)
	}
	return bslot{st.ID, st.Count}
}

// invHandle reads the bridge's window packets.
func invHandle(b *bot, id int32, r *wire.Reader) bool {
	s := binv
	if s.slots == nil {
		s.slots = s.inv[:]
	}
	switch id {
	case v777.ClientboundPlayContainerSetContent:
		win := r.VarInt()
		s.stateID = r.VarInt()
		n := int(r.VarInt())
		items := make([]bslot, n)
		for i := range items {
			items[i] = invReadSlot(r)
		}
		s.carried = invReadSlot(r)
		if win == 0 {
			copy(s.inv[:], items)
			if s.win == 0 {
				s.slots = s.inv[:]
			}
		} else if win == s.win {
			s.slots = items
			// The inventory part is the player's.
			if len(items) >= 36 {
				copy(s.inv[9:45], items[len(items)-36:])
			}
		}
		log.Printf("set content window %d (%d slots)", win, n)
	case v777.ClientboundPlayContainerSetSlot:
		win := r.VarInt()
		s.stateID = r.VarInt()
		slot := int(r.Int16())
		it := invReadSlot(r)
		if win == -1 && slot == -1 {
			s.carried = it
			break
		}
		if win == 0 && slot >= 0 && slot < 46 {
			s.inv[slot] = it
		}
		if win == s.win && slot >= 0 && slot < len(s.slots) {
			s.slots[slot] = it
			if win != 0 && slot >= len(s.slots)-36 {
				s.inv[9+slot-(len(s.slots)-36)] = it
			}
		}
		log.Printf("set slot window %d slot %d = %s", win, slot, it)
	case v777.ClientboundPlaySetCursorItem:
		s.carried = invReadSlot(r)
		log.Printf("cursor = %s", s.carried)
	case v777.ClientboundPlaySetPlayerInventory:
		i := int(r.VarInt())
		it := invReadSlot(r)
		js := -1
		switch {
		case i < 9:
			js = 36 + i
		case i < 36:
			js = i
		case i < 40:
			js = 8 - (i - 36)
		case i == 40:
			js = 45
		}
		if js >= 0 {
			s.inv[js] = it
			if s.win != 0 && js >= 9 && js < 45 && len(s.slots) >= 36 {
				s.slots[len(s.slots)-36+js-9] = it
			}
		}
		log.Printf("player inventory %d = %s", i, it)
	case v777.ClientboundPlaySetHeldSlot:
		s.held = int(r.VarInt())
		log.Printf("held slot %d", s.held)
	case v777.ClientboundPlayOpenScreen:
		s.win = r.VarInt()
		s.menu = r.VarInt()
		s.title = printable(r.Rest())
		s.slots = nil
		log.Printf("OPEN SCREEN %d menu %d %q", s.win, s.menu, s.title)
	case v777.ClientboundPlayContainerClose:
		w := r.VarInt()
		log.Printf("CLOSE SCREEN %d", w)
		s.win, s.slots = 0, s.inv[:]
	case v777.ClientboundPlayContainerSetData:
		w := r.VarInt()
		k, v := r.Int16(), r.Int16()
		log.Printf("window data %d: %d = %d", w, k, v)
	case v777.ClientboundPlayBlockChangedAck:
		s.acks++
		log.Printf("block ack %d", r.VarInt())
	case v777.ClientboundPlayBlockDestruction:
		s.cracks++
		eid := r.VarInt()
		x, y, z := r.Position()
		log.Printf("block destruction %d at %d %d %d stage %d", eid, x, y, z, r.Byte())
	case v777.ClientboundPlayShowDialog:
		s.dialogs++
		body := r.Rest()
		// The form id is in the first action's payload: find the "f" int tag.
		if i := strings.Index(string(body), "\x03\x00\x01f"); i >= 0 && i+8 <= len(body) {
			rr := wire.NewReader(body[i+4:])
			s.formID = rr.Int32()
		}
		log.Printf("DIALOG form %d: %s", s.formID, printable(body))
	case v777.ClientboundPlayBlockEvent:
		x, y, z := r.Position()
		a, prm := r.Byte(), r.Byte()
		log.Printf("BLOCK EVENT %d %d %d action %d param %d block %d", x, y, z, a, prm, r.VarInt())
	case v777.ClientboundPlaySetExperience:
		bar := r.Float32()
		log.Printf("XP level %d bar %.2f", r.VarInt(), bar)
	case v777.ClientboundPlayMountScreenOpen:
		s.win = r.VarInt()
		cols := r.VarInt()
		eid := r.Int32()
		s.menu, s.title, s.slots = -2, "mount", nil
		log.Printf("MOUNT SCREEN %d columns %d entity %d", s.win, cols, eid)
	case v777.ClientboundPlayMerchantOffers:
		win := r.VarInt()
		n := int(r.VarInt())
		var offers []string
		cost := func() string {
			id, c := r.VarInt(), r.VarInt()
			for k := r.VarInt(); k > 0; k-- { // component predicates: not sent by the bridge
				r.VarInt()
			}
			return fmt.Sprintf("%s x%d", strings.TrimPrefix(jitem.Name(id), "minecraft:"), c)
		}
		for i := 0; i < n && r.Err == nil; i++ {
			a := cost()
			out := invReadSlot(r)
			b2 := ""
			if r.Bool() {
				b2 = " + " + cost()
			}
			disabled := r.Bool()
			uses, maxUses, xp := r.Int32(), r.Int32(), r.Int32()
			r.Int32()
			r.Float32()
			r.Int32()
			offers = append(offers, fmt.Sprintf("[%d: %s%s -> %s uses %d/%d xp %d disabled %v]", i, a, b2, out, uses, maxUses, xp, disabled))
		}
		lvl, xp := r.VarInt(), r.VarInt()
		log.Printf("MERCHANT window %d level %d xp %d progress %v: %s", win, lvl, xp, r.Bool(), strings.Join(offers, " "))
	case v777.ClientboundPlayUpdateRecipes:
		n := int(r.VarInt())
		var sets []string
		for i := 0; i < n && r.Err == nil; i++ {
			key := r.String(256)
			m := int(r.VarInt())
			propSets[key] = map[int32]bool{}
			for j := 0; j < m; j++ {
				propSets[key][r.VarInt()] = true
			}
			sets = append(sets, fmt.Sprintf("%s=%d", key, m))
		}
		stone := int(r.VarInt())
		log.Printf("UPDATE RECIPES sets %s; stonecutter %d", strings.Join(sets, " "), stone)
	case v777.ClientboundPlayOpenSignEditor:
		x, y, z := r.Position()
		s.signOpen = fmt.Sprintf("%d %d %d", x, y, z)
		log.Printf("SIGN EDITOR at %s front %v", s.signOpen, r.Bool())
	default:
		return false
	}
	return true
}

// layout facts of the open window: container part size.
func (s *invState) containerSize() int {
	if s.win == 0 {
		return 9 // result, grid, armour (not moved into by shift here)
	}
	return len(s.slots) - 36
}

func invMaxStack(id int32) int32 { return jitem.DefaultMaxStackSize(id) }

// invStepClick applies a click locally (vanilla's rules) and sends it with the changed slots.
func invStepClick(b *bot, a []string) time.Duration {
	slot, button, mode := invAtoi(a, 1), invAtoi(a, 2), invAtoi(a, 3)
	b.mu.Lock()
	s := binv
	before := append([]bslot(nil), s.slots...)
	carriedBefore := s.carried
	s.apply(slot, button, mode)
	changed := map[int]bslot{}
	for i := range s.slots {
		if s.slots[i] != before[i] {
			changed[i] = s.slots[i]
		}
	}
	_ = carriedBefore
	win, state, carried := s.win, s.stateID, s.carried
	if win == 0 {
		copy(s.inv[:], s.slots)
	} else if len(s.slots) >= 36 {
		copy(s.inv[9:45], s.slots[len(s.slots)-36:])
	}
	b.mu.Unlock()
	invSendClick(b, win, state, slot, button, mode, changed, carried)
	return 500 * time.Millisecond
}

func invSendClick(b *bot, win, state int32, slot, button, mode int, changed map[int]bslot, carried bslot) {
	w := &wire.Writer{}
	w.VarInt(win)
	w.VarInt(state)
	w.Int16(int16(slot))
	w.Int8(int8(button))
	w.VarInt(int32(mode))
	w.VarInt(int32(len(changed)))
	for i, st := range changed {
		w.Int16(int16(i))
		invWriteHashed(w, st)
	}
	invWriteHashed(w, carried)
	b.write(v777.ServerboundPlayContainerClick, w)
	log.Printf("CLICK window %d slot %d button %d mode %d: %d changed, carried %s", win, slot, button, mode, len(changed), carried)
}

func invWriteHashed(w *wire.Writer, s bslot) {
	if s.empty() {
		w.Bool(false)
		return
	}
	w.Bool(true)
	w.VarInt(s.id)
	w.VarInt(s.n)
	w.VarInt(0)
	w.VarInt(0)
}

// isResult reports whether a slot of the open window is a crafting result.
func (s *invState) isResult(slot int) bool {
	if s.win != 0 {
		for name, rs := range stationResultSlots {
			if s.menu == v777.BuiltinID("minecraft:menu", "minecraft:"+name) {
				return slot == rs
			}
		}
	}
	if slot != 0 {
		return false
	}
	return s.win == 0 || s.menu == v777.BuiltinID("minecraft:menu", "minecraft:crafting")
}

// stationResultSlots are the result slots of the station windows (their inputs are not shrunk
// by the bot: the bridge sends what is left).
var stationResultSlots = map[string]int{"anvil": 2, "grindstone": 2, "stonecutter": 1, "smithing": 3, "loom": 3,
	"cartography_table": 2, "merchant": 2}

func (s *invState) grid() []int {
	if s.win == 0 {
		return []int{1, 2, 3, 4}
	}
	if s.isResult(0) {
		return []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	}
	return nil
}

// apply applies a click as a vanilla client does (the parts the tests use).
func (s *invState) apply(slot, button, mode int) {
	sl := s.slots
	get := func(i int) *bslot { return &sl[i] }
	switch mode {
	case 0:
		if slot == -999 {
			if button == 0 {
				s.carried = bslot{}
			} else if !s.carried.empty() {
				s.carried.n--
			}
			return
		}
		if slot < 0 || slot >= len(sl) {
			return
		}
		t := get(slot)
		if s.isResult(slot) {
			if t.empty() || (!s.carried.empty() && (s.carried.id != t.id || s.carried.n+t.n > invMaxStack(t.id))) {
				return
			}
			s.carried = bslot{t.id, s.carried.n + t.n}
			*t = bslot{}
			for _, g := range s.grid() {
				if !sl[g].empty() {
					sl[g].n--
				}
			}
			return
		}
		c := &s.carried
		switch {
		case c.empty() && t.empty():
		case c.empty():
			n := t.n
			if button == 1 {
				n = (t.n + 1) / 2
			}
			*c = bslot{t.id, n}
			t.n -= n
		case t.empty():
			n := c.n
			if button == 1 {
				n = 1
			}
			*t = bslot{c.id, n}
			c.n -= n
		case t.id == c.id:
			n := c.n
			if button == 1 {
				n = 1
			}
			n = min(n, invMaxStack(t.id)-t.n)
			t.n += n
			c.n -= n
		default:
			*t, *c = *c, *t
		}
		if c.n <= 0 {
			*c = bslot{}
		}
		if t.n <= 0 {
			*t = bslot{}
		}
	case 1:
		if slot < 0 || slot >= len(sl) || sl[slot].empty() {
			return
		}
		n := s.containerSize()
		st := sl[slot]
		var dst []int
		if s.win == 0 {
			switch eq := invEquipSlot(st.id); {
			case slot >= 9 && eq >= 0 && sl[eq].empty():
				dst = []int{eq} // vanilla InventoryMenu: armour to its empty slot, a shield to the off hand
			case slot < 9:
				dst = invRange(9, 45, true)
			case slot < 36:
				dst = invRange(36, 45, false)
			default:
				dst = invRange(9, 36, false)
			}
		} else if slot < n {
			dst = invRange(n, n+36, true)
		} else if r, ok := s.furnaceRoute(st.id, slot, n); ok {
			dst = r // vanilla AbstractFurnaceMenu.quickMoveStack, with the property sets the bridge sent
		} else {
			dst = invRange(0, n, false)
			if s.isResult(0) {
				dst = invRange(1, 10, false)
			}
		}
		if s.isResult(slot) {
			// One craft at a time, as the client predicts.
			s.carried, sl[slot] = sl[slot], bslot{}
			for _, g := range s.grid() {
				if !sl[g].empty() {
					sl[g].n--
				}
			}
			st = s.carried
			s.carried = bslot{}
		} else {
			sl[slot] = bslot{}
		}
		left := invMoveTo(sl, st, dst)
		if left.n > 0 {
			if s.isResult(slot) {
				return
			}
			sl[slot] = left
		}
	case 2:
		hb := len(sl) - 9 + button
		if s.win == 0 {
			hb = 36 + button
			if button == 40 {
				hb = 45
			}
		}
		if slot < 0 || slot >= len(sl) || hb < 0 || hb >= len(sl) {
			return
		}
		if s.isResult(slot) {
			if sl[hb].empty() && !sl[slot].empty() {
				sl[hb], sl[slot] = sl[slot], bslot{}
				for _, g := range s.grid() {
					if !sl[g].empty() {
						sl[g].n--
					}
				}
			}
			return
		}
		sl[slot], sl[hb] = sl[hb], sl[slot]
	case 4:
		if slot < 0 || slot >= len(sl) || sl[slot].empty() {
			return
		}
		if button == 0 {
			sl[slot].n--
			if sl[slot].n == 0 {
				sl[slot] = bslot{}
			}
		} else {
			sl[slot] = bslot{}
		}
	case 6:
		c := &s.carried
		if c.empty() {
			return
		}
		for i := range sl {
			if c.n >= invMaxStack(c.id) {
				break
			}
			if s.isResult(i) || sl[i].id != c.id || sl[i].empty() {
				continue
			}
			n := min(sl[i].n, invMaxStack(c.id)-c.n)
			c.n += n
			sl[i].n -= n
			if sl[i].n == 0 {
				sl[i] = bslot{}
			}
		}
	}
}

// propSets are the recipe property sets of update_recipes (Java item ids).
var propSets = map[string]map[int32]bool{}

// furnaceRoute is where a vanilla client shift-clicks an inventory item in a furnace window:
// smeltable (the window's input property set) to the input, fuel to the fuel slot, else between
// the inventory and the hotbar.
func (s *invState) furnaceRoute(id int32, slot, n int) ([]int, bool) {
	set := ""
	for name, key := range map[string]string{"furnace": "minecraft:furnace_input", "blast_furnace": "minecraft:blast_furnace_input", "smoker": "minecraft:smoker_input"} {
		if s.menu == v777.BuiltinID("minecraft:menu", "minecraft:"+name) {
			set = key
		}
	}
	if set == "" || slot < n {
		return nil, false
	}
	fuel := strings.TrimPrefix(jitem.Name(id), "minecraft:")
	isFuel := fuel == "coal" || fuel == "charcoal" || fuel == "coal_block" || fuel == "lava_bucket" || fuel == "blaze_rod" ||
		fuel == "stick" || strings.HasSuffix(fuel, "_planks") || strings.HasSuffix(fuel, "_log")
	switch {
	case propSets[set][id]:
		return []int{0}, true
	case isFuel:
		return []int{1}, true
	case slot < n+27:
		return invRange(n+27, n+36, false), true
	}
	return invRange(n, n+27, false), true
}

func invRange(from, to int, reverse bool) []int {
	var out []int
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

// invMoveTo is vanilla's moveItemStackTo: merge into stacks of the item, then empty slots.
func invMoveTo(sl []bslot, st bslot, dst []int) bslot {
	for _, i := range dst {
		if st.n == 0 {
			break
		}
		if sl[i].id == st.id && !sl[i].empty() {
			n := min(st.n, invMaxStack(st.id)-sl[i].n)
			sl[i].n += n
			st.n -= n
		}
	}
	for _, i := range dst {
		if st.n == 0 {
			break
		}
		if sl[i].empty() {
			sl[i] = st
			st.n = 0
		}
	}
	return st
}

// invStepDrag spreads the cursor over slots (button 0: evenly, 1: one each).
func invStepDrag(b *bot, a []string) time.Duration {
	btn := invAtoi(a, 1)
	var slots []int
	for _, x := range a[2:] {
		v, _ := strconv.Atoi(x)
		slots = append(slots, v)
	}
	b.mu.Lock()
	s := binv
	win, state := s.win, s.stateID
	c := s.carried
	orig := c
	changed := map[int]bslot{}
	if !c.empty() && len(slots) > 0 {
		per := c.n / int32(len(slots))
		if btn == 1 {
			per = 1
		}
		for _, i := range slots {
			if i < 0 || i >= len(s.slots) || c.n < per || per == 0 {
				continue
			}
			t := &s.slots[i]
			if !t.empty() && t.id != c.id {
				continue
			}
			n := min(per, invMaxStack(c.id)-t.n)
			*t = bslot{c.id, t.n + n}
			c.n -= n
			changed[i] = *t
		}
		if c.n == 0 {
			c = bslot{}
		}
		s.carried = c
	}
	b.mu.Unlock()
	start, add, end := 0, 1, 2
	if btn == 1 {
		start, add, end = 4, 5, 6
	}
	invSendClick(b, win, state, -999, start, 5, nil, orig)
	for _, i := range slots {
		invSendClick(b, win, state, i, add, 5, nil, orig)
	}
	invSendClick(b, win, state, -999, end, 5, changed, s.carried)
	return 500 * time.Millisecond
}

// invEquipSlot is the inventory window slot a vanilla client quick-moves an item into before the
// inventory (armour 5-8, off hand 45), -1 for none (by item name: enough for the tests).
func invEquipSlot(id int32) int {
	n := strings.TrimPrefix(jitem.Name(id), "minecraft:")
	switch {
	case strings.HasSuffix(n, "_helmet") || n == "carved_pumpkin" || strings.HasSuffix(n, "_head") || strings.HasSuffix(n, "_skull"):
		return 5
	case strings.HasSuffix(n, "_chestplate") || n == "elytra":
		return 6
	case strings.HasSuffix(n, "_leggings"):
		return 7
	case strings.HasSuffix(n, "_boots"):
		return 8
	case n == "shield":
		return 45
	}
	return -1
}
