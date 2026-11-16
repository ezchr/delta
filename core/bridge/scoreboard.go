package bridge

import (
	"strconv"
	"strings"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Scoreboards: the server's sidebar, tab list and below-name objectives as Java objectives.
//
// Bedrock lines are mostly fake players whose name is the text of the line and whose score orders
// the lines (Dragonfly and most plugins count up and sort ascending). Java always sorts the
// sidebar by descending score, so an objective shown ascending in the sidebar has its scores sent
// negated, with the real number as fixed text. Text lines show no number (as on Java servers).
//
// Score holders: a fake player's line is "bb<entry id>" with the line as display name (Bedrock
// lines may repeat the same text). A player's line is the player's Java profile name (the tab
// list and below-name slots look scores up by that name), shown with the Bedrock name; an
// entity's is its Java UUID, shown with its name tag or its type's name. Names come from the
// player list, AddPlayer and AddActor/SetActorData (remembered after the entity is gone); a line
// whose player or entity is not known yet says "Player Offline" (as a Bedrock client does) or
// "Entity", and is sent again when it becomes known.

type scoreboard struct {
	objectives map[string]*objective // by objective name
	entries    map[int64]*scoreEntry // by Bedrock entry id
	// names are the players' and entities' names by unique id: the Java profile name (holder)
	// and the text shown.
	names map[int64]scoreName
}

type objective struct {
	ascending bool           // sorted ascending in the sidebar
	slots     map[int32]bool // Java display slots it is shown in
}

type scoreEntry struct {
	e      protocol.ScoreboardEntry // as the server last sent it
	holder string                   // as the client has it
	shown  string                   // display name sent
}

type scoreName struct {
	holder, shown string
}

func (p *Player) board() *scoreboard {
	if p.sb == nil {
		p.sb = &scoreboard{objectives: map[string]*objective{}, entries: map[int64]*scoreEntry{}, names: map[int64]scoreName{}}
	}
	return p.sb
}

// javaDisplaySlot is the Java display slot of a Bedrock one.
func javaDisplaySlot(slot string) (int32, bool) {
	switch slot {
	case packet.ScoreboardSlotList:
		return 0, true
	case packet.ScoreboardSlotSidebar:
		return 1, true
	case packet.ScoreboardSlotBelowName:
		return 2, true
	}
	return 0, false
}

// negated reports whether the objective's scores are sent negated (ascending in the sidebar).
func (o *objective) negated() bool { return o.ascending && o.slots[1] }

func (p *Player) setDisplayObjective(pk *packet.SetDisplayObjective) {
	sb := p.board()
	slot, ok := javaDisplaySlot(pk.DisplaySlot)
	if !ok {
		return
	}
	// The slot shows something else now.
	for name, o := range sb.objectives {
		if name != pk.ObjectiveName && o.slots[slot] {
			was := o.negated()
			delete(o.slots, slot)
			if o.negated() != was {
				p.resendScores(name)
			}
		}
	}
	if pk.ObjectiveName == "" {
		p.sendDisplayObjective(slot, "") // the slot is cleared
		return
	}
	o := sb.objectives[pk.ObjectiveName]
	mode := byte(2)
	if o == nil {
		o = &objective{slots: map[int32]bool{}}
		sb.objectives[pk.ObjectiveName] = o
		mode = 0
	}
	was := o.negated()
	if slot == 1 {
		o.ascending = pk.SortOrder == packet.ScoreboardSortOrderAscending
	}
	w := p.pk()
	w.String(pk.ObjectiveName)
	w.Byte(mode)
	writeText(w, pk.DisplayName)
	w.VarInt(0)   // integer render type
	w.Bool(false) // default number format (styled per slot; scores may set their own)
	p.send(v777.ClientboundPlaySetObjective, w)
	o.slots[slot] = true
	p.sendDisplayObjective(slot, pk.ObjectiveName)
	if mode == 2 && o.negated() != was {
		p.resendScores(pk.ObjectiveName)
	}
}

func (p *Player) sendDisplayObjective(slot int32, name string) {
	w := p.pk()
	w.VarInt(slot)
	w.String(name)
	p.send(v777.ClientboundPlaySetDisplayObjective, w)
}

func (p *Player) removeObjective(name string) {
	sb := p.board()
	if _, ok := sb.objectives[name]; !ok {
		return
	}
	delete(sb.objectives, name)
	for id, e := range sb.entries {
		if e.e.ObjectiveName == name {
			delete(sb.entries, id)
		}
	}
	w := p.pk()
	w.String(name)
	w.Byte(1)
	p.send(v777.ClientboundPlaySetObjective, w)
}

func (p *Player) setScore(pk *packet.SetScore) {
	sb := p.board()
	for _, e := range pk.Entries {
		if e.IdentityType == protocol.ScoreboardIdentityRemove {
			if old, ok := sb.entries[e.EntryID]; ok && (e.ObjectiveName == "" || e.ObjectiveName == old.e.ObjectiveName) {
				delete(sb.entries, e.EntryID)
				p.resetScore(old.holder, old.e.ObjectiveName)
			}
			continue
		}
		if sb.objectives[e.ObjectiveName] == nil {
			continue // not shown: Java needs the objective first
		}
		old := sb.entries[e.EntryID]
		if old != nil && old.e.ObjectiveName != e.ObjectiveName {
			p.resetScore(old.holder, old.e.ObjectiveName)
			old = nil
		}
		if old == nil {
			old = &scoreEntry{}
			sb.entries[e.EntryID] = old
		}
		old.e = e
		p.sendScore(old)
	}
}

// sendScore sends an entry with its holder and name as they are known now.
func (p *Player) sendScore(se *scoreEntry) {
	e := se.e
	o := p.board().objectives[e.ObjectiveName]
	if o == nil {
		return
	}
	holder, shown := "bb"+strconv.FormatInt(e.EntryID, 36), e.DisplayName
	if e.IdentityType != protocol.ScoreboardIdentityFakePlayer || shown == "" {
		holder, shown = p.scoreHolder(e)
	}
	if se.holder != "" && se.holder != holder {
		p.resetScore(se.holder, e.ObjectiveName)
	}
	se.holder, se.shown = holder, shown
	value := e.Score
	if o.negated() {
		value = -value
	}
	w := p.pk()
	w.String(holder)
	w.String(e.ObjectiveName)
	w.VarInt(value)
	w.Bool(true)
	writeText(w, shown)
	switch {
	case e.IdentityType == protocol.ScoreboardIdentityFakePlayer:
		w.Bool(true)
		w.VarInt(0) // blank: a text line (its score only orders the lines)
	case o.negated():
		w.Bool(true)
		w.VarInt(2) // fixed: the server's number, as the slot's default style would show it
		writeText(w, "§c"+strconv.Itoa(int(e.Score)))
	default:
		w.Bool(false) // the slot's own style (red in the sidebar, yellow in the tab list)
	}
	p.send(v777.ClientboundPlaySetScore, w)
}

// resendScores sends every entry of an objective again (its numbers changed sign).
func (p *Player) resendScores(name string) {
	for _, se := range p.board().entries {
		if se.e.ObjectiveName == name {
			p.sendScore(se)
		}
	}
}

// scoreHolder is the holder and name of a player's or entity's line.
func (p *Player) scoreHolder(e protocol.ScoreboardEntry) (holder, shown string) {
	player := e.IdentityType == protocol.ScoreboardIdentityPlayer
	if p.rem != nil && e.EntityUniqueID == p.rem.gd.EntityUniqueID {
		return p.profile.Name, p.profile.Name
	}
	if n, ok := p.lookupScoreName(e.EntityUniqueID, player); ok {
		return n.holder, n.shown
	}
	if player {
		return "bb" + strconv.FormatInt(e.EntryID, 36), "Player Offline"
	}
	return "bb" + strconv.FormatInt(e.EntryID, 36), "Entity"
}

// lookupScoreName finds a player's or entity's name: the entity the client has, the tab list,
// or the names remembered.
func (p *Player) lookupScoreName(uniq int64, player bool) (scoreName, bool) {
	sb := p.board()
	if p.ents != nil {
		if rid, ok := p.ents.RuntimeOf(uniq); ok {
			if en := p.ents.shown[rid]; en != nil {
				if n, ok := p.entityScoreName(en); ok {
					sb.names[uniq] = n
					return n, true
				}
			}
		}
		if player {
			for _, te := range p.ents.tab {
				if te.uniq == uniq && te.uniq != 0 {
					n := scoreName{holder: te.name, shown: te.name}
					if te.display != "" {
						n.shown = te.display
					}
					sb.names[uniq] = n
					return n, true
				}
			}
		}
	}
	n, ok := sb.names[uniq]
	return n, ok
}

// entityScoreName is the holder and name of an entity's line: a player's profile name and
// Bedrock name, else the entity's UUID and its name tag (its type's name without one).
func (p *Player) entityScoreName(en *entity) (scoreName, bool) {
	if en.bedrock == "minecraft:player" {
		te := p.ents.tab[en.uuid]
		if te == nil {
			return scoreName{}, false
		}
		n := scoreName{holder: te.name, shown: te.name}
		if te.display != "" {
			n.shown = te.display
		}
		return n, true
	}
	shown, _ := en.meta[protocol.EntityDataKeyName].(string)
	if shown == "" {
		shown = entityTypeName(en.bedrock)
	}
	return scoreName{holder: en.uuid.String(), shown: shown}, true
}

// entityTypeName is the English name of a Bedrock entity type ("minecraft:cow" -> "Cow").
func entityTypeName(id string) string {
	short := strings.TrimPrefix(id, "minecraft:")
	if s := translate("entity."+short+".name", nil); s != "entity."+short+".name" {
		return s
	}
	words := strings.Split(short, "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// scoreNamesChanged sends again the lines of the player or entity uniq (its name became known
// or changed).
func (p *Player) scoreNamesChanged(uniq int64) {
	if p.sb == nil {
		return
	}
	for _, se := range p.sb.entries {
		if se.e.IdentityType == protocol.ScoreboardIdentityFakePlayer || se.e.EntityUniqueID != uniq {
			continue
		}
		if h, s := p.scoreHolder(se.e); h != se.holder || s != se.shown {
			p.sendScore(se)
		}
	}
}

// hasEntityLines reports whether any line is a player's or entity's.
func (sb *scoreboard) hasEntityLines() bool {
	if sb == nil {
		return false
	}
	for _, se := range sb.entries {
		if se.e.IdentityType != protocol.ScoreboardIdentityFakePlayer {
			return true
		}
	}
	return false
}

func (p *Player) resetScore(holder, objective string) {
	w := p.pk()
	w.String(holder)
	w.Bool(true)
	w.String(objective)
	p.send(v777.ClientboundPlayResetScore, w)
}

// clearScoreboard removes every objective (a new server).
func (p *Player) clearScoreboard() {
	if p.sb == nil {
		return
	}
	for name := range p.sb.objectives {
		p.removeObjective(name)
	}
	p.sb = nil
}

func init() {
	OnBedrock(packet.IDSetDisplayObjective, (*Player).setDisplayObjective)
	OnBedrock(packet.IDRemoveObjective, func(p *Player, pk *packet.RemoveObjective) { p.removeObjective(pk.ObjectiveName) })
	OnBedrock(packet.IDSetScore, (*Player).setScore)
	// Names that become known (these run after the entity handlers: file order).
	OnBedrock(packet.IDPlayerList, func(p *Player, pk *packet.PlayerList) {
		if !p.sb.hasEntityLines() {
			return
		}
		for _, en := range pk.Entries {
			if en.ActionType == protocol.PlayerListActionAdd {
				p.scoreNamesChanged(en.EntityUniqueID)
			}
		}
	})
	OnBedrock(packet.IDAddPlayer, func(p *Player, pk *packet.AddPlayer) {
		if p.sb.hasEntityLines() {
			p.scoreNamesChanged(pk.AbilityData.EntityUniqueID)
		}
	})
	OnBedrock(packet.IDAddActor, func(p *Player, pk *packet.AddActor) {
		if p.sb.hasEntityLines() {
			p.scoreNamesChanged(pk.EntityUniqueID)
		}
	})
	OnBedrock(packet.IDSetActorData, func(p *Player, pk *packet.SetActorData) {
		if _, ok := pk.EntityMetadata[protocol.EntityDataKeyName]; !ok || !p.sb.hasEntityLines() {
			return
		}
		if en := p.ents.shown[pk.EntityRuntimeID]; en != nil {
			p.scoreNamesChanged(en.uniq)
		}
	})
	OnReset(func(p *Player, newConn bool) {
		if newConn {
			p.clearScoreboard()
		}
	})
}
