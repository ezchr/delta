package bridge

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Disconnect reasons and the command tree.

func TestMain(m *testing.M) {
	cfg.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg.LangFile = filepath.Join(os.TempDir(), "delta-no-such-lang-file") // the built-in copy
	os.Exit(m.Run())
}

// TestKickText: Bedrock disconnect messages as lang keys (with or without %, whole or inside a
// message), keys no lang file has, and reason codes without a message.
func TestKickText(t *testing.T) {
	for _, c := range []struct {
		code int32
		msg  string
		want string
	}{
		{-1, "%disconnect.disconnected", "Disconnected by Server"},
		{-1, "disconnect.disconnected", "Disconnected by Server"},
		{-1, "disconnectionScreen.serverFull", "Wow this server is popular! Check back later to see if space opens up."},
		{-1, "%disconnectionScreen.notAllowed", "You're not invited to play on this server."},
		{-1, "%disconnect.kicked.reason go away", "You were kicked from the game: go away"},
		{-1, "%disconnect.someNewReason", "some new reason"},
		{-1, "Server closed", "Server closed"},
		{-1, `{"rawtext":[{"translate":"disconnect.timeout"}]}`, "Timed out"},
		{packet.DisconnectReasonKicked, "", "kicked"},
		{packet.DisconnectReasonServerFull, "", "the server is full"},
		{packet.DisconnectReasonShutdown, "%disconnect.disconnected", "Disconnected by Server"},
		{-1, "", "no reason given"},
	} {
		if got := kickText(c.code, c.msg); got != c.want {
			t.Errorf("kickText(%d, %q) = %q, want %q", c.code, c.msg, got, c.want)
		}
	}
	err := fmt.Errorf("dial: %w", minecraft.DisconnectError("%disconnectionScreen.serverFull"))
	if got := dialErrorText(err); !strings.HasPrefix(got, "the server refused: Wow") {
		t.Errorf("dial error: %q", got)
	}
	if got := dialErrorText(errors.New("connect: disconnectionScreen.notAllowed")); got != "connect: You're not invited to play on this server." {
		t.Errorf("bare key in a dial error: %q", got)
	}
}

// testCommands is a small AvailableCommands as BDS sends it (numbering of 1.26.60).
func testCommands() *packet.AvailableCommands {
	pk := &packet.AvailableCommands{
		EnumValues: []string{"survival", "creative", "s", "c", "tp", "teleport", "as", "run", "true", "false"},
		Enums: []protocol.CommandEnum{
			{Type: "GameMode", ValueIndices: []uint32{0, 1, 2, 3}},
			{Type: "teleportCommandAliases", ValueIndices: []uint32{4, 5}},
			{Type: "Option_As", ValueIndices: []uint32{6}},
			{Type: "Option_Run", ValueIndices: []uint32{7}},
			{Type: "Boolean", ValueIndices: []uint32{8, 9}},
		},
		DynamicEnums: []protocol.DynamicEnum{{Type: "ScoreboardObjectives"}},
	}
	// A big Item enum: minecraft:item_n and item_n.
	items := protocol.CommandEnum{Type: "Item"}
	for i := 0; i < 60; i++ {
		for _, v := range []string{fmt.Sprintf("minecraft:item_%d", i), fmt.Sprintf("item_%d", i)} {
			pk.EnumValues = append(pk.EnumValues, v)
			items.ValueIndices = append(items.ValueIndices, uint32(len(pk.EnumValues)-1))
		}
	}
	items.ValueIndices = append(items.ValueIndices, 0) // "survival" as an item too, why not
	pk.Enums = append(pk.Enums, items)
	enum := func(i uint32) uint32 { return protocol.CommandArgValid | protocol.CommandArgEnum | i }
	basic := func(t uint32) uint32 { return protocol.CommandArgValid | t }
	none := uint32(0xffffffff)
	pk.Commands = []protocol.Command{
		{Name: "gamemode", AliasesOffset: none, Overloads: []protocol.CommandOverload{
			{Parameters: []protocol.CommandParameter{{Name: "gameMode", Type: enum(0)}, {Name: "player", Type: basic(protocol.CommandArgTypeTarget), Optional: true}}},
			{Parameters: []protocol.CommandParameter{{Name: "gameMode", Type: basic(protocol.CommandArgTypeInt)}, {Name: "player", Type: basic(protocol.CommandArgTypeTarget), Optional: true}}},
		}},
		{Name: "teleport", AliasesOffset: 1, Overloads: []protocol.CommandOverload{
			{Parameters: []protocol.CommandParameter{{Name: "destination", Type: basic(protocol.CommandArgTypePosition)}, {Name: "checkForBlocks", Type: enum(4), Optional: true}}},
			{Parameters: []protocol.CommandParameter{{Name: "destination", Type: basic(protocol.CommandArgTypeTarget)}}},
			{Parameters: []protocol.CommandParameter{{Name: "victim", Type: basic(protocol.CommandArgTypeTarget)}, {Name: "destination", Type: basic(protocol.CommandArgTypePosition)}}},
		}},
		{Name: "give", AliasesOffset: none, Overloads: []protocol.CommandOverload{
			{Parameters: []protocol.CommandParameter{{Name: "player", Type: basic(protocol.CommandArgTypeTarget)}, {Name: "itemName", Type: enum(5)},
				{Name: "amount", Type: basic(protocol.CommandArgTypeInt), Optional: true}, {Name: "components", Type: basic(protocol.CommandArgTypeJSON), Optional: true}}},
		}},
		{Name: "say", AliasesOffset: none, Overloads: []protocol.CommandOverload{
			{Parameters: []protocol.CommandParameter{{Name: "message", Type: basic(bedrockMessageRoot)}}},
		}},
		{Name: "scoreboard", AliasesOffset: none, Overloads: []protocol.CommandOverload{
			{Parameters: []protocol.CommandParameter{{Name: "objective", Type: protocol.CommandArgValid | protocol.CommandArgSoftEnum | 0}}},
		}},
		{Name: "execute", AliasesOffset: none, Overloads: []protocol.CommandOverload{
			{Chaining: true, Parameters: []protocol.CommandParameter{{Name: "subcommand", Type: enum(2)}, {Name: "origin", Type: basic(protocol.CommandArgTypeTarget)},
				{Name: "chainedCommand", Type: protocol.CommandArgValid | protocol.CommandArgTypeChainedCommand}}},
			{Parameters: []protocol.CommandParameter{{Name: "subcommand", Type: enum(3)}, {Name: "command", Type: basic(bedrockCommandNew)}}},
		}},
		{Name: "join", AliasesOffset: none}, // the bridge's /join wins
	}
	return pk
}

// decodedNode is a node as a Java client reads it.
type decodedNode struct {
	flags    byte
	children []int32
	redirect int32
	name     string
	parser   int32
	suggest  string
}

func decodeTree(t *testing.T, b []byte) []decodedNode {
	t.Helper()
	r := wire.NewReader(b)
	nodes := make([]decodedNode, r.VarInt())
	for i := range nodes {
		n := &nodes[i]
		n.flags, n.redirect = r.Byte(), -1
		for c := r.VarInt(); c > 0; c-- {
			n.children = append(n.children, r.VarInt())
		}
		if n.flags&flagRedirect != 0 {
			n.redirect = r.VarInt()
		}
		if n.flags&3 == nodeRoot {
			continue
		}
		n.name = r.String(32767)
		if n.flags&3 != nodeArgument {
			continue
		}
		n.parser = r.VarInt()
		switch n.parser {
		case parserFloat, parserInteger:
			if r.Byte() != 0 {
				t.Fatal("ranges are not sent")
			}
		case parserString:
			r.VarInt()
		case parserEntity, parserScoreHolder:
			r.Byte()
		}
		if n.flags&flagSuggestions != 0 {
			n.suggest = r.String(32767)
		}
	}
	if root := r.VarInt(); root != 0 || r.Err != nil || r.Len() != 0 {
		t.Fatalf("root %d, err %v, %d bytes left", root, r.Err, r.Len())
	}
	return nodes
}

// paths are the node paths under the root literal name ("<arg:parser>", "!" executable,
// "-> x" a redirect).
func paths(nodes []decodedNode, name string) []string {
	var out []string
	var walk func(i int32, path string, depth int)
	walk = func(i int32, path string, depth int) {
		n := nodes[i]
		label := n.name
		if n.flags&3 == nodeArgument {
			label = fmt.Sprintf("<%s:%d>", n.name, n.parser)
		}
		if n.flags&flagExecutable != 0 {
			label += "!"
		}
		path = strings.TrimSpace(path + " " + label)
		if n.redirect >= 0 {
			out = append(out, path+" -> "+nodes[n.redirect].name)
			return
		}
		if len(n.children) == 0 || depth > 6 {
			out = append(out, path)
			return
		}
		for _, c := range n.children {
			walk(c, path, depth+1)
		}
	}
	for _, c := range nodes[0].children {
		if nodes[c].name == name {
			walk(c, "", 0)
		}
	}
	slices.Sort(out)
	return out
}

func TestCommandTree(t *testing.T) {
	tr := newCmdTree()
	localTree(tr)
	tr.addBedrock(testCommands())
	tr.resolve()
	var w wire.Writer
	tr.encode(&w, func(id int32) int32 { return id })
	nodes := decodeTree(t, w.B)

	want := map[string][]string{
		"gamemode": {"gamemode <gameMode:3>! <player:6>!", "gamemode c! <player:6>!", "gamemode creative! <player:6>!",
			"gamemode s! <player:6>!", "gamemode survival! <player:6>!"},
		"teleport": {"teleport <destination:10>! false!", "teleport <destination:10>! true!", "teleport <destination_2:6>!",
			"teleport <victim:6> <destination:10>!"},
		"tp":      {"tp -> teleport"},
		"give":    {"give <player:6> <itemName:31>! <amount:3>! <components:5>!"},
		"say":     {"say <message:20>!"},
		"execute": {"execute as <origin:6> -> execute", "execute run -> "},
	}
	for name, w := range want {
		if got := paths(nodes, name); !slices.Equal(got, w) {
			t.Errorf("%s:\n got  %q\n want %q", name, got, w)
		}
	}
	if !nodes[nodes[0].children[0]].exec() {
		t.Error("/jb is not executable")
	}
}

func (n decodedNode) exec() bool { return n.flags&flagExecutable != 0 }

func TestSuggestions(t *testing.T) {
	tr := newCmdTree()
	localTree(tr)
	tr.addBedrock(testCommands())
	tr.resolve()
	p := &Player{}
	soft := map[string][]string{"ScoreboardObjectives": {"kills", "deaths"}}
	for _, c := range []struct {
		line  string
		start int
		want  []string
	}{
		{"give @a item_1", 8, []string{"item_1", "item_10", "item_11", "item_12", "item_13", "item_14", "item_15", "item_16", "item_17", "item_18", "item_19"}},
		{"give @a minecraft:item_5", 8, []string{"minecraft:item_5", "minecraft:item_50", "minecraft:item_51", "minecraft:item_52", "minecraft:item_53",
			"minecraft:item_54", "minecraft:item_55", "minecraft:item_56", "minecraft:item_57", "minecraft:item_58", "minecraft:item_59"}},
		{"scoreboard k", 11, []string{"kills"}},
		{"execute as @a run give @s item_59", 26, []string{"item_59"}},
		{"gamemode cr", 9, nil}, // literals: the client completes those itself
	} {
		start, got := tr.suggestions(p, c.line, soft)
		if c.want != nil && start != c.start || !slices.Equal(got, c.want) {
			t.Errorf("%q: start %d %q, want %d %q", c.line, start, got, c.start, c.want)
		}
	}
	_, all := tr.suggestions(p, "give @a ", soft)
	if len(all) != 61 || slices.Contains(all, "minecraft:item_3") {
		t.Errorf("give @a <nothing>: %d suggestions (want 61, the namespaced twins left out)", len(all))
	}
}

func TestTokens(t *testing.T) {
	got := tokens(`say "a b" c `)
	want := []cmdToken{{"say", 0}, {`"a b"`, 4}, {"c", 10}, {"", 12}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}
