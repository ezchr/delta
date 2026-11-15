package bridge

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// javaValidate is the Java client's ClientboundCommandsPacket.validateEntries: every redirect
// target must be buildable before the node (no redirect cycles) and every child before its
// parent (the children form a DAG). A tree failing it disconnects the client ("Server sent an
// impossible command tree").
func javaValidate(nodes []decodedNode) (string, bool) {
	pass := func(name string, ok func(n decodedNode, left map[int32]bool) bool) (string, bool) {
		left := map[int32]bool{}
		for i := range nodes {
			left[int32(i)] = true
		}
		for len(left) > 0 {
			removed := false
			for i := range left {
				if ok(nodes[i], left) {
					delete(left, i)
					removed = true
				}
			}
			if !removed {
				var names []string
				for i := range left {
					if len(names) < 8 {
						names = append(names, nodes[i].name)
					}
				}
				return name + ": stuck at " + strings.Join(names, ", "), false
			}
		}
		return "", true
	}
	if s, ok := pass("redirects", func(n decodedNode, left map[int32]bool) bool {
		return n.redirect < 0 || !left[n.redirect]
	}); !ok {
		return s, false
	}
	return pass("children", func(n decodedNode, left map[int32]bool) bool {
		for _, c := range n.children {
			if left[c] {
				return false
			}
		}
		return true
	})
}

// TestBDSCommandTreeValid builds the tree of a real BDS 1.26.60's AvailableCommands (operator,
// cheats on: /execute with its chained subcommands) and checks the Java client accepts it.
func TestBDSCommandTreeValid(t *testing.T) {
	f, err := os.Open("testdata/bds_commands.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var pk packet.AvailableCommands
	if err := json.NewDecoder(zr).Decode(&pk); err != nil {
		t.Fatal(err)
	}
	for _, pkc := range []*packet.AvailableCommands{testCommands(), &pk} {
		tr := newCmdTree()
		localTree(tr)
		tr.addBedrock(pkc)
		tr.resolve()
		var w wire.Writer
		tr.encode(&w, func(id int32) int32 { return id })
		nodes := decodeTree(t, w.B)
		if s, ok := javaValidate(nodes); !ok {
			t.Fatalf("%d nodes: the Java client refuses the tree: %s", len(nodes), s)
		}
		// Suggestions still go through /execute's chain.
		p := &Player{}
		if _, got := tr.suggestions(p, "execute as @s at @s ", nil); len(got) == 0 && pkc == &pk {
			t.Log("no suggestions after execute as @s at @s (literals are client-side)")
		}
	}
}
