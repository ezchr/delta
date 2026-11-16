package main

// NPC dialogue steps of jbbot (the original bridge npc.go): the bridge shows an NPC dialogue as a dialog
// whose buttons run delta:npc with {n: dialog, b: button}. The bot logs `NPC DIALOG n: text`
// for each one and `CLEAR DIALOG`, and answers like a Java client clicking a button or pressing
// Escape (the exit action, b -2).
//
// Steps: npcbtn B | npcclose | expectdialog TEXT (the last NPC dialog contains TEXT) |
// expectdialogs N (at least N NPC dialogs shown) | expectcleared (no dialog shown now).

import (
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

var bnpc struct {
	id    int32
	last  string
	shown int
	open  bool
}

func init() {
	// Before inv.go's handler (which logs every dialog and stops the chain).
	extraHandlers = append([]func(b *bot, id int32, r *wire.Reader) bool{npcHandle}, extraHandlers...)
	extraSteps["npcbtn"] = func(b *bot, a []string) time.Duration {
		n, _ := strconv.Atoi(invArg(a, 1))
		npcClick(b, int32(n))
		return 1200 * time.Millisecond
	}
	extraSteps["npcclose"] = func(b *bot, a []string) time.Duration {
		npcClick(b, -2)
		return 1000 * time.Millisecond
	}
	extraSteps["expectdialog"] = func(b *bot, a []string) time.Duration {
		want := strings.Join(a[1:], " ")
		b.mu.Lock()
		ok := bnpc.open && strings.Contains(bnpc.last, want)
		b.mu.Unlock()
		log.Printf("EXPECTDIALOG %q: %v", want, ok)
		return 0
	}
	extraSteps["expectdialogs"] = func(b *bot, a []string) time.Duration {
		n, _ := strconv.Atoi(invArg(a, 1))
		b.mu.Lock()
		c := bnpc.shown
		b.mu.Unlock()
		log.Printf("EXPECTDIALOGS >= %d: %v (%d)", n, c >= n, c)
		return 0
	}
	extraSteps["expectcleared"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		ok := !bnpc.open
		b.mu.Unlock()
		log.Printf("EXPECTCLEARED: %v", ok)
		return 0
	}
}

func npcHandle(b *bot, id int32, r *wire.Reader) bool {
	switch id {
	case v777.ClientboundPlayShowDialog:
		body := r.B[r.Off:] // not consumed: inv.go reads it too
		if !strings.Contains(string(body), "delta:npc") {
			bnpc.open = false
			return false
		}
		if i := strings.Index(string(body), "\x03\x00\x01n"); i >= 0 && i+8 <= len(body) {
			bnpc.id = wire.NewReader(body[i+4:]).Int32()
		}
		bnpc.last, bnpc.open = printable(body), true
		bnpc.shown++
		log.Printf("NPC DIALOG %d: %s", bnpc.id, bnpc.last)
		return false // inv.go logs it too
	case v777.ClientboundPlayClearDialog:
		bnpc.open = false
		log.Printf("CLEAR DIALOG")
		return true
	}
	return false
}

// npcClick sends custom_click_action delta:npc for the last NPC dialog.
func npcClick(b *bot, button int32) {
	b.mu.Lock()
	id := bnpc.id
	if button == -2 {
		bnpc.open = false // Escape closes the screen on the client
	}
	b.mu.Unlock()
	pl := &wire.Writer{}
	pl.Byte(text.TagCompound)
	text.IntTag(pl, "n", id)
	text.IntTag(pl, "b", button)
	pl.Byte(text.TagEnd)
	w := &wire.Writer{}
	w.String("delta:npc")
	w.VarInt(int32(len(pl.B)))
	w.Raw(pl.B)
	b.write(v777.ServerboundPlayCustomClickAction, w)
	log.Printf("NPC CLICK dialog %d button %d", id, button)
}
