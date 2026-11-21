package bridge

import (
	"strings"
	"testing"

	"delta/core/internal/javapack"
)

func TestSlotTextFillerCells(t *testing.T) {
	p := &Player{hudSlots: javapack.HudSlots{ActionBar: true, Strip: []string{"_hud+:"}}}
	cell := func(s string) string { return s + strings.Repeat("[", 100-len([]rune(s))) }
	// Customizable Sidebar's action bar: padding, offsets, background path, the marker, then a
	// cell of 100 per line.
	bar := " " + strings.Repeat("]", 14) + "\n" + strings.Repeat("[", 14) + "§z0000" +
		cell("ui/hud_tip_text_background") + "_hud+:§f" + cell("§6My Server") + cell("Money: 5") + "Kills: 2"
	got := p.slotText(bar)
	if want := "§f§6My Server\nMoney: 5\nKills: 2"; got != want {
		t.Errorf("slotText = %q, want %q", got, want)
	}
	// A title as it is: lines stay lines.
	if got := p.slotText("§lStats\nMoney: 5"); got != "§lStats\nMoney: 5" {
		t.Errorf("plain title = %q", got)
	}
}
