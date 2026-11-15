package bridge

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/df-mc/dragonfly/server/java/protocol/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// The Java client's own command code on the tree of a real BDS: tools/cmdcheck/CmdCheck.java
// decodes the commands packet with the 26.3 client classes and types each line of clientLines
// as the chat box does (parse, completion at the end of the line). Needs java, javac and the
// 26.3 client jar with its libraries (BB_CLIENT_JAR, BB_CLIENT_LIBS; skipped without them).

// clientLine is a command typed in the chat box (no slash) and what the player must get at its
// end: want are suggestions that must be offered (by the client itself or the bridge), ok that
// the whole line parses without an error (the client shows no red and no error text).
type clientLine struct {
	line string
	want []string
	ok   bool
	// known: a Bedrock syntax no Java parser accepts (logged, not failed).
	known bool
}

var clientLines = []clientLine{
	{line: "execute ", want: []string{"as", "at", "if", "run"}},
	{line: "execute as @s ", want: []string{"at", "run", "if"}},
	{line: "execute as @s at @s ", want: []string{"positioned", "run"}},
	{line: "execute as @a at @s if score @s ", want: []string{"kills"}},
	{line: "execute if score @s kills ", want: []string{"matches", "="}},
	{line: "execute if score @s kills matches 1.. ", want: []string{"run", "as"}},
	{line: "execute if score @s kills matches 1.. run ", want: []string{"say", "give"}},
	{line: "execute if score @s kills >= @p kills run say hi", ok: true},
	{line: "execute if score @s kills <= @p kills ", want: []string{"run"}},
	{line: "execute if score @s kills matches !1..3 run say hi", ok: true},
	{line: "execute if block ~ ~-1 ~ ", want: []string{"stone"}},
	{line: "execute if block ~ ~-1 ~ stone ", want: []string{"run"}},
	{line: "execute unless block ~ ~-1 ~ wool [\"color\"=\"red\"] run say hi", ok: true},
	{line: "execute if entity @e[type=cow] ", want: []string{"run"}},
	{line: "execute positioned ~ ~1 ~ run tp @s ~ ~ ~", ok: true},
	{line: "execute positioned as @p ", want: []string{"run"}},
	{line: "execute align xyz run ", want: []string{"say"}},
	{line: "execute in ", want: []string{"nether", "overworld", "the_end"}},
	{line: "execute rotated ~ ~ run ", want: []string{"say"}},
	{line: "execute facing entity @p eyes run say hi", ok: true},
	{line: "execute anchored eyes run ", want: []string{"say"}},
	{line: "execute run ", want: []string{"say"}},
	{line: "scoreboard ", want: []string{"objectives", "players"}},
	{line: "scoreboard players ", want: []string{"set", "add", "operation", "test", "random", "reset", "list"}},
	{line: "scoreboard players set @s ", want: []string{"kills"}},
	{line: "scoreboard players set @s kills 5", ok: true},
	{line: "scoreboard players add * kills 1", ok: true},
	{line: "scoreboard players operation @s kills += @p deaths", ok: true},
	{line: "scoreboard players operation @s kills ", want: []string{"+=", "><"}},
	{line: "scoreboard players test @s kills * 5", ok: true},
	{line: "scoreboard players random @s kills 1 10", ok: true},
	{line: "scoreboard objectives add kills dummy \"Kill Count\"", ok: true},
	{line: "scoreboard objectives setdisplay ", want: []string{"sidebar", "list", "belowname"}},
	{line: "scoreboard objectives setdisplay sidebar ", want: []string{"kills"}},
	{line: "scoreboard objectives setdisplay sidebar kills ascending", ok: true},
	{line: "scoreboard objectives remove ", want: []string{"kills"}},
	{line: "tp ", want: []string{"@s", "Steve"}},
	{line: "tp @s ", want: []string{"@p", "~"}},
	{line: "tp @s ~ ~10 ~ ", want: []string{"facing"}},
	{line: "tp @s 0 64 0 facing @p", ok: true},
	{line: "teleport @s ~ ~ ~ 90 0", ok: true},
	{line: "give @s ", want: []string{"diamond"}},
	{line: "give @s dia", want: []string{"diamond"}},
	{line: "give @s minecraft:diamond 5", ok: true},
	{line: "give @s diamond 1 0 {\"minecraft:keep_on_death\":{}}", ok: true},
	{line: "effect @s ", want: []string{"speed", "clear"}},
	{line: "effect @s speed ", want: []string{"infinite"}},
	{line: "effect @s speed 10 1 ", want: []string{"true", "false"}},
	{line: "effect @s speed infinite 1 true", ok: true},
	{line: "effect @s clear speed", ok: true},
	{line: "gamerule ", want: []string{"keepinventory", "randomtickspeed"}},
	{line: "gamerule keepinventory ", want: []string{"true", "false"}},
	{line: "gamerule randomtickspeed 3", ok: true},
	{line: "setblock ~ ~ ~ ", want: []string{"stone"}},
	{line: "setblock ~ ~ ~ stone ", want: []string{"replace", "destroy", "keep"}},
	{line: "setblock ~ ~ ~ wool [\"color\"=\"red\"] replace", ok: true},
	{line: "setblock ~ ~ ~ wool [\"color\"=\"red\", \"x\"=1] destroy", ok: true},
	{line: "fill ~ ~ ~ ~1 ~1 ~1 stone replace dirt", ok: true},
	{line: "particle minecraft:basic_flame_particle ~ ~1 ~", ok: true},
	{line: "playsound random.click @s ~ ~ ~ 1 1", ok: true},
	{line: "summon ", want: []string{"cow"}},
	{line: "summon cow ~ ~ ~ 0 0 minecraft:entity_spawned \"Bob\"", ok: true},
	{line: "summon zombie \"Big Bob\" ~ ~ ~", ok: true},
	{line: "tag @s add ", want: []string{"red"}},
	{line: "tag @s add red", ok: true},
	{line: "title @s title hello world", ok: true},
	{line: "title @s times 10 20 10", ok: true},
	{line: "clear @s diamond 0 1", ok: true},
	{line: "kill @e[type=cow]", ok: true},
	{line: "enchant @s sharpness 5", ok: true},
	{line: "gamemode ", want: []string{"creative", "survival"}},
	{line: "gamemode c @s", ok: true},
	{line: "camera @s set minecraft:free", ok: true},
	{line: "tellraw @a {\"rawtext\":[{\"text\":\"hi\"}]}", ok: true},
	{line: "time set ", want: []string{"day", "night"}},
	{line: "weather ", want: []string{"clear", "rain", "thunder"}},
	{line: "xp 5L @s", ok: true},
	{line: "spreadplayers ~ ~ 1 10 @a", ok: true},
	{line: "say hello @a", ok: true},
	{line: "bb ", want: []string{"join", "status"}},
	{line: "time set day", ok: true},
	{line: "tp \"Some Player\" ~ ~ ~", ok: true},
	{line: "execute as @e[type=cow,tag=a,scores={kills=1..}] run say hi", ok: true},
	// Bedrock-only selector arguments and coordinates without spaces: red on Java.
	{line: "execute as @e[r=5] run say hi", ok: true, known: true},
	{line: "tp @p[c=1,m=c] ~ ~ ~", ok: true, known: true},
	{line: "tp @s ~~1~", ok: true, known: true},
}

// TestCommandTreeOneAskPerParent: no node has two children asking the server (the client cancels
// the first request and the completion never finishes).
func TestCommandTreeOneAskPerParent(t *testing.T) {
	tr, _ := bdsTree(t)
	var w wire.Writer
	tr.encode(&w, func(id int32) int32 { return id })
	nodes := decodeTree(t, w.B)
	asking := 0
	for _, n := range nodes {
		if n.suggest != "" {
			asking++
		}
	}
	for i, n := range nodes {
		k := 0
		for _, c := range n.children {
			if nodes[c].suggest != "" {
				k++
			}
		}
		if k > 1 {
			t.Errorf("node %d %q has %d children asking the server", i, n.name, k)
		}
	}
	t.Logf("%d nodes, %d asking the server", len(nodes), asking)
}

type clientResult struct {
	Line        string   `json:"line"`
	Asks        int      `json:"asks"`
	AskInputs   []string `json:"ask_inputs"`
	ParsedTo    int      `json:"parsed_to"`
	CanRead     bool     `json:"can_read"`
	Errors      []string `json:"errors"`
	Trailing    string   `json:"trailing"`
	Parent      string   `json:"parent"`
	Usage       []string `json:"usage"`
	Hung        bool     `json:"hung"`
	Suggestions []string `json:"suggestions"`
	Count       int      `json:"count"`
}

func clientClasspath(t *testing.T) string {
	jar := os.Getenv("JB_CLIENT_JAR")
	if jar == "" {
		t.Skip("JB_CLIENT_JAR not set (the 26.3 client jar)")
	}
	libs := os.Getenv("JB_CLIENT_LIBS")
	if libs == "" {
		t.Skip("JB_CLIENT_LIBS not set (the 26.3 client libraries)")
	}
	if _, err := os.Stat(jar); err != nil {
		t.Skip("no 26.3 client jar:", err)
	}
	for _, tool := range []string{"java", "javac"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no", tool)
		}
	}
	cp := []string{jar}
	_ = filepath.Walk(libs, func(path string, info os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(path, ".jar") {
			cp = append(cp, path)
		}
		return nil
	})
	return strings.Join(cp, string(os.PathListSeparator))
}

// bdsTree is the tree a Java client gets on the BDS of the dump (with the bridge's commands).
func bdsTree(t *testing.T) (*cmdTree, map[string][]string) {
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
	tr := newCmdTree()
	localTree(tr)
	tr.addBedrock(&pk)
	tr.resolve()
	soft := map[string][]string{}
	for _, e := range pk.DynamicEnums {
		soft[e.Type] = slices.Clone(e.Values)
	}
	soft["ScoreboardObjectives"] = []string{"kills", "deaths"}
	soft["TagValues"] = []string{"red", "blue"}
	return tr, soft
}

// overloadLines are one line per overload of every BDS command, each parameter given a typical
// Bedrock value of its type: the client must parse every one without an error.
func overloadLines(t *testing.T) []clientLine {
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
	sample := map[uint32]string{
		protocol.CommandArgTypeInt: "1", protocol.CommandArgTypeFloat: "1.5", protocol.CommandArgTypeRValue: "~10",
		protocol.CommandArgTypeWildcardInt: "*", protocol.CommandArgTypeOperator: "+=",
		protocol.CommandArgTypeCompareOperator: ">=", protocol.CommandArgTypeTarget: "@s",
		protocol.CommandArgTypeWildcardTarget: "*", protocol.CommandArgTypeFilepath: "My/func",
		protocol.CommandArgTypeIntegerRange: "!1..3", protocol.CommandArgTypeString: "minecraft:free",
		protocol.CommandArgTypeBlockPosition: "~ ~-1 ~", protocol.CommandArgTypePosition: "~ ~1.5 ~",
		protocol.CommandArgTypeMessage: "hello world", bedrockMessageRoot: "hello @a",
		protocol.CommandArgTypeRawText: "ws://127.0.0.1:1", protocol.CommandArgTypeJSON: `{"rawtext":[{"text":"a b"}]}`,
		protocol.CommandArgTypeBlockStates: `["color"="red", "x"=1]`, protocol.CommandArgTypeClockTimeMarker: "day",
	}
	var out []clientLine
	seen := map[string]bool{}
	for _, c := range pk.Commands {
		if !validLiteral(strings.ToLower(c.Name)) {
			continue
		}
	overloads:
		for _, o := range c.Overloads {
			words := []string{strings.ToLower(c.Name)}
		params:
			for _, prm := range o.Parameters {
				base := prm.Type & 0xffff
				switch {
				case prm.Type&protocol.CommandArgTypeChainedCommand != 0:
					words = append(words, "run", "say", "hi")
					break params
				case prm.Type&protocol.CommandArgEnum != 0:
					vals := enumValues(&pk, pk.Enums[base])
					if len(vals) == 0 {
						continue overloads
					}
					words = append(words, vals[0])
				case prm.Type&protocol.CommandArgSoftEnum != 0:
					words = append(words, "kills")
				case prm.Type&protocol.CommandArgSuffixed != 0:
					words = append(words, "5"+pk.Suffixes[base])
				case base == protocol.CommandArgTypeCommand || base == protocol.CommandArgTypeSlashCommand || base == bedrockCommandNew:
					words = append(words, "say", "hi")
					break params
				default:
					s, ok := sample[base]
					if !ok {
						t.Logf("/%s %s: no sample for type %d", c.Name, prm.Name, base)
						continue overloads
					}
					words = append(words, s)
				}
			}
			line := strings.Join(words, " ")
			if !seen[line] {
				seen[line] = true
				out = append(out, clientLine{line: line, ok: true})
			}
		}
	}
	return out
}

func TestJavaClientCommands(t *testing.T) {
	cp := clientClasspath(t)
	dir := t.TempDir()
	clientLines := append(slices.Clone(clientLines), overloadLines(t)...)
	src, err := filepath.Abs("../tools/cmdcheck/CmdCheck.java")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("javac", "-nowarn", "-cp", cp, "-d", dir, src).CombinedOutput(); err != nil {
		t.Fatalf("javac: %v\n%s", err, out)
	}
	tr, soft := bdsTree(t)
	var w wire.Writer
	tr.encode(&w, func(id int32) int32 { return id })
	if err := os.WriteFile(filepath.Join(dir, "tree.bin"), w.B, 0o644); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for _, l := range clientLines {
		lines.WriteString(l.line + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("java", "-Xmx512m", "-cp", dir+string(os.PathListSeparator)+cp, "CmdCheck", filepath.Join(dir, "tree.bin"), filepath.Join(dir, "lines.txt"))
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("CmdCheck: %v\n%s", err, stderr.String())
	}
	results := map[string]clientResult{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r clientResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("CmdCheck output %q: %v", sc.Text(), err)
		}
		results[r.Line] = r
	}
	p := &Player{}
	failed := 0
	for _, l := range clientLines {
		r, ok := results[l.line]
		if !ok {
			t.Errorf("/%s: no result", l.line)
			continue
		}
		var problems []string
		if r.Asks > 1 {
			problems = append(problems, "several ask_server requests in one completion")
		}
		if r.Hung {
			problems = append(problems, "the completion never finishes")
		}
		got := slices.Clone(r.Suggestions)
		if i := slices.Index(got, "<ASK>"); i >= 0 {
			got = slices.Delete(got, i, i+1)
			in := r.AskInputs[len(r.AskInputs)-1]
			_, vals := tr.suggestions(p, strings.TrimPrefix(in, "/"), soft)
			got = append(got, vals...)
		}
		for _, wnt := range l.want {
			if !slices.Contains(got, wnt) {
				problems = append(problems, "no suggestion "+wnt)
			}
		}
		if l.ok && (len(r.Errors) > 0 || r.CanRead) {
			problems = append(problems, "does not parse")
		}
		if l.known {
			t.Logf("/%s (Bedrock-only syntax): %v, errors %v", l.line, problems, r.Errors)
			continue
		}
		if len(problems) > 0 {
			failed++
			t.Errorf("/%s: %s\n\tparsed to %d, errors %v %s, parent %s, usage %v, suggestions %v (%d)",
				l.line, strings.Join(problems, "; "), r.ParsedTo, r.Errors, r.Trailing, r.Parent, firstN(r.Usage, 4), firstN(got, 12), r.Count)
		}
	}
	t.Logf("%d of %d lines as a Java client wants them", len(clientLines)-failed, len(clientLines))
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
