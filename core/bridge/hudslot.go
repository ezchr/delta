package bridge

import (
	"strings"
	"unicode"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Sidebar packs that move the vanilla title (or the action bar) to a side of the screen as a
// whole (javapack/hudslots.go): every title the server sends is a sidebar there, with no marker
// at all (/titleraw @a title {"rawtext":[...]} on a timer). On Java such a text is a HUD panel
// (hudpanel.go) at the side, so the Java sidebar, merged under a sidebar of the server's own.
// It goes when Bedrock's would: a title after its fade in, stay and fade out (the server's
// times, or Bedrock's 10, 70 and 20 ticks), an action bar text after three seconds; a server
// keeps such a sidebar up by sending it again before that.

const (
	slotTitleMarker = "\x00title"
	slotBarMarker   = "\x00actionbar"
	// slotBarTicks is how long Bedrock shows an action bar text.
	slotBarTicks = 60
)

// slotTitleTicks is how long a title is shown: the server's times, else Bedrock's.
func (p *Player) slotTitleTicks() int {
	if t := p.hudPanel.titleTicks; t > 0 {
		return t
	}
	return 10 + 70 + 20
}

// slotTitle shows a title or an action bar text the pack shows as a sidebar; false when it is
// not one (it is shown as usual).
func (p *Player) slotTitle(pk *packet.SetTitle, t string) bool {
	st := &p.hudPanel
	switch pk.ActionType {
	case packet.TitleActionSetDurations:
		st.titleTicks = int(pk.FadeInDuration + pk.RemainDuration + pk.FadeOutDuration)
		return false
	case packet.TitleActionReset:
		st.titleTicks = 0
		fallthrough
	case packet.TitleActionClear:
		if p.hudSlots.Title {
			p.setSlotPanel(slotTitleMarker, "", 0)
		}
		return false
	case packet.TitleActionSetTitle, packet.TitleActionTitleTextObject:
		if !p.hudSlots.Title {
			return false
		}
		p.setSlotPanel(slotTitleMarker, p.slotText(t), p.slotTitleTicks())
		return true
	case packet.TitleActionSetSubtitle, packet.TitleActionSubtitleTextObject:
		// The pack's title box has no subtitle (it is part of the vanilla title control the
		// pack replaced): not shown on Bedrock either.
		return p.hudSlots.Title
	case packet.TitleActionSetActionBar, packet.TitleActionActionbarTextObject:
		if !p.hudSlots.ActionBar {
			return false
		}
		p.setSlotPanel(slotBarMarker, p.slotText(t), slotBarTicks)
		return true
	}
	return false
}

// setSlotPanel shows (t "": takes away) the panel of a moved title or action bar for ticks.
func (p *Player) setSlotPanel(marker, t string, ticks int) {
	st := &p.hudPanel
	if st.expire == nil {
		st.expire = map[string]int{}
	}
	if t == "" {
		delete(st.expire, marker)
		if _, ok := st.panels[marker]; !ok {
			return
		}
	} else {
		st.expire[marker] = ticks
	}
	p.setHudPanel(marker, t)
}

// slotTick takes away the moved title or action bar panels whose time is up.
func slotTick(p *Player) {
	st := &p.hudPanel
	for m, left := range st.expire {
		if left--; left > 0 {
			st.expire[m] = left
			continue
		}
		delete(st.expire, m)
		if _, ok := st.panels[m]; ok {
			delete(st.panels, m)
			p.drawHudPanel()
		}
	}
}

// slotText is the readable text of a moved title or action bar: the markers the pack cuts out
// taken out, and a text the pack lays out in cells padded with a filler character ("[" to a
// width of 100, a picture's path in a cell of its own) one line per cell.
func (p *Player) slotText(t string) string {
	for _, m := range p.hudSlots.Strip {
		t = strings.ReplaceAll(t, m, "")
	}
	cells := fillerCells(t)
	if cells == nil {
		return t
	}
	var lines []string
	for _, c := range cells {
		plain := strings.TrimSpace(stripColours(c))
		if plain == "" || (strings.Contains(plain, "/") && !strings.ContainsRune(plain, ' ')) {
			continue // padding, or the path of a picture (the box's background)
		}
		lines = append(lines, c)
	}
	return strings.Join(lines, "\n")
}

// fillerCells splits a text at runs of 6 or more of one filler character (punctuation that is
// not a colour sign), nil when it has none.
func fillerCells(t string) []string {
	rs := []rune(t)
	var cells []string
	start, found := 0, false
	for i := 0; i < len(rs); {
		j := i
		for j < len(rs) && rs[j] == rs[i] {
			j++
		}
		if r := rs[i]; j-i >= 6 && unicode.IsPunct(r) && r != '§' {
			cells = append(cells, string(rs[start:i]))
			start, found = j, true
		}
		i = j
	}
	if !found {
		return nil
	}
	return append(cells, string(rs[start:]))
}
