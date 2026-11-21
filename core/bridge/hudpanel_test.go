package bridge

import (
	"reflect"
	"strconv"
	"testing"

	"delta/core/internal/javapack"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// hudPlayer is a player on a fake connection whose Java packets the test takes, on a server
// whose pack has a sidebar (sb:), a top bar (tb:) and a bottom bar (ab:) made of titles.
func hudPlayer() (*Player, func() []outPacket) {
	p := &Player{}
	p.java = &JavaConn{ver: version.Newest, wake: make(chan struct{}, 1), closed: make(chan struct{})}
	p.java.writers.New = func() any { return &wire.Writer{} }
	p.ents = newEntities(p)
	p.hudMarkers = []string{"ab:", "sb:", "tb:"}
	p.hudPanels = p.hudMarkers
	p.hudPlaces = map[string]javapack.HudPlace{"sb:": javapack.HudSide, "tb:": javapack.HudTop, "ab:": javapack.HudBottom}
	take := func() []outPacket {
		p.java.outMu.Lock()
		defer p.java.outMu.Unlock()
		out := p.java.out
		p.java.out = nil
		return out
	}
	return p, take
}

// sidebarShown is the objective the client was last told the sidebar shows ("-": not told).
func sidebarShown(out []outPacket) string {
	shown := "-"
	for _, pk := range out {
		if pk.id != v777.ClientboundPlaySetDisplayObjective {
			continue
		}
		r := wire.NewReader(pk.w.B)
		if r.VarInt() == 1 {
			shown = r.String(32767)
		}
	}
	return shown
}

func count(out []outPacket, id int32) int {
	n := 0
	for _, pk := range out {
		if pk.id == id {
			n++
		}
	}
	return n
}

func textLines(texts ...string) []hudLine {
	out := make([]hudLine, len(texts))
	for i, t := range texts {
		out[i] = hudLine{text: t}
	}
	return out
}

func fakeLine(id int64, objective, text string, score int32) protocol.ScoreboardEntry {
	return protocol.ScoreboardEntry{EntryID: id, ObjectiveName: objective, Score: score,
		IdentityType: protocol.ScoreboardIdentityFakePlayer, DisplayName: text}
}

// TestMergeSidebar: the server's lines, an empty line, the panel's; fifteen lines at most, the
// server's first.
func TestMergeSidebar(t *testing.T) {
	server := func(n int) []hudLine {
		out := make([]hudLine, n)
		for i := range out {
			out[i] = hudLine{text: "s" + strconv.Itoa(i)}
		}
		return out
	}
	board := []string{"§lBoard", "Money: 5", "Gems: 2", "Rank: A"}
	got := mergeSidebar(server(2), board)
	if want := textLines("s0", "s1", "", "§lBoard", "Money: 5", "Gems: 2", "Rank: A"); !reflect.DeepEqual(got, want) {
		t.Errorf("merged = %q", got)
	}
	// Twelve of the server's: the empty line and two of the panel's four still fit.
	if got = mergeSidebar(server(12), board); len(got) != 15 || got[11].text != "s11" || got[12].text != "" || got[14].text != "Money: 5" {
		t.Errorf("cut: %q", got)
	}
	// No room for more than the empty line: the server's lines only.
	if got = mergeSidebar(server(14), board); len(got) != 14 {
		t.Errorf("14 server lines: %d lines", len(got))
	}
	if got = mergeSidebar(server(20), board); len(got) != 15 || got[14].text != "s14" {
		t.Errorf("20 server lines: %q", got)
	}
	if got = mergeSidebar(server(3), nil); len(got) != 3 {
		t.Errorf("no panel: %q", got)
	}
}

// TestServerLines: the server's lines in its order: ascending counts up, descending down, equal
// scores as the client orders them; a text line has no number, a player's has the score.
func TestServerLines(t *testing.T) {
	sb := &scoreboard{entries: map[int64]*scoreEntry{}}
	add := func(id int64, text string, score int32) {
		sb.entries[id] = &scoreEntry{e: fakeLine(id, "obj", text, score), holder: "bb" + strconv.FormatInt(id, 36), shown: text}
	}
	add(1, "third", 3)
	add(2, "first", 1)
	add(3, "second", 2)
	add(4, "second too", 2)
	sb.entries[9] = &scoreEntry{e: fakeLine(9, "other", "not this objective", 0), holder: "bb9", shown: "x"}
	sb.entries[5] = &scoreEntry{e: protocol.ScoreboardEntry{EntryID: 5, ObjectiveName: "obj", Score: 7,
		IdentityType: protocol.ScoreboardIdentityPlayer}, holder: "Steve", shown: "Steve"}
	want := []hudLine{{"first", ""}, {"second", ""}, {"second too", ""}, {"third", ""}, {"Steve", "7"}}
	if got := sb.serverLines("obj", true); !reflect.DeepEqual(got, want) {
		t.Errorf("ascending = %q", got)
	}
	want = []hudLine{{"Steve", "7"}, {"third", ""}, {"second", ""}, {"second too", ""}, {"first", ""}}
	if got := sb.serverLines("obj", false); !reflect.DeepEqual(got, want) {
		t.Errorf("descending = %q", got)
	}
}

// TestHudSidebarWithServer: a panel has the sidebar to itself until the server shows an
// objective there; then the sidebar is the server's lines with the panel below, and every
// change of the server's sidebar or of the panel merges again.
func TestHudSidebarWithServer(t *testing.T) {
	p, take := hudPlayer()
	st := &p.hudPanel
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "sb:§lBoard\nMoney: 5\nGems: 2"})
	if !st.on || st.title != "§lBoard" || !reflect.DeepEqual(st.sent, textLines("Money: 5", "Gems: 2")) {
		t.Fatalf("alone: %q %q", st.title, st.sent)
	}
	if out := take(); sidebarShown(out) != hudObjective || count(out, v777.ClientboundPlaySetTitleText) != 0 {
		t.Errorf("alone: the sidebar shows %q", sidebarShown(out))
	}

	// The server shows its own sidebar, counting up.
	p.setDisplayObjective(&packet.SetDisplayObjective{DisplaySlot: packet.ScoreboardSlotSidebar, ObjectiveName: "obj",
		DisplayName: "§eServer", CriteriaName: "dummy", SortOrder: packet.ScoreboardSortOrderAscending})
	p.setScore(&packet.SetScore{Entries: []protocol.ScoreboardEntry{
		fakeLine(1, "obj", "Kills: 3", 2), fakeLine(2, "obj", "Online: 9", 1)}})
	want := textLines("Online: 9", "Kills: 3", "", "§lBoard", "Money: 5", "Gems: 2")
	if st.title != "§eServer" || !reflect.DeepEqual(st.sent, want) {
		t.Fatalf("merged: %q %q", st.title, st.sent)
	}
	// The server's objective was put in the slot, and after it the merged one.
	if out := take(); sidebarShown(out) != hudObjective {
		t.Errorf("merged: the sidebar shows %q", sidebarShown(out))
	}

	// A line of the server's changes, one goes and one comes.
	p.setScore(&packet.SetScore{Entries: []protocol.ScoreboardEntry{
		fakeLine(1, "obj", "Kills: 4", 2), fakeLine(3, "obj", "Deaths: 0", 3)}})
	p.setScore(&packet.SetScore{Entries: []protocol.ScoreboardEntry{
		{EntryID: 2, ObjectiveName: "obj", IdentityType: protocol.ScoreboardIdentityRemove}}})
	want = textLines("Kills: 4", "Deaths: 0", "", "§lBoard", "Money: 5", "Gems: 2")
	if !reflect.DeepEqual(st.sent, want) {
		t.Errorf("after scores: %q", st.sent)
	}
	if out := take(); sidebarShown(out) != "-" {
		t.Errorf("a score put %q in the sidebar", sidebarShown(out))
	}

	// The panel changes: only its changed line is sent (to the merged objective).
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "sb:§lBoard\nMoney: 6\nGems: 2"})
	if st.sent[4].text != "Money: 6" {
		t.Errorf("after the panel: %q", st.sent)
	}
	if out := take(); count(out, v777.ClientboundPlaySetScore) != 1 || count(out, v777.ClientboundPlaySetObjective) != 0 {
		t.Errorf("a changed panel line sent %d scores, %d objectives", count(out, v777.ClientboundPlaySetScore), count(out, v777.ClientboundPlaySetObjective))
	}

	// The server sorts the other way.
	p.setDisplayObjective(&packet.SetDisplayObjective{DisplaySlot: packet.ScoreboardSlotSidebar, ObjectiveName: "obj",
		DisplayName: "§eServer 2", CriteriaName: "dummy", SortOrder: packet.ScoreboardSortOrderDescending})
	if st.title != "§eServer 2" || st.sent[0].text != "Deaths: 0" || st.sent[1].text != "Kills: 4" {
		t.Errorf("descending: %q %q", st.title, st.sent)
	}
	if out := take(); sidebarShown(out) != hudObjective {
		t.Errorf("descending: the sidebar shows %q", sidebarShown(out))
	}

	// The panel goes: the server's own objective has the sidebar again.
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "sb:"})
	if st.on {
		t.Error("the panel's objective is still there")
	}
	if out := take(); sidebarShown(out) != "obj" {
		t.Errorf("without a panel the sidebar shows %q", sidebarShown(out))
	}

	// And back; then the server removes its objective: the panel alone.
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "sb:§lBoard\nMoney: 6"})
	if len(st.sent) != 5 {
		t.Errorf("merged again: %q", st.sent)
	}
	p.removeObjective("obj")
	if st.title != "§lBoard" || !reflect.DeepEqual(st.sent, textLines("Money: 6")) {
		t.Errorf("after the objective was removed: %q %q", st.title, st.sent)
	}
	take()

	// A server that clears the slot leaves the panel in it.
	p.setDisplayObjective(&packet.SetDisplayObjective{DisplaySlot: packet.ScoreboardSlotSidebar, ObjectiveName: "obj2", DisplayName: "Two"})
	p.setDisplayObjective(&packet.SetDisplayObjective{DisplaySlot: packet.ScoreboardSlotSidebar, ObjectiveName: ""})
	if out := take(); sidebarShown(out) != hudObjective || st.title != "§lBoard" {
		t.Errorf("cleared slot: the sidebar shows %q, title %q", sidebarShown(out), st.title)
	}
}

// TestHudPlaces: a top panel is boss bars, a bottom panel the action bar, kept there, and
// neither is in the sidebar.
func TestHudPlaces(t *testing.T) {
	p, take := hudPlayer()
	st := &p.hudPanel
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "tb:§dLevel 10\nSecond line"})
	if out := take(); count(out, v777.ClientboundPlayBossEvent) != 2 || st.on || !reflect.DeepEqual(st.bars, []string{"§dLevel 10", "Second line"}) {
		t.Errorf("top: %d boss events, bars %q, sidebar %v", count(out, v777.ClientboundPlayBossEvent), st.bars, st.on)
	}
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "tb:§dLevel 11"})
	if out := take(); count(out, v777.ClientboundPlayBossEvent) != 2 || len(st.bars) != 1 { // a new title, a bar less
		t.Errorf("top again: %d boss events, bars %q", count(out, v777.ClientboundPlayBossEvent), st.bars)
	}

	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "ab:§aQuest: 3/5"})
	if out := take(); count(out, v777.ClientboundPlaySetActionBarText) != 1 || st.bar != "§aQuest: 3/5" {
		t.Errorf("bottom: %d action bars, %q", count(out, v777.ClientboundPlaySetActionBarText), st.bar)
	}
	for i := 0; i < hudBarEvery-1; i++ {
		hudTick(p)
	}
	if out := take(); len(out) != 0 {
		t.Errorf("%d packets before the bar is due again", len(out))
	}
	hudTick(p)
	if out := take(); count(out, v777.ClientboundPlaySetActionBarText) != 1 {
		t.Error("the bar was not sent again")
	}

	// The server's own action bar text has the bar for its time: the bottom panel is in the
	// sidebar meanwhile (alone there: its line is the title), and nothing of it in the bar.
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetActionBar, Text: "§cYou are in combat"})
	if out := take(); count(out, v777.ClientboundPlaySetActionBarText) != 1 || sidebarShown(out) != hudObjective ||
		st.bar != "" || st.title != "§aQuest: 3/5" {
		t.Errorf("server text: %d action bars, sidebar %q %q, bar %q", count(out, v777.ClientboundPlaySetActionBarText), sidebarShown(out), st.title, st.bar)
	}
	p.bedrockTitle(&packet.SetTitle{ActionType: packet.TitleActionSetTitle, Text: "ab:§aQuest: 4/5"})
	if out := take(); count(out, v777.ClientboundPlaySetActionBarText) != 0 || st.title != "§aQuest: 4/5" {
		t.Errorf("%d action bars while the server's text shows, sidebar %q", count(out, v777.ClientboundPlaySetActionBarText), st.title)
	}
	for i := 0; i < hudBarReal-1; i++ {
		hudTick(p)
	}
	if out := take(); len(out) != 0 {
		t.Errorf("%d packets during the server's text", len(out))
	}
	// Its time is over: the panel is in the bar again and out of the sidebar.
	hudTick(p)
	if out := take(); count(out, v777.ClientboundPlaySetActionBarText) != 1 || st.on || st.bar != "§aQuest: 4/5" {
		t.Errorf("after the server's text: %d action bars, sidebar %v, bar %q", count(out, v777.ClientboundPlaySetActionBarText), st.on, st.bar)
	}

	// The server shows a boss bar of its own: the top panel leaves the boss bars for the
	// sidebar, below the server's sidebar lines when it has one.
	p.bossEvent(&packet.BossEvent{BossEntityUniqueID: 5, EventType: packet.BossEventShow, BossBarTitle: "Dragon", HealthPercentage: 1})
	hudTick(p)
	if out := take(); count(out, v777.ClientboundPlayBossEvent) != 2 || len(st.bars) != 0 || st.title != "§dLevel 11" { // the server's bar, the panel's gone
		t.Errorf("server boss bar: %d boss events, bars %q, sidebar %q", count(out, v777.ClientboundPlayBossEvent), st.bars, st.title)
	}
	p.setDisplayObjective(&packet.SetDisplayObjective{DisplaySlot: packet.ScoreboardSlotSidebar, ObjectiveName: "obj", DisplayName: "Server"})
	p.setScore(&packet.SetScore{Entries: []protocol.ScoreboardEntry{fakeLine(1, "obj", "Kills: 3", 1)}})
	if want := textLines("Kills: 3", "", "§dLevel 11"); st.title != "Server" || !reflect.DeepEqual(st.sent, want) {
		t.Errorf("top panel under the server's sidebar: %q %q", st.title, st.sent)
	}
	take()
	// The boss bar goes: the panel is a boss bar again and the sidebar the server's alone.
	p.bossEvent(&packet.BossEvent{BossEntityUniqueID: 5, EventType: packet.BossEventHide})
	hudTick(p)
	if out := take(); count(out, v777.ClientboundPlayBossEvent) != 2 || len(st.bars) != 1 || st.on || sidebarShown(out) != "obj" {
		t.Errorf("boss bar gone: %d boss events, bars %q, sidebar %v %q", count(out, v777.ClientboundPlayBossEvent), st.bars, st.on, sidebarShown(out))
	}

	// Another server: everything goes.
	p.clearHudPanels()
	if out := take(); count(out, v777.ClientboundPlayBossEvent) != 1 || count(out, v777.ClientboundPlaySetActionBarText) != 1 || st.bar != "" {
		t.Errorf("cleared: %d boss events, %d action bars", count(out, v777.ClientboundPlayBossEvent), count(out, v777.ClientboundPlaySetActionBarText))
	}
}
