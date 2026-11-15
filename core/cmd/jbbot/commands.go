package main

import (
	"fmt"
	"log"
	"strings"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
)

// Commands: the bot decodes the commands packet (the Brigadier graph) as a Java client does and
// can print a command's paths (step "tree NAME"), and asks for suggestions like the chat box's tab
// completion (step "complete TEXT").

// loginCookie is the cookie value for key (nil: none; the bot stores no cookies).
func loginCookie(key string) []byte { return nil }

type jnode struct {
	flags    byte
	children []int32
	redirect int32
	name     string
	parser   int32
	suggest  string
}

// parser names (26.3 minecraft:command_argument_type) for the log.
var parserNames = map[int32]string{0: "bool", 1: "float", 2: "double", 3: "integer", 4: "long", 5: "string", 6: "entity",
	7: "game_profile", 8: "block_pos", 9: "column_pos", 10: "vec3", 11: "vec2", 12: "block_state", 14: "item_stack",
	20: "message", 26: "operation", 28: "angle", 29: "rotation", 31: "score_holder", 36: "resource_location", 39: "int_range"}

// decodeCommands reads a commands packet as the client does; it fails on a malformed one.
func decodeCommands(body []byte) ([]jnode, int32, error) {
	r := wire.NewReader(body)
	n := r.VarInt()
	if n < 0 || n > 1<<20 {
		return nil, 0, fmt.Errorf("bad node count %d", n)
	}
	nodes := make([]jnode, n)
	for i := range nodes {
		nd := &nodes[i]
		nd.flags = r.Byte()
		nd.redirect = -1
		for c := r.VarInt(); c > 0 && r.Err == nil; c-- {
			nd.children = append(nd.children, r.VarInt())
		}
		if nd.flags&0x08 != 0 {
			nd.redirect = r.VarInt()
		}
		kind := nd.flags & 0x03
		if kind == 0 {
			continue
		}
		nd.name = r.String(32767)
		if kind != 2 {
			continue
		}
		nd.parser = r.VarInt()
		switch nd.parser {
		case 1, 2, 3, 4: // number ranges
			f := r.Byte()
			for _, bit := range []byte{1, 2} {
				if f&bit == 0 {
					continue
				}
				if nd.parser == 2 || nd.parser == 4 {
					r.Int64()
				} else {
					r.Int32()
				}
			}
		case 5:
			r.VarInt()
		case 6, 31:
			r.Byte()
		case 43:
			r.Int32()
		case 44, 45, 46, 47:
			r.String(32767)
		}
		if nd.flags&0x10 != 0 {
			nd.suggest = r.String(32767)
		}
	}
	root := r.VarInt()
	if r.Err != nil {
		return nil, 0, r.Err
	}
	if r.Len() != 0 {
		return nil, 0, fmt.Errorf("%d bytes left over", r.Len())
	}
	for i, nd := range nodes {
		for _, c := range nd.children {
			if c < 0 || int(c) >= len(nodes) {
				return nil, 0, fmt.Errorf("node %d: child %d out of range", i, c)
			}
		}
		if nd.redirect >= int32(len(nodes)) {
			return nil, 0, fmt.Errorf("node %d: redirect %d out of range", i, nd.redirect)
		}
	}
	return nodes, root, nil
}

func (b *bot) onCommands(body []byte) {
	nodes, root, err := decodeCommands(body)
	if err != nil {
		log.Printf("COMMANDS malformed: %v", err)
		return
	}
	b.cmdNodes, b.cmdRoot = nodes, root
	log.Printf("COMMANDS %d nodes, %d commands, %d bytes", len(nodes), len(nodes[root].children), len(body))
}

// printTree logs the paths under the root literal name (at most 40): "NAME [FILTER]" shows only
// the paths containing FILTER.
func (b *bot) printTree(spec string) {
	name, filter, _ := strings.Cut(spec, " ")
	if b.cmdNodes == nil {
		log.Printf("TREE %s: no commands packet", name)
		return
	}
	var start int32 = -1
	for _, c := range b.cmdNodes[b.cmdRoot].children {
		if b.cmdNodes[c].name == name {
			start = c
		}
	}
	if start < 0 {
		log.Printf("TREE %s: not in the tree", name)
		return
	}
	var paths []string
	var walk func(i int32, path []string, depth int)
	walk = func(i int32, path []string, depth int) {
		nd := b.cmdNodes[i]
		label := nd.name
		if nd.flags&3 == 2 {
			label = "<" + nd.name + ":" + parserNames[nd.parser]
			if nd.suggest != "" {
				label += "*"
			}
			label += ">"
		}
		if nd.flags&0x04 != 0 {
			label += "!"
		}
		path = append(path, label)
		if nd.redirect >= 0 {
			to := "root"
			if nd.redirect != b.cmdRoot {
				to = b.cmdNodes[nd.redirect].name
			}
			paths = append(paths, strings.Join(path, " ")+" -> "+to)
			return
		}
		if len(nd.children) == 0 || depth > 12 {
			paths = append(paths, strings.Join(path, " "))
			return
		}
		if nd.flags&0x04 != 0 && depth > 0 {
			paths = append(paths, strings.Join(path, " "))
		}
		for _, c := range nd.children {
			walk(c, path, depth+1)
		}
	}
	walk(start, nil, 0)
	if filter != "" {
		all := paths
		paths = nil
		for _, p := range all {
			if strings.Contains(p, filter) {
				paths = append(paths, p)
			}
		}
	}
	log.Printf("TREE %s: %d paths", name, len(paths))
	for i, p := range paths {
		if i >= 40 {
			log.Printf("TREE   ... %d more", len(paths)-i)
			break
		}
		log.Printf("TREE   %s", p)
	}
}

// complete asks for the suggestions at the end of text (as typed after the slash).
func (b *bot) complete(text string) {
	b.suggestID++
	w := &wire.Writer{}
	w.VarInt(b.suggestID)
	w.String("/" + text)
	b.write(v777.ServerboundPlayCommandSuggestion, w)
}

func (b *bot) onSuggestions(r *wire.Reader) {
	id, start, length := r.VarInt(), r.VarInt(), r.VarInt()
	var vals []string
	for n := r.VarInt(); n > 0 && r.Err == nil; n-- {
		vals = append(vals, r.String(32767))
		if r.Bool() {
			break // tooltips: the bridge sends none
		}
	}
	shown := vals
	if len(shown) > 12 {
		shown = shown[:12]
	}
	log.Printf("SUGGEST id %d start %d length %d: %d %v", id, start, length, len(vals), shown)
}
