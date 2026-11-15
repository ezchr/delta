package bridge

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/java/protocol/text"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// NPC dialogues. minecraft:npc is shown as a plain villager (entities_types.go, entities_meta.go:
// plains type, no profession; Bedrock's NPC skins are people, none of them a villager look) and a
// Java interact on it is the usual UseItemOnEntity interact, which makes BDS send NPCDialogue.
//
// What BDS 1.21.3 does (decompile: NpcComponent, npc::handlePacket, NpcDialoguePacket):
//
//   - Interact (NpcComponent::getInteraction's capture) sends NPCDialogue open with the NPC's unique
//     id. Only when the player has a scene for that NPC (/dialogue open|change) are the scene's
//     text, scene name, NPC name and action JSON filled in (initializePacket); otherwise all of them
//     are empty and the client takes the NPC's entity data: the name tag, InteractText (data 100)
//     and Actions (data 41, the same JSON).
//   - NPCDialogue close (NpcSystemHelper::sendClosePacket) carries only the unique id.
//   - The action JSON is a list of {"type": 1 command | 0 url, "mode": 0 button | 1 on close |
//     2 on open, "text", "button_name", "data": [{"cmd_line", "cmd_ver"}]}. Buttons are the mode 0
//     entries, and ExecuteAction's ActionType is the entry's index in the whole list.
//   - NPCRequest (runtime id, RequestType, CommandString, ActionType, SceneName): the server takes
//     ExecuteAction (1), ExecuteClosingCommands (2) and ExecuteOpeningCommands (6) from anyone (the
//     others need world builder); the scene name picks the scene's actions, empty = the NPC's own.
//     Opening/closing commands only ever run on these requests, so the client sends them when its
//     screen opens and closes.
//
// On Java the dialogue is a dialog (show_dialog, like forms.go): title the NPC name, body the
// dialogue text, a button per NPC button, Escape = close. Opening sends ExecuteOpeningCommands;
// a button sends ExecuteAction and the dialog waits (after_action wait_for_response) for the
// server, which decides what follows: BDS runs the button's commands and closes the dialogue
// with NPCDialogue close ~0.3 s later (seen on 1.26.60), unless the commands opened another one
// (/dialogue open: the next page), which replaces it. A server that sends neither within
// npcAnswerWait gets the same dialog back (a Bedrock client's screen stays open). Escape sends
// ExecuteClosingCommands; a close by the server sends nothing. URL buttons put the link in chat
// (Bedrock opens it in the browser; the server does nothing for them).
//
// Names: the server keeps an NPC's name as raw text in NameRawText (data 112, NpcComponent::setName,
// also what /dialogue change sets from the scene's npc_name); Bedrock clients evaluate it into the
// name tag they show, so the villager's custom name and the dialog title use it (shownName).

const npcAction = ProductName + ":npc"

// npcAnswerWait is how long a clicked button waits for the server before the dialog comes back.
const npcAnswerWait = 2 * time.Second

// npcState is the NPC dialogue the Java client has open (p.mu).
type npcState struct {
	open    bool
	id      int32  // dialog number, in the buttons' payload (stale clicks are ignored)
	rid     uint64 // the NPC's runtime id
	uniq    int64  // and unique id
	scene   string // scene name the server sent (echoed in every request)
	buttons []npcButton
	// title and body of the dialog shown (Bedrock text), to show it again.
	title, body string
	// waitUntil: a button was clicked; the dialog comes back then unless the server answers.
	waitUntil time.Time
}

type npcButton struct {
	index int    // index in the action list (ExecuteAction's ActionType)
	label string // Bedrock text
	url   string // URL buttons
}

// npcActionEntry is one entry of the action JSON.
type npcActionEntry struct {
	Type       int    `json:"type"`
	Mode       int    `json:"mode"`
	Text       string `json:"text"`
	ButtonName string `json:"button_name"`
	Data       []struct {
		CmdLine string `json:"cmd_line"`
	} `json:"data"`
}

func init() {
	OnBedrock(packet.IDNPCDialogue, (*Player).handleNPCDialogue)
	OnJavaStop(v777.ServerboundPlayCustomClickAction, func(p *Player, r *wireReader) (bool, error) {
		id := r.String(32767)
		if r.Err != nil || id != npcAction {
			return false, nil
		}
		p.npcClicked(r.Rest())
		return true, nil
	})
	OnTick((*Player).npcTick)
	OnReset(func(p *Player, newConn bool) {
		if p.npc.open {
			p.send(v777.ClientboundPlayClearDialog, p.pk())
		}
		id := p.npc.id
		p.npc = npcState{id: id}
	})
}

// npcTick shows the dialog again when the server did not answer a button.
func (p *Player) npcTick() {
	n := &p.npc
	if n.open && !n.waitUntil.IsZero() && time.Now().After(n.waitUntil) {
		n.waitUntil = time.Time{}
		p.showNPCDialog()
	}
}

// handleNPCDialogue opens, replaces or closes the NPC dialog.
func (p *Player) handleNPCDialogue(pk *packet.NPCDialogue) {
	uniq := int64(pk.EntityUniqueID)
	n := &p.npc
	if pk.ActionType == packet.NPCDialogueActionClose {
		if n.open && n.uniq == uniq {
			p.log.Debug("npc dialogue closed by the server", "npc", uniq)
			p.npcEnd(false)
		}
		return
	}
	if pk.ActionType != packet.NPCDialogueActionOpen {
		return
	}
	rid, ok := p.ents.RuntimeOf(uniq)
	if !ok {
		p.log.Debug("npc dialogue for an unknown entity", "npc", uniq)
		return
	}
	var meta protocol.EntityMetadata
	name := ""
	if en := p.ents.shown[rid]; en != nil {
		meta = en.meta
		name = shownName(en, meta)
	}
	dialogue, actions := pk.Dialogue, pk.ActionJSON
	if pk.NPCName != "" {
		name = rawText(pk.NPCName)
	}
	if pk.SceneName == "" {
		// No scene: the NPC's own text and buttons (entity data), as a Bedrock client shows them.
		if dialogue == "" {
			dialogue, _ = meta[protocol.EntityDataKeyInteractText].(string)
		}
		if actions == "" {
			actions, _ = meta[protocol.EntityDataKeyActions].(string)
		}
	}
	if name == "" {
		name = "NPC"
	}
	buttons := npcButtons(actions)

	n.id++
	n.open, n.rid, n.uniq, n.scene, n.buttons, n.waitUntil = true, rid, uniq, pk.SceneName, buttons, time.Time{}
	n.title, n.body = strings.TrimSpace(name), strings.TrimRight(rawText(dialogue), "\n")
	p.showNPCDialog()
	p.log.Debug("npc dialogue", "npc", uniq, "scene", pk.SceneName, "name", name, "buttons", len(buttons), "actions", actions)
	p.npcRequest(packet.NPCRequestActionExecuteOpeningCommands, 0)
}

// npcButtons reads the buttons (mode 0) of an action list.
func npcButtons(actions string) []npcButton {
	if strings.TrimSpace(actions) == "" {
		return nil
	}
	var list []npcActionEntry
	if json.Unmarshal([]byte(actions), &list) != nil {
		return nil
	}
	var out []npcButton
	for i, a := range list {
		if a.Mode != 0 || i > 255 { // ActionType is a byte
			continue
		}
		b := npcButton{index: i, label: rawText(a.ButtonName)}
		if a.Type == 0 {
			b.url = a.Text
		}
		if b.label == "" {
			b.label = a.Text
			if b.label == "" && len(a.Data) > 0 {
				b.label = a.Data[0].CmdLine
			}
		}
		out = append(out, b)
	}
	return out
}

// shownName is the name an entity shows: an NPC's evaluated NameRawText when it has one (see the
// file header), else the name tag.
func shownName(e *entity, m protocol.EntityMetadata) string {
	if e.bedrock == "minecraft:npc" {
		if raw, _ := m[protocol.EntityDataKeyNameRawText].(string); strings.TrimSpace(raw) != "" {
			return strings.TrimRight(rawText(raw), "\n")
		}
	}
	name, _ := m[protocol.EntityDataKeyName].(string)
	return name
}

// showNPCDialog sends the open dialogue as a multi_action dialog.
func (p *Player) showNPCDialog() {
	n := &p.npc
	w := p.pk()
	w.VarInt(0) // a dialog given inline
	w.Byte(text.TagCompound)
	text.StringTag(w, "type", "minecraft:multi_action")
	title := bedrockText(n.title)
	text.ComponentTag(w, "title", &title)
	text.BoolTag(w, "pause", false)
	text.StringTag(w, "after_action", "wait_for_response")
	writeBody(w, appendBody(nil, n.body))
	if len(n.buttons) == 0 {
		text.ListStart(w, "actions", text.TagCompound, 1)
		writeNPCButton(w, text.Translatable("gui.done"), n.id, -2)
	} else {
		text.ListStart(w, "actions", text.TagCompound, len(n.buttons))
		for i, b := range n.buttons {
			writeNPCButton(w, labelText(b.label), n.id, int32(i))
		}
	}
	text.IntTag(w, "columns", 1)
	text.Key(w, text.TagCompound, "exit_action")
	writeNPCButton(w, text.Translatable("gui.cancel"), n.id, -2)
	w.Byte(text.TagEnd)
	p.send(v777.ClientboundPlayShowDialog, w)
}

// writeNPCButton is writeButton (forms.go) with the NPC action: payload {n: dialog, b: button}
// (b -2: closed).
func writeNPCButton(w *wire.Writer, label text.Component, id, button int32) {
	text.ComponentTag(w, "label", &label)
	text.Key(w, text.TagCompound, "action")
	text.StringTag(w, "type", "minecraft:dynamic/custom")
	text.StringTag(w, "id", npcAction)
	text.Key(w, text.TagCompound, "additions")
	text.IntTag(w, "n", id)
	text.IntTag(w, "b", button)
	w.Byte(text.TagEnd)
	w.Byte(text.TagEnd)
	w.Byte(text.TagEnd)
}

// npcClicked handles a dialog button or the dialog's close.
func (p *Player) npcClicked(payload []byte) {
	vals, err := readClickPayload(payload)
	if err != nil {
		p.log.Debug("npc click payload", "err", err)
		return
	}
	id, _ := vals["n"].(int32)
	button, _ := vals["b"].(int32)
	n := &p.npc
	if !n.open || id != n.id || !n.waitUntil.IsZero() {
		return // an old dialog, or one already answered
	}
	if button < 0 || int(button) >= len(n.buttons) {
		p.log.Debug("npc dialogue closed", "npc", n.uniq)
		p.npcEnd(true)
		return
	}
	b := n.buttons[button]
	if b.url != "" {
		p.Message("§7" + b.label + ": §9§n" + b.url)
		p.npcEnd(true)
		return
	}
	p.log.Debug("npc button", "npc", n.uniq, "button", button, "action", b.index)
	p.npcRequest(packet.NPCRequestActionExecuteAction, byte(b.index))
	n.waitUntil = time.Now().Add(npcAnswerWait)
}

// npcEnd closes the dialog; closing sends ExecuteClosingCommands (a close by the player, not by
// the server).
func (p *Player) npcEnd(closing bool) {
	n := &p.npc
	if !n.open {
		return
	}
	if closing {
		p.npcRequest(packet.NPCRequestActionExecuteClosingCommands, 0)
	}
	p.send(v777.ClientboundPlayClearDialog, p.pk())
	n.open, n.buttons, n.waitUntil = false, nil, time.Time{}
}

// npcRequest sends an NPCRequest about the open dialogue's NPC.
func (p *Player) npcRequest(typ byte, action byte) {
	n := &p.npc
	p.Write(&packet.NPCRequest{EntityRuntimeID: n.rid, RequestType: typ, ActionType: action, SceneName: n.scene})
}
