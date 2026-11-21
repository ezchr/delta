package bridge

import (
	"encoding/binary"
	"slices"
	"sort"
	"strconv"
	"strings"

	"delta/core/internal/javapack"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// HUD panels: a pack can make a sidebar (or any panel of text) of the title. The server sends
// "sb:§lMy Server\nMoney: 5" as a title, the pack never shows the title and writes what follows
// the marker in a panel of its own, kept until the next such title (javapack/hud.go finds those
// markers, javapack/hudplace.go where the pack draws each). Lines that are packed layout data
// (readable.go) are shown as their readable fields or left out. On Java a panel is shown where
// the pack has it:
//
//   - at a side (or anywhere else but the two below): the sidebar. An objective of the bridge's
//     own (hudObjective) whose title is the panel's first line and whose lines are the others,
//     with a blank number, in order. Several such panels follow each other by marker with an
//     empty line between them.
//   - at the top in the middle: boss bars, one per line, empty (Java has no boss bar without
//     the bar), above the game as the pack's bar is.
//   - at the bottom in the middle: the action bar, the lines side by side. Java fades an action
//     bar text after three seconds, so it is sent again every two; a text the server itself puts
//     there is shown for its time and the panel comes back after it.
//
// The top and the bottom are the panel's only while the server does not use them itself: while
// the server shows a boss bar of its own the top panels are in the sidebar with the side panels,
// and so are the bottom panels while a text the server put in the action bar is shown. They go
// back when the place is free again (hudTick sees the last boss bar go and the text's time end).
//
// A sidebar of the server's own and a side panel share the Java sidebar. While both are there
// the slot shows hudObjective, not the server's objective: its title is the server objective's,
// its first lines are the server objective's lines in the server's order (the texts and numbers
// the client would show for them), then an empty line and the panel's lines, the panel's title
// first. Java shows fifteen lines: the server's go first and the panel is cut. The server's
// objective is kept up to date on the client all the while (scoreboard.go), only not displayed,
// and gets the slot back when the panel goes. scoreboard.go calls sidebarChanged whenever the
// server's sidebar changes (a score set or reset, another or no objective in the slot, the
// objective removed, a name that became known); only the lines that differ are sent.

const (
	hudObjective = "delta_hud"
	hudMaxLines  = 15 // the most lines a Java sidebar shows
	hudMaxBars   = 4  // the most boss bars made of top panels
	// A Java action bar text stays 60 ticks, the last 20 fading: a panel's is sent again before
	// it fades, and a server's own text keeps the bar for its whole time.
	hudBarEvery = 40
	hudBarReal  = 60
)

// hudLine is a line of the sidebar: its text and the number shown after it ("": none).
type hudLine struct {
	text, num string
}

type hudPanelState struct {
	panels map[string][]string // the lines of each panel marker's last title

	// The sidebar: hudObjective as the client has it.
	on    bool      // the objective is made and in the sidebar
	title string    // its title
	sent  []hudLine // its lines (holders hudHolder(0..len-1))

	bars     []string // the boss bars shown, top to bottom
	bossBusy bool     // the server showed a boss bar of its own at the last look

	bar      string // the action bar text of the bottom panels ("": none, or they are in the sidebar)
	barWait  int    // ticks until it is sent again
	realWait int    // ticks a text of the server's own still has the action bar
}

func hudHolder(i int) string { return "delta_hud_" + strconv.Itoa(i) }

// hudBossUUID is the boss bar of line i of the top panels.
func hudBossUUID(i int) uuid.UUID {
	var u uuid.UUID
	copy(u[:], "dltahud!")
	binary.BigEndian.PutUint64(u[8:], uint64(i))
	u[6] = u[6]&0x0f | 0x40
	return u
}

// setHudPanel shows the text of a title with a panel marker (the marker cut out).
func (p *Player) setHudPanel(marker, t string) {
	st := &p.hudPanel
	if st.panels == nil {
		st.panels = map[string][]string{}
	}
	if lines := panelLines(t); len(lines) > 0 {
		st.panels[marker] = lines
	} else {
		delete(st.panels, marker)
	}
	p.drawHudPanel()
}

// panelLines is a panel's text as sidebar lines: its readable lines, without empty lines at the
// start and the end.
func panelLines(t string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(t, "\r", ""), "\n") {
		packed := looksPacked(l)
		l = readableText(l)
		if (packed && l == "") || strings.Contains(stripColours(l), "textures/") {
			continue // layout data with nothing to read
		}
		out = append(out, l)
	}
	blank := func(s string) bool { return strings.TrimSpace(stripColours(s)) == "" }
	for len(out) > 0 && blank(out[0]) {
		out = out[1:]
	}
	for len(out) > 0 && blank(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	return out
}

// serverSidebar is the objective the server shows in the sidebar (nil: none) and its name.
func (p *Player) serverSidebar() (string, *objective) {
	if p.sb == nil {
		return "", nil
	}
	for name, o := range p.sb.objectives {
		if o.slots[1] {
			return name, o
		}
	}
	return "", nil
}

// panelText is the lines of the panels drawn at place, one panel after the other by marker with
// sep between two (nil: nothing between them).
func (p *Player) panelText(place javapack.HudPlace, sep []string) []string {
	st := &p.hudPanel
	markers := make([]string, 0, len(st.panels))
	for m := range st.panels {
		if p.hudPlaceNow(m) == place {
			markers = append(markers, m)
		}
	}
	sort.Strings(markers)
	var all []string
	for i, m := range markers {
		if i > 0 {
			all = append(all, sep...)
		}
		all = append(all, st.panels[m]...)
	}
	return all
}

// serverBossBar reports whether the server shows a boss bar of its own.
func (p *Player) serverBossBar() bool { return p.ents != nil && len(p.ents.bosses) > 0 }

// hudPlaceNow is where the panel of marker is shown now: where the pack draws it, or the
// sidebar while the server itself uses that place.
func (p *Player) hudPlaceNow(marker string) javapack.HudPlace {
	place := p.hudPlaces[marker]
	if place == javapack.HudTop && p.serverBossBar() || place == javapack.HudBottom && p.hudPanel.realWait > 0 {
		return javapack.HudSide
	}
	return place
}

// drawHudPanel shows the panels as they are now.
func (p *Player) drawHudPanel() {
	p.hudPanel.bossBusy = p.serverBossBar()
	p.drawHudSidebar(false)
	p.drawHudBars()
	p.drawHudActionBar()
}

// sidebarChanged is called when the server's own sidebar changed: display, another objective or
// none in the sidebar slot (the client was told to show that); else a line of it.
func (p *Player) sidebarChanged(display bool) {
	if len(p.hudPanel.panels) == 0 && !p.hudPanel.on {
		return
	}
	p.drawHudSidebar(display)
}

// serverLines are the lines of the server's objective name as a sidebar shows them, top to
// bottom: by score (descending, or ascending for an objective sorted so), equal scores by
// holder as the client orders them. A text line (a fake player) has no number.
func (sb *scoreboard) serverLines(name string, ascending bool) []hudLine {
	var es []*scoreEntry
	for _, se := range sb.entries {
		if se.e.ObjectiveName == name {
			es = append(es, se)
		}
	}
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.e.Score != b.e.Score {
			return (a.e.Score < b.e.Score) == ascending
		}
		if c := strings.Compare(strings.ToLower(a.holder), strings.ToLower(b.holder)); c != 0 {
			return c < 0
		}
		return a.e.EntryID < b.e.EntryID
	})
	lines := make([]hudLine, 0, len(es))
	for _, se := range es {
		l := hudLine{text: se.shown}
		if se.holder == "" {
			l.text = se.e.DisplayName // not sent yet
		}
		if se.e.IdentityType != protocol.ScoreboardIdentityFakePlayer {
			l.num = strconv.Itoa(int(se.e.Score))
		}
		lines = append(lines, l)
	}
	return lines
}

// mergeSidebar is the server's lines with a panel's below them: the server's first (the first
// hudMaxLines of them), then an empty line and the panel's lines, as many as still fit. A panel
// that would only be the empty line is left out.
func mergeSidebar(server []hudLine, board []string) []hudLine {
	out := slices.Clone(server[:min(len(server), hudMaxLines)])
	if len(board) == 0 || len(out)+2 > hudMaxLines {
		return out
	}
	out = append(out, hudLine{})
	for _, l := range board {
		if len(out) == hudMaxLines {
			break
		}
		out = append(out, hudLine{text: l})
	}
	return out
}

// hudSidebar is what hudObjective shows now: its title and lines; ok false when there is no side
// panel (the slot is the server's alone).
func (p *Player) hudSidebar() (title string, lines []hudLine, ok bool) {
	board := p.panelText(javapack.HudSide, []string{""})
	if len(board) == 0 {
		return "", nil, false
	}
	if name, o := p.serverSidebar(); o != nil {
		return o.title, mergeSidebar(p.sb.serverLines(name, o.ascending), board), true
	}
	for _, l := range board[1:min(len(board), 1+hudMaxLines)] {
		lines = append(lines, hudLine{text: l})
	}
	return board[0], lines, true
}

// drawHudSidebar sends the sidebar as the side panels and the server's sidebar have it now.
// display: the client was just told what the sidebar slot shows, so it is told again.
func (p *Player) drawHudSidebar(display bool) {
	st := &p.hudPanel
	title, lines, ok := p.hudSidebar()
	if !ok {
		p.removeHudPanel()
		return
	}
	if !st.on || st.title != title {
		w := p.pk()
		w.String(hudObjective)
		if st.on {
			w.Byte(2) // update
		} else {
			w.Byte(0) // create
		}
		writeText(w, title)
		w.VarInt(0)   // integer
		w.Bool(false) // default number format
		p.send(v777.ClientboundPlaySetObjective, w)
		display = display || !st.on
		st.on, st.title = true, title
	}
	if display {
		p.sendDisplayObjective(1, hudObjective)
	}
	for i, l := range lines {
		if i < len(st.sent) && st.sent[i] == l {
			continue
		}
		w := p.pk()
		w.String(hudHolder(i))
		w.String(hudObjective)
		w.VarInt(int32(hudMaxLines - i)) // the sidebar is in descending order
		w.Bool(true)
		writeText(w, l.text)
		w.Bool(true)
		if l.num == "" {
			w.VarInt(0) // blank: no number
		} else {
			w.VarInt(2) // fixed: the server's number, as the sidebar shows one
			writeText(w, "§c"+l.num)
		}
		p.send(v777.ClientboundPlaySetScore, w)
	}
	for i := len(lines); i < len(st.sent); i++ {
		p.resetScore(hudHolder(i), hudObjective)
	}
	st.sent = lines
}

// removeHudPanel takes the sidebar objective away; the server's own, if it shows one, has the
// sidebar again.
func (p *Player) removeHudPanel() {
	st := &p.hudPanel
	if !st.on {
		return
	}
	w := p.pk()
	w.String(hudObjective)
	w.Byte(1)
	p.send(v777.ClientboundPlaySetObjective, w)
	st.on, st.title, st.sent = false, "", nil
	if name, o := p.serverSidebar(); o != nil {
		p.sendDisplayObjective(1, name)
	}
}

// drawHudBars shows the top panels' lines as boss bars: one each, empty and white.
func (p *Player) drawHudBars() {
	st := &p.hudPanel
	lines := p.panelText(javapack.HudTop, nil)
	if len(lines) > hudMaxBars {
		lines = lines[:hudMaxBars]
	}
	for i, l := range lines {
		switch {
		case i >= len(st.bars):
			w := p.pk()
			w.UUID(hudBossUUID(i))
			w.VarInt(0) // add
			writeText(w, l)
			w.Float32(0)
			w.VarInt(6) // white
			w.VarInt(0) // no notches
			w.Byte(0)   // flags
			p.send(v777.ClientboundPlayBossEvent, w)
		case st.bars[i] != l:
			w := p.pk()
			w.UUID(hudBossUUID(i))
			w.VarInt(3) // title
			writeText(w, l)
			p.send(v777.ClientboundPlayBossEvent, w)
		}
	}
	for i := len(lines); i < len(st.bars); i++ {
		w := p.pk()
		w.UUID(hudBossUUID(i))
		w.VarInt(1) // remove
		p.send(v777.ClientboundPlayBossEvent, w)
	}
	st.bars = slices.Clone(lines)
}

// drawHudActionBar shows the bottom panels' lines in the action bar. While a text of the
// server's own has the bar they are in the sidebar and nothing is sent here.
func (p *Player) drawHudActionBar() {
	st := &p.hudPanel
	bar := strings.Join(p.panelText(javapack.HudBottom, nil), "§r  ")
	if bar == st.bar {
		return
	}
	st.bar = bar
	if st.realWait > 0 {
		return // the server's text stays
	}
	p.sendActionBar(bar) // empty: the panel is gone, and so is its text
	st.barWait = hudBarEvery
}

// serverActionBar shows an action bar text of the server's own. It has the bar for as long as
// Java shows it: bottom panels are in the sidebar meanwhile and back in the bar after it.
func (p *Player) serverActionBar(s string) {
	st := &p.hudPanel
	if strings.TrimSpace(stripColours(s)) == "" {
		// The server clears the bar: it is the panels' again at once (and not cleared if they
		// are in it).
		if st.realWait > 0 {
			st.realWait = 0
			p.sendActionBar(s)
			p.drawHudPanel()
		} else if st.bar == "" {
			p.sendActionBar(s)
		}
		return
	}
	was := st.realWait > 0
	st.realWait = hudBarReal
	p.sendActionBar(s)
	if !was && len(st.panels) > 0 {
		p.drawHudPanel() // the bottom panels move to the sidebar
	}
}

// hudTick keeps a bottom panel in the action bar, and moves the top and bottom panels when the
// server takes or leaves their place.
func hudTick(p *Player) {
	st := &p.hudPanel
	if st.realWait > 0 {
		if st.realWait--; st.realWait == 0 && len(st.panels) > 0 {
			p.drawHudPanel() // the bottom panels are back in the bar
		}
		return
	}
	if len(st.panels) > 0 && st.bossBusy != p.serverBossBar() {
		p.drawHudPanel()
	}
	if st.bar == "" {
		return
	}
	if st.barWait--; st.barWait <= 0 {
		p.sendActionBar(st.bar)
		st.barWait = hudBarEvery
	}
}

// clearHudPanels takes every panel off the screen and forgets them.
func (p *Player) clearHudPanels() {
	p.hudPanel.panels = nil
	p.drawHudPanel()
	p.hudPanel.realWait = 0
}

func init() {
	OnTick(hudTick)
	OnReset(func(p *Player, newConn bool) {
		if newConn {
			// Another server: its pack has its own markers.
			p.clearHudPanels()
		}
	})
}
