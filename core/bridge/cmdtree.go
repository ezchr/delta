package bridge

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The Java command tree (ClientboundPlayCommands) built from the Bedrock server's
// AvailableCommands, so the Java client completes and highlights the server's commands as it does
// vanilla Java's. Every command is a literal under the root and every overload a path of nodes
// from it:
//
//	int              -> brigadier:integer     float           -> brigadier:float
//	value (~ float)  -> minecraft:angle       wildcard int    -> token
//	target           -> minecraft:entity      wildcard target -> minecraft:score_holder
//	block position   -> minecraft:block_pos   position        -> minecraft:vec3
//	string           -> token, or a quoted brigadier:string phrase ("<name>" beside <name>)
//	message          -> minecraft:message     operator        -> minecraft:operation
//	compare operator -> literals < <= = >= >  integer range   -> token (Bedrock has !1..3)
//	raw text, JSON, block states -> brigadier:string greedy  file path, suffixed (10L) -> token
//	clock time marker -> token with the vanilla markers as suggestions
//	enum (<= maxEnumLiterals values)  -> one literal per value
//	enum (bigger)                     -> an argument whose suggestions the bridge gives
//	                                     (minecraft:ask_server), from the enum's values
//	soft enum                         -> token, suggestions from the bridge (values as
//	                                     UpdateSoftEnum changes them)
//	command (execute run)             -> redirect to the root
//	chained subcommand (execute)      -> redirect to the command's literal
//
// "token" is minecraft:score_holder, which reads any text up to a space (except text starting
// with @, which is a selector): the Java string parsers stop at ':' or '<' or '*' and mark the
// rest red, which made the client show usage text instead of suggestions after the first such
// argument (minecraft:basic_flame_particle, >=, *, !1..3).
//
// Aliases are literals redirecting to their command. The bridge's own commands (/bb, /join, /hub,
// /ping) are added first and win over server commands of the same name. Java sends a command
// typed with the tree as text either way (it is not checked against the tree), so a Bedrock-only
// syntax the Java parsers mark red still runs (Bedrock-only selector arguments such as r, c, m,
// family or hasitem, which minecraft:entity refuses: no Java parser accepts them).
//
// A parent has at most one child asking the server (encode): the client's
// ClientSuggestionProvider.customSuggestion cancels the pending request when the next child asks,
// and a cancelled future leaves the whole completion unfinished (no suggestions, no usage). The
// bridge's answer covers every child of the parent anyway (suggestions walks the whole tree).

// maxEnumLiterals is the most values an enum may have to become literals.
const maxEnumLiterals = 48

// maxSuggestions is the most suggestions sent for one request. BDS's Block enum has 2648 values
// (with and without minecraft:), Item 3410: a cap of 1000 cut the sorted list before "stone".
const maxSuggestions = 8000

// clockTimeMarkers are the overworld clock's time markers (/time set <timeMarkerName>), which
// BDS 1.26.60 sends as a plain parameter.
var clockTimeMarkers = []string{"day", "noon", "sunset", "night", "midnight", "sunrise"}

// compareOperators are /execute if score's operators (Java's minecraft:operation has no <= >=).
var compareOperators = []string{"<", "<=", "=", ">=", ">"}

// Brigadier argument parser ids (minecraft:command_argument_type, 26.3).
const (
	parserFloat       = 1
	parserInteger     = 3
	parserString      = 5
	parserEntity      = 6
	parserBlockPos    = 8
	parserVec3        = 10
	parserMessage     = 20
	parserOperation   = 26
	parserAngle       = 28
	parserScoreHolder = 31

	stringWord   = 0
	stringPhrase = 1
	stringGreedy = 2
)

// Brigadier node flags.
const (
	nodeRoot     = 0
	nodeLiteral  = 1
	nodeArgument = 2

	flagExecutable  = 0x04
	flagRedirect    = 0x08
	flagSuggestions = 0x10
)

// Bedrock parameter types (AvailableCommands, BDS 1.26.60; gophertunnel's constants where they
// agree).
const (
	bedrockMessageRoot = 68 // BDS's message (gophertunnel's CommandArgTypeMessage is 67)
	bedrockCommandNew  = 90 // BDS 1.26.60's /execute run <command>
)

// suggestSource is where an argument's suggestions come from (minecraft:ask_server).
type suggestSource struct {
	id    string                   // identity, for merging nodes
	enum  []string                 // a large Bedrock enum's values
	soft  string                   // a Bedrock soft enum (values from Commands.soft)
	local func(p *Player) []string // a bridge command's (p.mu held)
}

// cmdNode is one Brigadier node.
type cmdNode struct {
	kind     byte
	exec     bool
	name     string
	parser   int32
	props    []byte // encoded parser properties
	words    int    // words the argument takes (-1: the rest of the line)
	sugg     *suggestSource
	children []int32
	redirect int32 // -1: none
}

// key identifies a node among its siblings: two overloads with the same parameter share it.
func (n *cmdNode) key() string {
	s := ""
	if n.sugg != nil {
		s = n.sugg.id
	}
	return fmt.Sprintf("%d|%s|%d|%x|%s", n.kind, n.name, n.parser, n.props, s)
}

// cmdTree is a Brigadier graph; node 0 is the root.
type cmdTree struct {
	nodes []cmdNode
	// later are redirects resolved once every command is in: [node, target].
	later [][2]int32
}

func newCmdTree() *cmdTree {
	return &cmdTree{nodes: []cmdNode{{kind: nodeRoot, redirect: -1}}}
}

func (t *cmdTree) add(n cmdNode) int32 {
	t.nodes = append(t.nodes, n)
	return int32(len(t.nodes) - 1)
}

// child returns parent's child equal to n, creating it if there is none. Brigadier keys children
// by name, so a different node of the same name is renamed (name_2...).
func (t *cmdTree) child(parent int32, n cmdNode) int32 {
	k := n.key()
	for _, c := range t.nodes[parent].children {
		if t.nodes[c].key() == k {
			return c
		}
	}
	if n.kind == nodeArgument {
		base := n.name
		for i := 2; t.hasChildNamed(parent, n.name); i++ {
			n.name = fmt.Sprintf("%s_%d", base, i)
			k = n.key()
			for _, c := range t.nodes[parent].children {
				if t.nodes[c].key() == k {
					return c
				}
			}
		}
	} else if t.hasChildNamed(parent, n.name) {
		return t.childNamed(parent, n.name)
	}
	i := t.add(n)
	t.nodes[parent].children = append(t.nodes[parent].children, i)
	return i
}

func (t *cmdTree) hasChildNamed(parent int32, name string) bool {
	return t.childNamed(parent, name) >= 0
}

func (t *cmdTree) childNamed(parent int32, name string) int32 {
	for _, c := range t.nodes[parent].children {
		if t.nodes[c].name == name {
			return c
		}
	}
	return -1
}

// link makes node i a child of parent too, unless parent has a child of that name.
func (t *cmdTree) link(parent, i int32) int32 {
	if c := t.childNamed(parent, t.nodes[i].name); c >= 0 {
		return c
	}
	t.nodes[parent].children = append(t.nodes[parent].children, i)
	return i
}

// lit adds (or finds) a literal under parent.
func (t *cmdTree) lit(parent int32, name string, exec bool) int32 {
	i := t.child(parent, cmdNode{kind: nodeLiteral, name: name, redirect: -1})
	if exec {
		t.nodes[i].exec = true
	}
	return i
}

// greedy adds a greedy string argument under parent, its suggestions from f (nil: none).
func (t *cmdTree) greedy(parent int32, name string, exec bool, f func(p *Player) []string) int32 {
	n := cmdNode{kind: nodeArgument, name: name, parser: parserString, props: varIntProp(stringGreedy), words: -1, redirect: -1, exec: exec}
	if f != nil {
		n.sugg = &suggestSource{id: "local:" + name, local: f}
	}
	return t.child(parent, n)
}

// redirectLater redirects node to target once the tree is complete.
func (t *cmdTree) redirectLater(node, target int32) {
	t.later = append(t.later, [2]int32{node, target})
}

// resolve sets the redirects asked for. A node that also goes on by itself (another overload
// continues after it, e.g. /execute if block <pos> <block> [states] [chained]) or that is asked to
// redirect to several targets cannot redirect to one of them, since a Brigadier redirect replaces
// a node's children. It redirects instead to a new node that nothing has as a child, holding its
// own children and the targets'. Linking the target's children under the node itself would make
// a cycle (execute -> if -> block -> ... -> if), which the Java client refuses as "an impossible
// command tree" and disconnects.
func (t *cmdTree) resolve() {
	var order []int32
	targets := map[int32][]int32{}
	for _, l := range t.later {
		if _, ok := targets[l[0]]; !ok {
			order = append(order, l[0])
		}
		if !slices.Contains(targets[l[0]], l[1]) {
			targets[l[0]] = append(targets[l[0]], l[1])
		}
	}
	t.later = nil
	for _, n := range order {
		ts := targets[n]
		if len(t.nodes[n].children) == 0 && t.nodes[n].redirect < 0 && len(ts) == 1 && ts[0] != n {
			t.nodes[n].redirect = ts[0]
			continue
		}
		m := t.add(cmdNode{kind: nodeLiteral, name: t.nodes[n].name, redirect: -1, exec: t.nodes[n].exec})
		own := t.nodes[n].children
		if r := t.nodes[n].redirect; r >= 0 {
			ts = append([]int32{r}, ts...)
		}
		t.nodes[n].children, t.nodes[n].redirect = nil, m
		for _, c := range own {
			t.link(m, c)
		}
		for _, tg := range ts {
			for _, c := range slices.Clone(t.effectiveChildren(tg)) {
				t.link(m, c)
			}
		}
	}
}

// effectiveChildren are the nodes that follow node i: its redirect's when it redirects.
func (t *cmdTree) effectiveChildren(i int32) []int32 {
	for range 8 {
		n := &t.nodes[i]
		if n.redirect < 0 || len(n.children) > 0 {
			return n.children
		}
		i = n.redirect
	}
	return t.nodes[i].children
}

// rootNames are the root's literal names.
func (t *cmdTree) rootNames() map[string]bool {
	m := map[string]bool{}
	for _, c := range t.nodes[0].children {
		m[t.nodes[c].name] = true
	}
	return m
}

// addBedrock adds the server's commands, except names the tree has already (the bridge's).
func (t *cmdTree) addBedrock(pk *packet.AvailableCommands) {
	taken := t.rootNames()
	cmds := slices.Clone(pk.Commands)
	sort.SliceStable(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	for _, c := range cmds {
		name := strings.ToLower(c.Name)
		if !validLiteral(name) || taken[name] {
			continue
		}
		taken[name] = true
		root := t.lit(0, name, false)
		for _, o := range c.Overloads {
			t.addOverload(root, o.Parameters, pk)
		}
		if int(c.AliasesOffset) < len(pk.Enums) {
			for _, a := range enumValues(pk, pk.Enums[c.AliasesOffset]) {
				a = strings.ToLower(a)
				if a == name || !validLiteral(a) || taken[a] {
					continue
				}
				taken[a] = true
				i := t.add(cmdNode{kind: nodeLiteral, name: a, redirect: root})
				t.nodes[0].children = append(t.nodes[0].children, i)
			}
		}
	}
	// Aliases are executable when their command is.
	for _, c := range t.nodes[0].children {
		if r := t.nodes[c].redirect; r > 0 {
			t.nodes[c].exec = t.nodes[r].exec
		}
	}
}

// addOverload adds the path of one overload's parameters under the command's literal.
func (t *cmdTree) addOverload(root int32, params []protocol.CommandParameter, pk *packet.AvailableCommands) {
	frontier := []int32{root}
	if allOptional(params) {
		t.nodes[root].exec = true
	}
	for i, prm := range params {
		base := prm.Type & 0xffff
		plain := prm.Type&(protocol.CommandArgEnum|protocol.CommandArgSoftEnum|protocol.CommandArgSuffixed) == 0
		switch {
		case prm.Type&protocol.CommandArgTypeChainedCommand != 0:
			// /execute's subcommands go on with another subcommand.
			for _, f := range frontier {
				t.redirectLater(f, root)
			}
			return
		case plain && (base == protocol.CommandArgTypeCommand || base == protocol.CommandArgTypeSlashCommand || base == bedrockCommandNew):
			for _, f := range frontier {
				t.redirectLater(f, 0) // run <command>: any command
			}
			return
		}
		last := i == len(params)-1
		lits, args := bedrockParam(prm, last, pk)
		var next []int32
		if lits != nil {
			for _, f := range frontier {
				for _, l := range lits {
					next = append(next, t.lit(f, l, false))
				}
			}
		} else {
			// Alternatives (a string and its quoted form) are siblings with the same children:
			// Brigadier tries both and keeps the one that parses further.
			for _, arg := range args {
				shared := int32(-1)
				for _, f := range frontier {
					if shared < 0 {
						shared = t.child(f, arg)
						next = append(next, shared)
					} else {
						next = append(next, t.link(f, shared))
					}
				}
			}
		}
		slices.Sort(next)
		frontier = slices.Compact(next)
		if allOptional(params[i+1:]) {
			for _, f := range frontier {
				t.nodes[f].exec = true
			}
		}
	}
}

func allOptional(params []protocol.CommandParameter) bool {
	for _, p := range params {
		if !p.Optional {
			return false
		}
	}
	return true
}

// enumValues are the values of a Bedrock enum.
func enumValues(pk *packet.AvailableCommands, e protocol.CommandEnum) []string {
	out := make([]string, 0, len(e.ValueIndices))
	for _, i := range e.ValueIndices {
		if int(i) < len(pk.EnumValues) {
			out = append(out, pk.EnumValues[i])
		}
	}
	return out
}

// bedrockParam is the Java form of a Bedrock parameter: literals (an enum's values), or argument
// nodes (alternatives: siblings with the same children).
func bedrockParam(prm protocol.CommandParameter, last bool, pk *packet.AvailableCommands) ([]string, []cmdNode) {
	n := cmdNode{kind: nodeArgument, name: argName(prm.Name), words: 1, redirect: -1}
	one := func() ([]string, []cmdNode) { return nil, []cmdNode{n} }
	token := func() {
		n.parser, n.props = parserScoreHolder, []byte{1}
	}
	greedy := func() {
		n.parser, n.props, n.words = parserString, varIntProp(stringGreedy), -1
	}
	base := prm.Type & 0xffff
	switch {
	case prm.Type&protocol.CommandArgEnum != 0:
		if int(base) >= len(pk.Enums) {
			break
		}
		e := pk.Enums[base]
		vals := enumValues(pk, e)
		if len(vals) > 0 && len(vals) <= maxEnumLiterals && !slices.ContainsFunc(vals, func(v string) bool { return !validLiteral(v) }) {
			return vals, nil
		}
		token() // ids with and without minecraft:, some with capitals
		n.sugg = &suggestSource{id: "enum:" + e.Type, enum: vals}
		return one()
	case prm.Type&protocol.CommandArgSoftEnum != 0:
		name := ""
		if int(base) < len(pk.DynamicEnums) {
			name = pk.DynamicEnums[base].Type
		}
		// Values come and go (objectives, tags): any word, whatever the values are now.
		token()
		n.sugg = &suggestSource{id: "soft:" + name, soft: name}
		return one()
	case prm.Type&protocol.CommandArgSuffixed != 0:
		token()
		return one()
	}
	switch base {
	case protocol.CommandArgTypeInt:
		n.parser, n.props = parserInteger, []byte{0}
	case protocol.CommandArgTypeFloat:
		n.parser, n.props = parserFloat, []byte{0}
	case protocol.CommandArgTypeRValue:
		n.parser = parserAngle // a float that may be relative (~)
	case protocol.CommandArgTypeOperator:
		n.parser = parserOperation // = += -= *= /= %= < > ><, as Bedrock's
	case protocol.CommandArgTypeCompareOperator:
		return compareOperators, nil
	case protocol.CommandArgTypeTarget:
		n.parser, n.props = parserEntity, []byte{0} // several, not only players; "quoted names"
	case protocol.CommandArgTypeWildcardTarget:
		n.parser, n.props = parserScoreHolder, []byte{1} // several, and *
	case protocol.CommandArgTypeWildcardInt, protocol.CommandArgTypeIntegerRange, protocol.CommandArgTypeFilepath:
		token() // * / !1..3 / paths with capitals
	case protocol.CommandArgTypeClockTimeMarker:
		token()
		n.sugg = &suggestSource{id: "time_markers", enum: clockTimeMarkers}
	case protocol.CommandArgTypeString:
		// A word (namespaced ids, dotted sound names) or a "quoted string" with spaces.
		token()
		q := n
		q.name = `"` + n.name + `"`
		q.parser, q.props = parserString, varIntProp(stringPhrase)
		return nil, []cmdNode{n, q}
	case protocol.CommandArgTypeBlockPosition:
		n.parser, n.words = parserBlockPos, 3
	case protocol.CommandArgTypePosition:
		n.parser, n.words = parserVec3, 3
	case protocol.CommandArgTypeMessage, bedrockMessageRoot:
		n.parser, n.words = parserMessage, -1
	case protocol.CommandArgTypeBlockStates, protocol.CommandArgTypeJSON, protocol.CommandArgTypeRawText:
		// ["color"="red", "x"=1], {"rawtext":[...]}: brackets and spaces no Java parser reads
		// but the greedy string (the parameters after it get no suggestions).
		greedy()
	default:
		token()
		if last {
			greedy()
		}
	}
	return one()
}

func varIntProp(v int32) []byte {
	var w wire.Writer
	w.VarInt(v)
	return w.B
}

// argName is a parameter name as a Java argument name.
func argName(s string) string {
	s = strings.Join(strings.Fields(s), "_")
	if s == "" {
		return "value"
	}
	return s
}

// validLiteral reports whether s can be a Brigadier literal.
func validLiteral(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == '"' || r == '\'' || r > 0x7e {
			return false
		}
	}
	return true
}

// encode writes the commands packet body for a client of version v (parser ids remapped).
func (t *cmdTree) encode(w *wire.Writer, mapParser func(int32) int32) {
	ask := t.askNodes()
	w.VarInt(int32(len(t.nodes)))
	for i, n := range t.nodes {
		flags := n.kind
		if n.exec {
			flags |= flagExecutable
		}
		if n.redirect >= 0 {
			flags |= flagRedirect
		}
		if ask[i] {
			flags |= flagSuggestions
		}
		w.Byte(flags)
		w.VarInt(int32(len(n.children)))
		for _, c := range n.children {
			w.VarInt(c)
		}
		if n.redirect >= 0 {
			w.VarInt(n.redirect)
		}
		if n.kind == nodeRoot {
			continue
		}
		w.String(n.name)
		if n.kind != nodeArgument {
			continue
		}
		w.VarInt(mapParser(n.parser))
		w.Raw(n.props)
		if ask[i] {
			w.String("minecraft:ask_server")
		}
	}
	w.VarInt(0) // root
}

// askNodes are the argument nodes sent with minecraft:ask_server: those with suggestions, but
// only one among a parent's children (see the top of the file), the first in node order.
func (t *cmdTree) askNodes() []bool {
	ask := make([]bool, len(t.nodes))
	for i, n := range t.nodes {
		ask[i] = n.sugg != nil && n.kind == nodeArgument
	}
	for _, n := range t.nodes {
		seen := false
		for _, c := range n.children {
			if !ask[c] {
				continue
			}
			if seen {
				ask[c] = false
			}
			seen = true
		}
	}
	return ask
}

// cmdToken is a word of a command line being completed.
type cmdToken struct {
	s     string
	start int
}

// tokens splits a command line into words ("quoted strings" are one word). The last token is the
// one being typed (empty after a space).
func tokens(line string) []cmdToken {
	var out []cmdToken
	i := 0
	for {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		start := i
		if i >= len(line) {
			out = append(out, cmdToken{start: start})
			return out
		}
		if line[i] == '"' {
			i++
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' {
					i++
				}
				i++
			}
			if i < len(line) {
				i++
			}
		} else {
			for i < len(line) && line[i] != ' ' {
				i++
			}
		}
		out = append(out, cmdToken{s: line[start:min(i, len(line))], start: start})
		if i >= len(line) {
			return out
		}
	}
}

// suggestions finds the arguments being typed at the end of line (no leading slash) and gives
// their suggestions: where the replaced text starts, and the values.
func (t *cmdTree) suggestions(p *Player, line string, soft map[string][]string) (int, []string) {
	toks := tokens(line)
	last := len(toks) - 1
	type state struct {
		node int32
		tok  int
	}
	states := []state{{0, 0}}
	seen := map[state]bool{}
	start := toks[last].start
	var out []string
	add := func(src *suggestSource, partial string, at int) {
		var vals []string
		switch {
		case src.local != nil:
			vals = src.local(p)
		case src.soft != "":
			vals = soft[src.soft]
		default:
			vals = src.enum
		}
		start = at
		out = append(out, matchValues(vals, partial)...)
	}
	for steps := 0; len(states) > 0 && steps < 4096; steps++ {
		s := states[0]
		states = states[1:]
		if seen[s] {
			continue
		}
		seen[s] = true
		n := &t.nodes[s.node]
		children := n.children
		if n.redirect >= 0 {
			children = t.nodes[n.redirect].children
		}
		for _, ci := range children {
			c := &t.nodes[ci]
			if c.kind == nodeLiteral {
				if s.tok < last && strings.EqualFold(toks[s.tok].s, c.name) {
					states = append(states, state{ci, s.tok + 1})
				}
				continue
			}
			switch {
			case c.words < 0:
				if c.sugg != nil {
					add(c.sugg, line[toks[s.tok].start:], toks[s.tok].start)
				}
			case s.tok+c.words-1 < last:
				states = append(states, state{ci, s.tok + c.words})
			case s.tok == last && c.sugg != nil:
				add(c.sugg, toks[last].s, toks[last].start)
			}
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return start, out
}

// matchValues are the values starting with partial (any case). Without a namespace typed, a
// namespaced value matches by its path, and is left out when its path alone is a value too
// (Bedrock enums list both "minecraft:stone" and "stone").
func matchValues(vals []string, partial string) []string {
	pl := strings.ToLower(strings.Trim(partial, `"`))
	var out []string
	short := map[string]bool{}
	if !strings.Contains(pl, ":") {
		for _, v := range vals {
			if !strings.Contains(v, ":") {
				short[strings.ToLower(v)] = true
			}
		}
	}
	for _, v := range vals {
		lv := strings.ToLower(v)
		if strings.HasPrefix(lv, pl) {
			if _, path, ok := strings.Cut(lv, ":"); ok && pl == "" && short[path] {
				continue
			}
			out = append(out, v)
			continue
		}
		if _, path, ok := strings.Cut(lv, ":"); ok && !strings.Contains(pl, ":") && strings.HasPrefix(path, pl) && !short[path] {
			out = append(out, v)
		}
	}
	return out
}
