// Command packconv converts Bedrock resource packs (and behaviour pack definitions) to a Java
// resource pack, a mapping file and a report.
//
//	packconv -in <pack dir/zip/mcpack/mcaddon>[,more...] [-in ...] [-bp <behaviour packs>]
//	         -out java.zip [-map mapping.json] [-report report.md]
//	packconv -bds <BDS root> [-world <name>] -out java.zip ...
//
// Packs are given bottom of the stack first (later packs win). -subpack picks a subpack
// ("folder" for every pack, or "<pack uuid>=folder"); by default each pack uses its first one.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"delta/packconv"
)

type list []string

func (l *list) String() string { return strings.Join(*l, ",") }
func (l *list) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func main() {
	var in, bp, subpacks list
	flag.Var(&in, "in", "resource packs (dirs, .zip, .mcpack, .mcaddon), bottom of the stack first; repeatable or comma separated")
	flag.Var(&bp, "bp", "behaviour packs whose item/block definitions are used")
	flag.Var(&subpacks, "subpack", `subpack to use: "folder" (every pack) or "<uuid>=folder"; "none" for none`)
	out := flag.String("out", "", "Java resource pack zip to write")
	mapPath := flag.String("map", "", "mapping.json to write")
	report := flag.String("report", "", "report (Markdown) to write")
	ns := flag.String("ns", packconv.Namespace, "Java namespace of the converted assets")
	bds := flag.String("bds", "", "read the packs of a BDS installation's world instead of -in/-bp")
	world := flag.String("world", "", "BDS world folder name (default: server.properties level-name)")
	items := flag.String("items", "", `JSON file of runtime items: ["ns:id", ...] or {"ns:id": {"components": ...}}`)
	desc := flag.String("desc", "", "pack description")
	flag.Parse()
	if *out == "" || (len(in) == 0 && *bds == "") {
		flag.Usage()
		os.Exit(2)
	}
	var input packconv.Input
	input.Namespace, input.Description = *ns, *desc
	input.Slots = packconv.Candidates()
	if *bds != "" {
		p, err := packconv.FromBDS(*bds, *world)
		if err != nil {
			fail(err)
		}
		input.Packs, input.BehaviorPacks = p.Resource, p.Behaviour
		for _, m := range p.Missing {
			fmt.Fprintln(os.Stderr, "pack not found:", m)
		}
	}
	for _, p := range in {
		srcs, err := packconv.ReadPacks(p)
		if err != nil {
			fail(err)
		}
		for _, s := range srcs {
			if s.IsBehaviour() {
				input.BehaviorPacks = append(input.BehaviorPacks, s)
			} else {
				input.Packs = append(input.Packs, s)
			}
		}
	}
	for _, p := range bp {
		srcs, err := packconv.ReadPacks(p)
		if err != nil {
			fail(err)
		}
		input.BehaviorPacks = append(input.BehaviorPacks, srcs...)
	}
	choose := map[string]string{}
	all := ""
	for _, s := range subpacks {
		if k, v, ok := strings.Cut(s, "="); ok {
			choose[strings.ToLower(k)] = v
		} else {
			all = s
		}
	}
	for _, s := range input.Packs {
		sp := s.DefaultSubpack()
		if all != "" {
			sp = all
		}
		if v, ok := choose[strings.ToLower(s.UUID)]; ok {
			sp = v
		}
		if sp == "none" {
			sp = ""
		}
		if !s.SetSubpack(sp) {
			fmt.Fprintf(os.Stderr, "%s: no subpack %q\n", s.Name, sp)
		}
	}
	if *items != "" {
		b, err := os.ReadFile(*items)
		if err != nil {
			fail(err)
		}
		var ids []string
		var data map[string]map[string]any
		if json.Unmarshal(b, &ids) == nil {
			for _, id := range ids {
				input.Items = append(input.Items, packconv.Item{ID: id})
			}
		} else if err := json.Unmarshal(b, &data); err == nil {
			for id, d := range data {
				input.Items = append(input.Items, packconv.Item{ID: id, Data: d})
			}
		} else {
			fail(fmt.Errorf("%s: %w", *items, err))
		}
	}
	res, err := packconv.Convert(&input)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, res.Zip, 0o644); err != nil {
		fail(err)
	}
	if *mapPath != "" {
		if err := os.WriteFile(*mapPath, res.Mapping.MarshalIndent(), 0o644); err != nil {
			fail(err)
		}
	}
	if *report != "" {
		if err := os.WriteFile(*report, []byte(res.Report.Markdown()), 0o644); err != nil {
			fail(err)
		}
	}
	st := res.Stats
	fmt.Printf("%s: %d bytes, sha1 %s; overrides %d, items %d, blocks %d, entities %d, sounds %d, lang keys %d\n",
		*out, len(res.Zip), res.SHA1, st.Overrides, st.Items, st.Blocks, st.Entities, st.Sounds, st.LangKeys)
	for cat, c := range res.Report.Counts {
		fmt.Printf("  %-14s converted %d, approximated %d, skipped %d\n", cat, c[packconv.Converted], c[packconv.Approximated], c[packconv.Skipped])
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "packconv:", err)
	os.Exit(1)
}
