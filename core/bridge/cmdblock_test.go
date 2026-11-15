package bridge

import (
	"testing"

	"javabridge/core/internal/blocks"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestCommandBlockStates: the three Bedrock command blocks are Java command blocks with their
// facing and conditional, listed as minecraft:command_block block entities.
func TestCommandBlockStates(t *testing.T) {
	pal, err := blocks.NewPalette(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	faces := []string{"down", "up", "north", "south", "west", "east"}
	for _, name := range []string{"command_block", "repeating_command_block", "chain_command_block"} {
		for f := int32(0); f < 6; f++ {
			for _, cond := range []uint8{0, 1} {
				idx, ok := pal.Lookup("minecraft:"+name, map[string]any{"facing_direction": f, "conditional_bit": cond})
				if !ok {
					t.Fatalf("%s %d %d: not in the palette", name, f, cond)
				}
				if be := pal.BlockEntity(idx); be != "minecraft:command_block" {
					t.Errorf("%s: block entity %q", name, be)
				}
				s := pal.Java(idx, false)
				jb := blocks.JavaBlockOf(s)
				if jb == nil || jb.Name != "minecraft:"+name {
					t.Fatalf("%s: Java %v", name, jb)
				}
				if got, _ := jb.Get(s, "facing"); got != faces[f] {
					t.Errorf("%s facing %d: %s", name, f, got)
				}
				want := map[uint8]string{0: "false", 1: "true"}[cond]
				if got, _ := jb.Get(s, "conditional"); got != want {
					t.Errorf("%s conditional %d: %s", name, cond, got)
				}
			}
		}
	}
}

func TestCommandBlockNBT(t *testing.T) {
	p := &Player{}
	be := map[string]any{"Command": "say hi", "CustomName": "Greeter", "SuccessCount": int32(1), "TrackOutput": uint8(1),
		"LastOutput": "commands.say.success", "LastOutputParams": []any{}, "powered": uint8(1), "conditionMet": uint8(1),
		"auto": uint8(1), "LPRedstoneMode": uint8(0), "LPCondionalMode": uint8(1), "LPCommandMode": int32(1), "TickDelay": int32(5),
		"ExecuteOnFirstTick": uint8(1)}
	m := decodeJavaNBT(t, p.beNBT(cmdBlockType, be))
	if m["Command"] != "say hi" || m["CustomName"] != "Greeter" || m["SuccessCount"] != int32(1) || m["TrackOutput"] != uint8(1) ||
		m["powered"] != uint8(1) || m["conditionMet"] != uint8(1) || m["auto"] != uint8(1) {
		t.Fatalf("command block: %v", m)
	}
	if _, ok := m["LastOutput"].(string); !ok {
		t.Fatalf("no LastOutput: %v", m)
	}
	be["TrackOutput"], be["auto"] = uint8(0), uint8(0)
	m = decodeJavaNBT(t, p.beNBT(cmdBlockType, be))
	if _, ok := m["LastOutput"]; ok || m["auto"] != uint8(0) || m["TrackOutput"] != uint8(0) {
		t.Fatalf("not tracking: %v", m)
	}
	if m = decodeJavaNBT(t, p.beNBT(cmdBlockType, map[string]any{})); m["Command"] != "" || m["TrackOutput"] != uint8(1) {
		t.Fatalf("empty: %v", m)
	}

	// Java REDSTONE (2) + automatic + conditional + track output -> impulse, no redstone needed.
	u := commandBlockUpdate(cube.Pos{1, 2, 3}, be, "say new", 2, 7)
	if !u.Block || u.Mode != packet.CommandBlockImpulse || u.NeedsRedstone || !u.Conditional || !u.ShouldTrackOutput ||
		u.Command != "say new" || u.Name != "Greeter" || u.TickDelay != 5 || !u.ExecuteOnFirstTick || u.Position[2] != 3 {
		t.Fatalf("update: %+v", u)
	}
	if u := commandBlockUpdate(cube.Pos{}, be, "x", 0, 0); u.Mode != packet.CommandBlockChain || !u.NeedsRedstone || u.Conditional || u.LastOutput != "" {
		t.Fatalf("chain update: %+v", u)
	}
	if u := commandBlockUpdate(cube.Pos{}, be, "x", 1, 4); u.Mode != packet.CommandBlockRepeating || u.NeedsRedstone {
		t.Fatalf("repeating update: %+v", u)
	}
}
