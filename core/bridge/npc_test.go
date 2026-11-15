package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/java/protocol/text"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/version"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// An NPC's action list as BDS 1.26.60 keeps it in Actions (data 41), with an on-open (mode 2)
// and an on-close (mode 1) entry around the buttons, and a URL button.
const testNPCActions = `[
 {"button_name":"Say hi","data":[{"cmd_line":"/say hi","cmd_ver":39}],"mode":0,"text":"/say hi\n","type":1},
 {"button_name":"","data":[{"cmd_line":"/tag @initiator add x","cmd_ver":39}],"mode":2,"text":"/tag @initiator add x\n","type":1},
 {"button_name":"Next","data":[{"cmd_line":"/dialogue open @s @initiator p2","cmd_ver":39}],"mode":0,"text":"/dialogue open @s @initiator p2\n","type":1},
 {"button_name":"","data":[{"cmd_line":"/say bye","cmd_ver":39}],"mode":1,"text":"/say bye\n","type":1},
 {"button_name":"Site","mode":0,"text":"https://example.com","type":0}
]`

func npcClick(t *testing.T, p *Player, button int32) { npcClickID(t, p, p.npc.id, button) }

func npcClickID(t *testing.T, p *Player, id, button int32) {
	t.Helper()
	pl := &wire.Writer{}
	pl.Byte(text.TagCompound)
	text.IntTag(pl, "n", id)
	text.IntTag(pl, "b", button)
	pl.Byte(text.TagEnd)
	w := &wire.Writer{}
	w.VarInt(int32(len(pl.B)))
	w.Raw(pl.B)
	p.npcClicked(w.B)
}

func TestNPCDialogue(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	p.rem = nil // p.Write is a no-op without a connection
	en := &entity{rid: 7, uniq: -7, id: p.ents.JavaID(7), bedrock: "minecraft:npc", meta: protocol.EntityMetadata{
		protocol.EntityDataKeyName:         "NPC",
		protocol.EntityDataKeyNameRawText:  `{"rawtext":[{"text":"Guide"}]}`,
		protocol.EntityDataKeyInteractText: "Hello!",
		protocol.EntityDataKeyActions:      testNPCActions,
	}}
	p.ents.shown[7] = en
	p.ents.SetUnique(-7, 7)
	if got := shownName(en, en.meta); got != "Guide" {
		t.Errorf("shown name %q", got)
	}

	// No scene: the NPC's entity data.
	p.handleNPCDialogue(&packet.NPCDialogue{EntityUniqueID: uint64(1<<64 - 7), ActionType: packet.NPCDialogueActionOpen})
	out := take()
	if len(out) != 1 || out[0].id != v777.ClientboundPlayShowDialog {
		t.Fatalf("open: %d packets", len(out))
	}
	body := string(out[0].w.B)
	for _, want := range []string{"Guide", "Hello!", "Say hi", "Next", "Site", "wait_for_response"} {
		if !strings.Contains(body, want) {
			t.Errorf("dialog lacks %q", want)
		}
	}
	if len(p.npc.buttons) != 3 || p.npc.buttons[0].index != 0 || p.npc.buttons[1].index != 2 || p.npc.buttons[2].index != 4 || p.npc.buttons[2].url == "" {
		t.Fatalf("buttons %+v", p.npc.buttons)
	}

	// A button waits for the server; no answer shows the dialog again.
	npcClick(t, p, 1)
	if p.npc.waitUntil.IsZero() || len(take()) != 0 {
		t.Fatal("button: not waiting")
	}
	npcClick(t, p, 0) // clicks while waiting are ignored
	p.npc.waitUntil = time.Now().Add(-time.Millisecond)
	p.npcTick()
	if out := take(); len(out) != 1 || out[0].id != v777.ClientboundPlayShowDialog || !p.npc.waitUntil.IsZero() {
		t.Fatalf("no answer: %d packets", len(out))
	}

	// A scene from the server replaces it (the packet's fields, not the entity data).
	old := p.npc.id
	p.handleNPCDialogue(&packet.NPCDialogue{EntityUniqueID: uint64(1<<64 - 7), ActionType: packet.NPCDialogueActionOpen,
		SceneName: "p2", NPCName: `{"rawtext":[{"text":"Guide 2"}]}` + "\n", Dialogue: "Page two",
		ActionJSON: `[{"button_name":"Done","data":[{"cmd_line":"/say done","cmd_ver":39}],"mode":0,"text":"/say done\n","type":1}]`})
	out = take()
	if len(out) != 1 || p.npc.id == old || p.npc.scene != "p2" || p.npc.title != "Guide 2" || len(p.npc.buttons) != 1 {
		t.Fatalf("scene: %d packets, %+v", len(out), p.npc)
	}
	if b := string(out[0].w.B); !strings.Contains(b, "Page two") || strings.Contains(b, "Hello!") {
		t.Errorf("scene dialog body")
	}
	// An old dialog's click is ignored.
	npcClickID(t, p, old, 0)
	if !p.npc.waitUntil.IsZero() {
		t.Error("stale click answered")
	}

	// The server closes it.
	npcClick(t, p, 0)
	p.handleNPCDialogue(&packet.NPCDialogue{EntityUniqueID: uint64(1<<64 - 7), ActionType: packet.NPCDialogueActionClose})
	if out := take(); len(out) != 1 || out[0].id != v777.ClientboundPlayClearDialog || p.npc.open {
		t.Fatalf("server close: %d packets", len(out))
	}

	// Escape.
	p.handleNPCDialogue(&packet.NPCDialogue{EntityUniqueID: uint64(1<<64 - 7), ActionType: packet.NPCDialogueActionOpen})
	take()
	npcClick(t, p, -2)
	if out := take(); len(out) != 1 || out[0].id != v777.ClientboundPlayClearDialog || p.npc.open {
		t.Fatalf("escape: %d packets", len(out))
	}

	// A URL button: the link in chat, the dialog closed.
	p.handleNPCDialogue(&packet.NPCDialogue{EntityUniqueID: uint64(1<<64 - 7), ActionType: packet.NPCDialogueActionOpen})
	take()
	npcClick(t, p, 2)
	out = take()
	if len(out) != 2 || out[1].id != v777.ClientboundPlayClearDialog || !strings.Contains(string(out[0].w.B), "example.com") {
		t.Fatalf("url: %d packets", len(out))
	}
}
