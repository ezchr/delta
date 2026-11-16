// Command genparticles writes internal/particles/particles_gen.go: the Bedrock -> Java particle
// tables.
//
//	go run ./tools/genparticles \
//	    -geyser <Geyser>/core/src/main/resources/mappings/particles.json,<GeyserMC mappings>/particles.json \
//	    -samples <bedrock-samples>/resource_pack/particles \
//	    > internal/particles/particles_gen.go
//
// (run in core/; the first -geyser file wins where both name a Bedrock particle).
//
// Geyser's particles.json maps Java particles to Bedrock (an "eventType", a Bedrock particle type
// or level event, and a "bedrockId", a named particle effect); it is reversed here. Where several
// Java particles map to one Bedrock one, the Java name closest to the Bedrock one wins (shared
// name parts, then fewer extra parts, then the shorter name). Bedrock particle type ids are the
// ParticleType iota block of the vendored gophertunnel (level_event.go), level event ids its
// LevelEvent constants. Bedrock particles Geyser does not map get the hand-picked Java particle in
// fallbacks.go, or are listed there as having none. Every Java particle, block and item written is
// checked against the 26.3 registries. With -samples (the vanilla resource pack's particles
// folder), every vanilla particle effect must be mapped or listed as skipped.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"delta/core/internal/blocks"

	v777 "github.com/ezchr/go-mcjava/v777"
)

func main() {
	geyser := flag.String("geyser", "", "Geyser particles.json files, comma separated (later ones add to earlier ones)")
	gt := flag.String("gt", "third_party/gophertunnel/minecraft/protocol/packet/level_event.go", "gophertunnel level_event.go")
	samples := flag.String("samples", "", "vanilla resource pack particles folder (optional coverage check)")
	flag.Parse()
	if *geyser == "" {
		log.Fatal("-geyser is required")
	}
	src, err := os.ReadFile(*gt)
	if err != nil {
		log.Fatal(err)
	}
	types, events, typeNames, eventNames := bedrockIDs(string(src))

	type mapping struct {
		Event, Bedrock string
	}
	byJava := map[string]mapping{}
	for _, f := range strings.Split(*geyser, ",") {
		var m map[string]struct {
			EventType string `json:"eventType"`
			BedrockID string `json:"bedrockId"`
		}
		b, err := os.ReadFile(f)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(b, &m); err != nil {
			log.Fatalf("%s: %v", f, err)
		}
		for j, v := range m {
			j = strings.ToLower(j)
			old := byJava[j]
			if v.EventType != "" {
				old.Event = v.EventType
			}
			// The first file (our fork) wins for named particles: it maps some Java particles to
			// vanilla ones where upstream uses a Geyser-only effect.
			if v.BedrockID != "" && (old.Bedrock == "" || strings.HasPrefix(old.Bedrock, "geyseropt:")) {
				old.Bedrock = v.BedrockID
			}
			byJava[j] = old
		}
	}

	legacyCand := map[int32][]string{}
	eventCand := map[int32][]string{}
	namedCand := map[string][]string{}
	for j, m := range byJava {
		if !javaParticle(j) {
			warn("no Java particle %s", j)
			continue
		}
		if m.Event != "" {
			n := norm(m.Event)
			if id, ok := types[n]; ok {
				legacyCand[id] = append(legacyCand[id], j)
			} else if id, ok := events[n]; ok {
				eventCand[id] = append(eventCand[id], j)
			} else {
				warn("no Bedrock particle type or level event %s (%s)", m.Event, j)
			}
		}
		if m.Bedrock != "" && strings.HasPrefix(m.Bedrock, "minecraft:") {
			namedCand[m.Bedrock] = append(namedCand[m.Bedrock], j)
		}
	}

	legacy := map[int32]entry{}
	for id, c := range legacyCand {
		legacy[id] = entry{Java: best(typeNames[id], c), Source: "geyser"}
	}
	levelEvents := map[int32]entry{}
	for id, c := range eventCand {
		levelEvents[id] = entry{Java: best(eventNames[id], c), Source: "geyser"}
	}
	named := map[string]entry{}
	for b, c := range namedCand {
		short := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(b, "minecraft:"), "_particle"), "_emitter")
		named[b] = entry{Java: best(short, c), Source: "geyser"}
	}

	// Overrides and fallbacks.
	for name, e := range legacyFallbacks {
		id, ok := types[norm(name)]
		if !ok {
			log.Fatalf("fallback: no Bedrock particle type %s", name)
		}
		if _, ok := legacy[id]; ok && !e.Override {
			continue
		}
		e.Source = "fallback"
		legacy[id] = e
	}
	for name, e := range namedFallbacks {
		id := "minecraft:" + name
		if _, ok := named[id]; ok && !e.Override {
			continue
		}
		e.Source = "fallback"
		named[id] = e
	}
	for id, e := range legacy {
		check(fmt.Sprint("particle type ", typeNames[id]), e)
	}
	for id, e := range named {
		check(id, e)
	}
	for id, e := range levelEvents {
		check(fmt.Sprint("level event ", eventNames[id]), e)
	}

	var unmappedTypes []string
	for id := int32(1); id <= maxID(types); id++ {
		if _, ok := legacy[id]; !ok {
			unmappedTypes = append(unmappedTypes, fmt.Sprintf("%d %s", id, typeNames[id]))
			if !legacySkip[typeNames[id]] {
				warn("particle type %d (%s) neither mapped nor skipped", id, typeNames[id])
			}
		}
	}
	if *samples != "" {
		for _, id := range vanillaParticles(*samples) {
			if _, ok := named[id]; !ok && !namedSkip[strings.TrimPrefix(id, "minecraft:")] {
				warn("vanilla particle %s neither mapped nor skipped", id)
			}
		}
	}

	var b bytes.Buffer
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("// Code generated by tools/genparticles; DO NOT EDIT.")
	w("")
	w("package particles")
	w("")
	w("// legacy maps Bedrock particle types (LevelEvent 0x4000|type, ParticleGenericSpawn) to Java particles.")
	w("var legacy = map[int32]Particle{")
	for _, id := range sortedInts(legacy) {
		w("\t%d: %s, // %s, %s", id, legacy[id].lit(), typeNames[id], legacy[id].Source)
	}
	w("}")
	w("")
	w("// named maps Bedrock particle effects (SpawnParticleEffect) to Java particles.")
	w("var named = map[string]Particle{")
	for _, id := range sortedStrings(named) {
		w("\t%q: %s, // %s", id, named[id].lit(), named[id].Source)
	}
	w("}")
	w("")
	w("// levelEvents maps Bedrock particle level events Geyser maps a Java particle to.")
	w("var levelEvents = map[int32]Particle{")
	for _, id := range sortedInts(levelEvents) {
		w("\t%d: %s, // %s, %s", id, levelEvents[id].lit(), eventNames[id], levelEvents[id].Source)
	}
	w("}")
	w("")
	w("// Skipped lists the Bedrock particles without a Java equal: particle types (id and name) and")
	w("// named effects.")
	w("var Skipped = struct {")
	w("\tTypes []string")
	w("\tNamed []string")
	w("}{")
	w("\tTypes: []string{")
	for _, s := range unmappedTypes {
		w("\t\t%q,", s)
	}
	w("\t},")
	w("\tNamed: []string{")
	var skipped []string
	for n := range namedSkip {
		skipped = append(skipped, "minecraft:"+n)
	}
	sort.Strings(skipped)
	for _, s := range skipped {
		w("\t\t%q,", s)
	}
	w("\t},")
	w("}")
	out, err := format.Source(b.Bytes())
	if err != nil {
		log.Fatalf("gofmt: %v\n%s", err, b.Bytes())
	}
	os.Stdout.Write(out)
}

// entry is a Java particle with its options, as written to the table.
type entry struct {
	Java     string
	Block    string // fixed block (Java name without namespace) for block particles
	Item     string // fixed item for item particles
	Colour   uint32 // ARGB: dust, entity_effect...
	Colour2  uint32 // dust_color_transition target colour
	Override bool   // fallbacks only: replaces Geyser's choice
	Source   string
}

func (e entry) lit() string {
	var f []string
	f = append(f, fmt.Sprintf("Java: %q", e.Java))
	if e.Block != "" {
		f = append(f, fmt.Sprintf("Block: %q", e.Block))
	}
	if e.Item != "" {
		f = append(f, fmt.Sprintf("Item: %q", e.Item))
	}
	if e.Colour != 0 {
		f = append(f, fmt.Sprintf("Colour: 0x%08x", e.Colour))
	}
	if e.Colour2 != 0 {
		f = append(f, fmt.Sprintf("Colour2: 0x%08x", e.Colour2))
	}
	return "{" + strings.Join(f, ", ") + "}"
}

func check(what string, e entry) {
	if !javaParticle(e.Java) {
		log.Fatalf("%s: no Java particle %s", what, e.Java)
	}
	if e.Block != "" && blocks.JavaBlockNamed("minecraft:"+e.Block) == nil {
		log.Fatalf("%s: no Java block %s", what, e.Block)
	}
	if e.Item != "" {
		if _, ok := v777.Builtin["minecraft:item"]["minecraft:"+e.Item]; !ok {
			log.Fatalf("%s: no Java item %s", what, e.Item)
		}
	}
}

func javaParticle(name string) bool {
	_, ok := v777.Builtin["minecraft:particle_type"]["minecraft:"+name]
	return ok
}

func norm(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return -1
	}, s)
}

// bedrockIDs reads the particle types (iota block) and level events of level_event.go, keyed by
// normalised name, and their Go names by id. Level events are also keyed as Cloudburst names
// them: "Particles..." as "particle...", and those without the word with "particle" in front.
func bedrockIDs(src string) (types, events map[string]int32, typeNames, eventNames map[int32]string) {
	types, events = map[string]int32{}, map[string]int32{}
	typeNames, eventNames = map[int32]string{}, map[int32]string{}
	blk := regexp.MustCompile(`(?s)ParticleTypeUndefined = iota(.*?)\)`).FindStringSubmatch(src)
	if blk == nil {
		log.Fatal("no ParticleType block")
	}
	for i, m := range regexp.MustCompile(`(?m)^\s*ParticleType(\w+)\s*$`).FindAllStringSubmatch(blk[1], -1) {
		types[norm(m[1])] = int32(i + 1)
		typeNames[int32(i+1)] = m[1]
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*LevelEvent(\w+)\s*=\s*(\d+)\s*$`).FindAllStringSubmatch(src, -1) {
		var v int32
		fmt.Sscan(m[2], &v)
		n := norm(m[1])
		events[n] = v
		if _, ok := eventNames[v]; !ok {
			eventNames[v] = m[1]
		}
		switch {
		case strings.HasPrefix(m[1], "Particles"):
			events["particle"+norm(strings.TrimPrefix(m[1], "Particles"))] = v
		case !strings.HasPrefix(m[1], "Particle"):
			events["particle"+n] = v
		}
	}
	return types, events, typeNames, eventNames
}

func maxID(m map[string]int32) int32 {
	var x int32
	for _, v := range m {
		x = max(x, v)
	}
	return x
}

// best picks the Java name closest to the Bedrock one.
func best(bedrock string, cands []string) string {
	parts := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, p := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == '_' || r == '.' }) {
			m[p] = true
		}
		return m
	}
	bp := parts(bedrock)
	nb := norm(bedrock)
	score := func(c string) (int, int, int) {
		shared, extra := 0, 0
		for p := range parts(c) {
			if bp[p] || strings.Contains(nb, p) {
				shared++
			} else {
				extra++
			}
		}
		return shared, extra, len(c)
	}
	sort.Slice(cands, func(i, j int) bool {
		si, ei, li := score(cands[i])
		sj, ej, lj := score(cands[j])
		if si != sj {
			return si > sj
		}
		if ei != ej {
			return ei < ej
		}
		if li != lj {
			return li < lj
		}
		return cands[i] < cands[j]
	})
	return cands[0]
}

func vanillaParticles(dir string) []string {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		log.Fatal(err)
	}
	re := regexp.MustCompile(`"identifier"\s*:\s*"([^"]+)"`)
	var ids []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Fatal(err)
		}
		if m := re.FindSubmatch(b); m != nil {
			ids = append(ids, string(m[1]))
		}
	}
	sort.Strings(ids)
	return ids
}

func sortedInts(m map[int32]entry) []int32 {
	var k []int32
	for id := range m {
		k = append(k, id)
	}
	sort.Slice(k, func(i, j int) bool { return k[i] < k[j] })
	return k
}

func sortedStrings(m map[string]entry) []string {
	var k []string
	for id := range m {
		k = append(k, id)
	}
	sort.Strings(k)
	return k
}

func warn(f string, a ...any) { fmt.Fprintf(os.Stderr, "genparticles: "+f+"\n", a...) }
