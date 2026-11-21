package bridge

import (
	"fmt"
	"slices"
	"strings"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Commands: the bridge's own (/jb, never sent to the server) and everything else passed to the
// server as typed (CommandRequest). The Java command tree is the bridge's commands plus the
// server's AvailableCommands translated to Brigadier nodes (cmdtree.go); suggestions for big
// enums, soft enums and the bridge's arguments are answered here (command_suggestion).

// Commands is the commands state of a player (p.mu).
type Commands struct {
	p *Player
	// server is the server's AvailableCommands (nil: none yet); soft its soft enums' values as
	// UpdateSoftEnum keeps them.
	server *packet.AvailableCommands
	soft   map[string][]string
	// tree is the tree last built; lastTree its encoding last sent.
	tree     *cmdTree
	lastTree []byte
}

func newCommands(p *Player) *Commands { return &Commands{p: p, soft: map[string][]string{}} }

// LocalCommandFunc runs a bridge command. It is called WITHOUT p.mu (commands may dial); take
// p.mu around anything touching translator state.
type LocalCommandFunc func(p *Player, args []string)

var localCommands = map[string]LocalCommandFunc{}

// LocalCommand registers a bridge command (never sent to the server). Only call it from init.
func LocalCommand(name string, f LocalCommandFunc) { localCommands[name] = f }

func init() {
	LocalCommand(localCommandName, (*Player).jbCommand)
	OnJava(v777.ServerboundPlayChatCommand, (*Player).javaCommand)
	OnJava(v777.ServerboundPlayChatCommandSigned, (*Player).javaCommand)
	OnJava(v777.ServerboundPlayCommandSuggestion, func(p *Player, r *wireReader) error {
		id := r.VarInt()
		line := r.String(32500)
		if r.Err != nil {
			return nil
		}
		p.cmds.suggest(id, line)
		return nil
	})
	OnBedrock(packet.IDAvailableCommands, func(p *Player, pk *packet.AvailableCommands) {
		p.cmds.server = pk
		for _, e := range pk.DynamicEnums {
			p.cmds.soft[e.Type] = slices.Clone(e.Values)
		}
		p.cmds.sendTree()
	})
	OnBedrock(packet.IDUpdateSoftEnum, func(p *Player, pk *packet.UpdateSoftEnum) {
		vals := p.cmds.soft[pk.EnumType]
		switch pk.ActionType {
		case packet.SoftEnumActionAdd:
			for _, o := range pk.Options {
				if !slices.Contains(vals, o) {
					vals = append(vals, o)
				}
			}
		case packet.SoftEnumActionRemove:
			vals = slices.DeleteFunc(vals, func(v string) bool { return slices.Contains(pk.Options, v) })
		case packet.SoftEnumActionSet:
			vals = slices.Clone(pk.Options)
		}
		p.cmds.soft[pk.EnumType] = vals
	})
	OnReset(func(p *Player, newConn bool) {
		if newConn {
			p.cmds.server = nil
			p.cmds.soft = map[string][]string{}
		}
	})
}

// javaCommand handles a command the Java client typed.
func (p *Player) javaCommand(r *wireReader) error {
	line := r.String(32767)
	if r.Err != nil {
		return nil
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return nil
	}
	if run, ok := localCommands[strings.ToLower(f[0])]; ok {
		p.goSafe("command "+f[0], func() { run(p, f[1:]) })
		return nil
	}
	if p.rem == nil {
		p.BridgeMessage("Not on the server yet.")
		return nil
	}
	p.runServerCommand(ampColours(line))
	return nil
}

// localCommandName is the bridge's own command (a server command of that name is hidden from
// Java players).
const localCommandName = "jb"

// jbCommand is /jb: status, ping, reconnect.
func (p *Player) jbCommand(args []string) {
	if len(args) == 0 {
		p.jbHelp()
		return
	}
	rest := args[1:]
	switch strings.ToLower(args[0]) {
	case "ping":
		p.pingCommand(rest)
	case "reconnect", "rc":
		p.switchTo(cfg.Target)
	case "where", "status":
		p.status()
	default:
		p.jbHelp()
	}
}

func (p *Player) jbHelp() {
	p.message("§e/%[1]s status§r, §e/%[1]s ping [player]§r, §e/%[1]s reconnect§r", localCommandName)
}

// status is /jb status: where the player is, as whom, and the ping.
func (p *Player) status() {
	p.mu.Lock()
	r := p.rem
	var dim int32
	if r != nil {
		dim = r.gd.Dimension
	}
	ping, ok := p.ping.median()
	p.mu.Unlock()
	if r == nil {
		p.message("Not connected to the server.")
		return
	}
	name := r.conn.IdentityData().DisplayName
	line := fmt.Sprintf("Connected as §e%s§r through %s (%s)", name, ProductName, [...]string{"overworld", "nether", "the end"}[min(max(dim, 0), 2)])
	if ok {
		line += fmt.Sprintf(", ping %d ms", ping.Milliseconds())
	}
	p.message("%s.", line)
}

// suggest answers a command_suggestion: the values for the argument being typed at the end of
// line. p.mu is held.
func (c *Commands) suggest(id int32, line string) {
	off := 0
	if strings.HasPrefix(line, "/") {
		off = 1
	}
	if c.tree == nil {
		c.sendTree()
	}
	start, vals := c.tree.suggestions(c.p, line[off:], c.soft)
	partial := len(line) - off - start
	w := c.p.pk()
	w.VarInt(id)
	w.VarInt(int32(start + off))
	w.VarInt(int32(partial))
	w.VarInt(int32(len(vals)))
	for _, v := range vals {
		w.String(v)
		w.Bool(false) // no tooltip
	}
	c.p.send(v777.ClientboundPlayCommandSuggestions, w)
}

// localTree adds the bridge's commands to t.
func localTree(t *cmdTree) {
	jb := t.lit(0, localCommandName, true)
	for _, s := range []string{"help", "reconnect", "status", "where"} {
		t.lit(jb, s, true)
	}
	t.greedy(t.lit(jb, "ping", true), "player", true, pingSuggestions)
	// Commands other files registered without a tree of their own: any arguments.
	for n := range localCommands {
		if !t.hasChildNamed(0, n) && validLiteral(n) {
			t.greedy(t.lit(0, n, true), "args", true, nil)
		}
	}
}

// sendTree builds the command tree and sends it if it changed. p.mu is held.
func (c *Commands) sendTree() {
	t := newCmdTree()
	localTree(t)
	if c.server != nil {
		t.addBedrock(c.server)
	}
	t.resolve()
	c.tree = t
	v := c.p.ver()
	var w wire.Writer
	t.encode(&w, func(id int32) int32 {
		if v.Native() {
			return id
		}
		return v.Builtin("minecraft:command_argument_type", id)
	})
	if slices.Equal(w.B, c.lastTree) {
		return
	}
	c.lastTree = slices.Clone(w.B)
	out := c.p.pk()
	out.Raw(w.B)
	c.p.send(v777.ClientboundPlayCommands, out)
	if c.server != nil {
		c.p.log.Debug("command tree sent", "nodes", len(t.nodes), "bytes", len(w.B), "server commands", len(c.server.Commands))
	}
}
