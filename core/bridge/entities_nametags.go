package bridge

import (
	"strconv"
	"strings"
	"unicode/utf8"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Name tags of other players. A Java client draws a player's profile name (the tab entry,
// 16 characters, no formatting) above the head, formatted by the scoreboard team the name is in.
// Bedrock servers set any name tag (ranks, colours, several lines). Each player shown gets a team
// of its own:
//
//   - a one-line tag that contains the profile name becomes the team's prefix and suffix, the
//     colour at the name the team colour (Java draws prefix + name in the team colour + suffix);
//   - any other tag (several lines, the name missing or coloured in parts) hides the Java tag
//     (team visibility never) and shows a text_display riding the player, at the height of a name
//     tag, with the whole text (multi-line text grows upwards like a Bedrock tag);
//   - an empty tag, an invisible player or always-show off hides it.
//
// The tab list keeps the Bedrock user name (a display name, so the team does not format it).

// Java team packet methods and name tag visibilities.
const (
	teamCreate = 0
	teamRemove = 1
	teamUpdate = 2
	tagAlways  = 0
	tagNever   = 1
)

// Text display entity data: translation 11, billboard 15, brightness 16, text 23, style 27.
const (
	serComponent     = 5
	serVector3       = 39
	textBillboardAll = 3
	textSeeThrough   = 0x02
	textDefaultBack  = 0x04
	// textLift puts the text's bottom where the Java name tag's is: the tag is drawn from 0.5
	// above the head (where a passenger sits) down one line of 9 pixels at 1/40 block.
	textLift = 0.5 - 9*0.025
)

// nameTag is how a player's name tag is shown.
type nameTag struct {
	key     string // what it was made from (tag, flags); "" = not set up
	team    string // the team's name, "" if none was made
	display int32  // the text_display's Java id, 0 if none
	text    string // the display's text
	sneak   bool   // the display was sent for a sneaking player (not see-through)
}

// teamName is the team of a player entity.
func teamName(e *entity) string { return "bb2_" + strconv.Itoa(int(e.id)) }

func init() {
	OnReset(func(p *Player, newConn bool) {
		// The Java client keeps teams over respawns, and the made-up text displays are not in
		// the entity map.
		if len(p.ents.tagDisplays) > 0 {
			w := p.pk()
			w.VarInt(int32(len(p.ents.tagDisplays)))
			for id := range p.ents.tagDisplays {
				w.VarInt(id)
			}
			p.send(v777.ClientboundPlayRemoveEntities, w)
			clear(p.ents.tagDisplays)
		}
		for name := range p.ents.teams {
			p.sendTeamRemove(name)
		}
		clear(p.ents.teams)
	})
}

// updateNameTag shows a player entity's name tag as the server has it now.
func (p *Player) updateNameTag(e *entity) {
	if e.t.kind&entPlayer == 0 || !e.spawned {
		return
	}
	te := p.ents.tab[e.uuid]
	if te == nil {
		return
	}
	tag, ok := e.meta[protocol.EntityDataKeyName].(string)
	if !ok {
		tag = te.display
		if tag == "" {
			tag = te.name
		}
	}
	tag = strings.TrimRight(tag, "\n")
	// Bedrock shows a player's tag with always-show off only while looked at; Java cannot.
	always := true
	if _, ok := e.meta[protocol.EntityDataKeyAlwaysShowNameTag]; ok {
		always = metaInt(e.meta, protocol.EntityDataKeyAlwaysShowNameTag) != 0
	}
	hidden := tag == "" || !always || e.flag(protocol.EntityDataFlagInvisible)
	sneak := e.flag(protocol.EntityDataFlagSneaking)
	key := tag
	if hidden {
		key = "\x00hidden"
	}
	nt := &e.tag
	if nt.key == key && (nt.display == 0 || nt.sneak == sneak) {
		return
	}
	nt.key = key

	prefix, suffix, colour, inTeam := teamSplit(tag, te.name)
	visibility := int32(tagAlways)
	if hidden || !inTeam {
		visibility, prefix, suffix, colour = tagNever, "", "", -1
	}
	if prefix != "" || suffix != "" || colour >= 0 {
		p.keepTabName(te)
	}
	p.sendTeam(e, te, visibility, prefix, suffix, colour)

	if hidden || inTeam {
		p.removeTagDisplay(e, true)
		return
	}
	p.showTagDisplay(e, tag, sneak)
}

// keepTabName gives a tab entry its name as a display name, so the team's prefix does not show
// in the tab list (a Bedrock player list shows user names).
func (p *Player) keepTabName(te *tabEntry) {
	if te.display != "" || !te.sent {
		return
	}
	te.display = te.name
	w := p.pk()
	w.Byte(tabDisplay)
	w.VarInt(1)
	w.UUID(te.id)
	w.Bool(true)
	writeText(w, te.display)
	p.send(v777.ClientboundPlayPlayerInfoUpdate, w)
}

// sendTeam creates or updates a player's team.
func (p *Player) sendTeam(e *entity, te *tabEntry, visibility int32, prefix, suffix string, colour int32) {
	name := teamName(e)
	method := byte(teamUpdate)
	if e.tag.team != name {
		method = teamCreate
		e.tag.team = name
		if p.ents.teams == nil {
			p.ents.teams = map[string]bool{}
		}
		p.ents.teams[name] = true
	}
	w := p.pk()
	w.String(name)
	w.Byte(method)
	writeText(w, "")
	writeText(w, prefix)
	writeText(w, suffix)
	w.VarInt(visibility)
	w.VarInt(0) // collision: always (the Java client pushes itself away from players)
	w.Bool(colour >= 0)
	if colour >= 0 {
		w.VarInt(colour)
	}
	w.Byte(0x02) // see invisible teammates: off for others, harmless
	if method == teamCreate {
		w.VarInt(1)
		w.String(te.name)
	}
	p.send(v777.ClientboundPlaySetPlayerTeam, w)
}

func (p *Player) sendTeamRemove(name string) {
	w := p.pk()
	w.String(name)
	w.Byte(teamRemove)
	p.send(v777.ClientboundPlaySetPlayerTeam, w)
}

// dropNameTag removes a player's team and text display (the player was removed).
func (p *Player) dropNameTag(e *entity) {
	p.removeTagDisplay(e, false)
	if e.tag.team != "" {
		p.sendTeamRemove(e.tag.team)
		delete(p.ents.teams, e.tag.team)
	}
	e.tag = nameTag{}
}

// showTagDisplay shows (or updates) the text display that carries a player's name tag.
func (p *Player) showTagDisplay(e *entity, tag string, sneak bool) {
	nt := &e.tag
	typ := p.ver().BuiltinID("minecraft:entity_type", "minecraft:text_display")
	if typ < 0 {
		return
	}
	isNew := nt.display == 0
	if isNew {
		nt.display = p.ents.next
		p.ents.next++
		if p.ents.tagDisplays == nil {
			p.ents.tagDisplays = map[int32]bool{}
		}
		p.ents.tagDisplays[nt.display] = true
		w := p.pk()
		w.VarInt(nt.display)
		w.UUID(entityUUID(uint64(nt.display) | 1<<62))
		w.VarInt(typ)
		w.Float64(e.pos[0])
		w.Float64(e.pos[1] + 1.8)
		w.Float64(e.pos[2])
		w.LpVec3(0, 0, 0)
		w.Angle(0)
		w.Angle(0)
		w.Angle(0)
		w.VarInt(0)
		p.send(v777.ClientboundPlayAddEntity, w)
	}
	w := p.pk()
	w.VarInt(nt.display)
	if isNew {
		w.Byte(11)
		w.VarInt(serVector3)
		w.Float32(0)
		w.Float32(float32(textLift))
		w.Float32(0)
		w.Byte(15)
		w.VarInt(serByte)
		w.Byte(textBillboardAll)
		w.Byte(16)
		w.VarInt(serInt)
		w.VarInt(15<<4 | 15<<20) // full bright, as name tags are drawn
	}
	if isNew || nt.text != tag {
		w.Byte(23)
		w.VarInt(serComponent)
		writeText(w, tag)
	}
	if isNew || nt.sneak != sneak {
		style := byte(textDefaultBack)
		if !sneak {
			style |= textSeeThrough // as a name tag: through walls unless sneaking
		}
		w.Byte(27)
		w.VarInt(serByte)
		w.Byte(style)
	}
	w.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, w)
	nt.text, nt.sneak = tag, sneak
	if isNew {
		p.sendPassengers(e)
	}
}

// removeTagDisplay removes a player's name tag display; resend: send the player's other riders
// (the player stays).
func (p *Player) removeTagDisplay(e *entity, resend bool) {
	nt := &e.tag
	if nt.display == 0 {
		return
	}
	w := p.pk()
	w.VarInt(1)
	w.VarInt(nt.display)
	p.send(v777.ClientboundPlayRemoveEntities, w)
	delete(p.ents.tagDisplays, nt.display)
	nt.display, nt.text = 0, ""
	if resend && e.spawned {
		p.sendPassengers(e)
	}
}

// teamSplit splits a one-line Bedrock name tag around the profile name: the text before it
// (prefix), after it (suffix, starting with the formatting in force there) and the colour of the
// name (a Java colour id, -1 none). ok is false if the tag cannot be shown that way.
func teamSplit(tag, name string) (prefix, suffix string, colour int32, ok bool) {
	if name == "" || strings.Contains(tag, "\n") {
		return "", "", -1, false
	}
	// The plain text and, for each of its characters, the byte offset in tag.
	var plain []rune
	var at []int
	for i := 0; i < len(tag); {
		r, n := utf8.DecodeRuneInString(tag[i:])
		if r == '§' {
			i += n
			if i < len(tag) {
				_, n2 := utf8.DecodeRuneInString(tag[i:])
				i += n2
			}
			continue
		}
		plain = append(plain, r)
		at = append(at, i)
		i += n
	}
	nr := []rune(name)
	idx := -1
	for i := 0; i+len(nr) <= len(plain); i++ {
		if string(plain[i:i+len(nr)]) == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", "", -1, false
	}
	start := at[idx]
	last := at[idx+len(nr)-1]
	_, ln := utf8.DecodeRuneInString(tag[last:])
	end := last + ln
	if strings.ContainsRune(tag[start:end], '§') {
		return "", "", -1, false // formatting inside the name: Java gives it one colour
	}
	codes := activeCodes(tag[:start])
	colour = -1
	if len(codes) > 0 {
		if c, ok := javaColour(codes[0]); ok {
			colour = c
		}
	}
	prefix = tag[:start]
	if rest := tag[end:]; rest != "" {
		var sb strings.Builder
		for _, c := range codes {
			sb.WriteRune('§')
			sb.WriteRune(c)
		}
		sb.WriteString(rest)
		suffix = sb.String()
	}
	return prefix, suffix, colour, true
}

// activeCodes are the formatting codes in force at the end of s: the last colour (or reset)
// first, then the format codes after it.
func activeCodes(s string) []rune {
	var codes []rune
	rs := []rune(s)
	for i := 0; i+1 < len(rs); i++ {
		if rs[i] != '§' {
			continue
		}
		c := rs[i+1]
		i++
		if _, isColour := javaColour(c); isColour || c == 'r' {
			codes = codes[:0]
			if c != 'r' {
				codes = append(codes, c)
			}
			continue
		}
		codes = append(codes, c)
	}
	return codes
}

// javaColour is the Java colour (ChatFormatting id 0-15) of a Bedrock colour code; Bedrock's
// material colours get the nearest one.
func javaColour(c rune) (int32, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	switch c {
	case 'g', 'n', 'p': // minecoin gold, copper, gold
		return 6, true
	case 'h': // quartz
		return 15, true
	case 'i': // iron
		return 7, true
	case 'j': // netherite
		return 8, true
	case 'm': // redstone
		return 4, true
	case 'q': // emerald
		return 2, true
	case 's': // diamond
		return 3, true
	case 't': // lapis
		return 1, true
	case 'u': // amethyst
		return 5, true
	}
	return 0, false
}
