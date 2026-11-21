package bridge

import (
	"slices"
	"strings"

	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Chat both ways, system messages, titles, the action bar and toasts. Bedrock text uses § codes,
// which text.Legacy turns into Java components with the Bedrock palette. Scoreboards, boss bars
// and forms are later milestones (they register their own handlers).

// textState is the text the translator keeps between packets.
type textState struct {
	// deathMessage is the last death message the server sent (shown on the death screen).
	deathMessage string
}

func init() {
	OnBedrock(packet.IDText, (*Player).bedrockText)
	OnBedrock(packet.IDSetTitle, (*Player).bedrockTitle)
	OnBedrock(packet.IDToastRequest, func(p *Player, pk *packet.ToastRequest) {
		c := text.Component{Extra: []text.Component{
			{Bold: text.On, Extra: []text.Component{text.Legacy(rawText(pk.Title), text.Bedrock)}},
			text.Plain(" "),
			text.Legacy(rawText(pk.Message), text.Bedrock),
		}}
		p.sendSystem(&c, false)
	})
	OnBedrock(packet.IDCommandOutput, func(p *Player, pk *packet.CommandOutput) {
		for _, m := range pk.OutputMessages {
			msg := translate(m.Message, m.Parameters)
			if !m.Success {
				msg = "§c" + msg
			}
			p.Message(msg)
		}
	})
	OnBedrock(packet.IDDeathInfo, func(p *Player, pk *packet.DeathInfo) {
		p.text.deathMessage = translate(pk.Cause, pk.Messages)
	})

	OnJava(v777.ServerboundPlayChat, func(p *Player, r *wireReader) error {
		msg := r.String(256)
		if r.Err != nil {
			return nil // signed chat fields after the message are not needed
		}
		if p.rem == nil {
			p.BridgeMessage("Not connected to a server. Use §e/join <address | gamertag>§r.")
			return nil
		}
		p.rem.write(&packet.Text{
			TextType:   packet.TextTypeChat,
			SourceName: p.rem.conn.IdentityData().DisplayName,
			Message:    msg,
			XUID:       p.rem.conn.IdentityData().XUID,
		})
		return nil
	})
}

// bedrockText shows a Text packet: chat lines, or the action bar for popups and tips.
func (p *Player) bedrockText(pk *packet.Text) {
	msg := textMessage(pk)
	if msg == "" {
		return
	}
	switch pk.TextType {
	case packet.TextTypePopup, packet.TextTypeJukeboxPopup, packet.TextTypeTip:
		p.serverActionBar(msg)
	default:
		p.Message(msg)
	}
}

// textMessage is what a Text packet shows in Java chat (v1's).
func textMessage(pk *packet.Text) string {
	msg := pk.Message
	switch pk.TextType {
	case packet.TextTypeObject, packet.TextTypeObjectWhisper, packet.TextTypeObjectAnnouncement:
		return rawText(msg)
	}
	if pk.NeedsTranslation || pk.TextType == packet.TextTypeTranslation {
		msg = translate(msg, pk.Parameters)
	}
	switch pk.TextType {
	case packet.TextTypeChat:
		if pk.SourceName != "" {
			return "<" + pk.SourceName + "> " + msg
		}
	case packet.TextTypeWhisper:
		return "§7§o" + pk.SourceName + " whispers to you: " + msg
	case packet.TextTypeAnnouncement:
		if pk.SourceName == "" || strings.HasPrefix(msg, "["+pk.SourceName+"]") {
			return msg // /say already names its source
		}
		return "[" + pk.SourceName + "] " + msg
	}
	return msg
}

// bedrockTitle shows titles, subtitles and the action bar.
func (p *Player) bedrockTitle(pk *packet.SetTitle) {
	t := pk.Text
	switch pk.ActionType {
	case packet.TitleActionTitleTextObject, packet.TitleActionSubtitleTextObject, packet.TitleActionActionbarTextObject:
		t = rawText(t)
	}
	// A Bedrock pack with its own HUD gets its data in titles that hold its marker and never
	// shows them: on Java they would flash on screen as text.
	for _, m := range p.hudMarkers {
		if t != "" && strings.Contains(t, m) {
			if slices.Contains(p.hudPanels, m) {
				p.setHudPanel(m, strings.ReplaceAll(t, m, ""))
			}
			return
		}
	}
	switch pk.ActionType {
	case packet.TitleActionClear:
		p.sendClearTitles(false)
	case packet.TitleActionReset:
		p.sendClearTitles(true)
	case packet.TitleActionSetTitle, packet.TitleActionTitleTextObject:
		p.sendTitle(t)
	case packet.TitleActionSetSubtitle, packet.TitleActionSubtitleTextObject:
		p.sendSubtitle(t)
	case packet.TitleActionSetActionBar, packet.TitleActionActionbarTextObject:
		p.serverActionBar(t)
	case packet.TitleActionSetDurations:
		p.sendTitleTimes(pk.FadeInDuration, pk.RemainDuration, pk.FadeOutDuration)
	}
}

// commandVersion is the CommandRequest version the client sends.
const commandVersion = "latest"

// runServerCommand runs a command on the server ("/..." as typed).
func (p *Player) runServerCommand(line string) {
	if !strings.HasPrefix(line, "/") {
		line = "/" + line
	}
	p.Write(&packet.CommandRequest{
		CommandLine:   line,
		CommandOrigin: protocol.CommandOrigin{Origin: protocol.CommandOriginPlayer, UUID: uuid.New()},
		Version:       commandVersion,
	})
}
