package packconv

import (
	"fmt"
	"sort"
	"strings"
)

// Status is what happened to one thing in a conversion.
type Status string

const (
	Converted    Status = "converted"
	Approximated Status = "approximated"
	Skipped      Status = "skipped"
)

// Report categories.
const (
	catPack     = "pack"
	catItem     = "item"
	catArmour   = "armour"
	catBlock    = "block"
	catEntity   = "entity"
	catOverride = "override"
	catSound    = "sound"
	catLang     = "lang"
	catOther    = "not converted"
)

// ReportEntry is one line of the report.
type ReportEntry struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	Status   Status `json:"status"`
	Detail   string `json:"detail,omitempty"`
}

// PackReport describes one input pack.
type PackReport struct {
	Name, UUID, Version, Kind, Subpack, Origin string
	Files                                      int
	Encrypted                                  bool
}

// Report lists what a conversion did.
type Report struct {
	Packs   []PackReport
	Entries []ReportEntry
	// Counts per category and status.
	Counts map[string]map[Status]int
	SHA1   string
	Bytes  int
}

func packInfo(s *Source, kind string) PackReport {
	return PackReport{Name: s.Name, UUID: s.UUID, Version: s.Version, Kind: kind, Subpack: s.ActiveSubpack(),
		Origin: s.Origin, Files: s.Len(), Encrypted: s.Encrypted}
}

func (r *Report) add(cat, id string, st Status, detail string) {
	r.Entries = append(r.Entries, ReportEntry{Category: cat, ID: id, Status: st, Detail: detail})
}

func (r *Report) finish(res *Result) {
	r.SHA1, r.Bytes = res.SHA1, len(res.Zip)
	sort.SliceStable(r.Entries, func(i, j int) bool {
		a, b := r.Entries[i], r.Entries[j]
		if a.Category != b.Category {
			return catOrder(a.Category) < catOrder(b.Category)
		}
		return a.ID < b.ID
	})
	r.Counts = map[string]map[Status]int{}
	for _, e := range r.Entries {
		if r.Counts[e.Category] == nil {
			r.Counts[e.Category] = map[Status]int{}
		}
		r.Counts[e.Category][e.Status]++
	}
}

func catOrder(c string) int {
	for i, x := range []string{catPack, catItem, catArmour, catBlock, catEntity, catOverride, catSound, catLang, catOther} {
		if x == c {
			return i
		}
	}
	return 99
}

// Count is the number of entries of a category with a status.
func (r *Report) Count(cat string, st Status) int { return r.Counts[cat][st] }

// Markdown is the report as a human-readable Markdown document.
func (r *Report) Markdown() string {
	var sb strings.Builder
	sb.WriteString("# Resource pack conversion report\n\n")
	fmt.Fprintf(&sb, "Java pack: %d bytes, SHA-1 `%s`.\n\n", r.Bytes, r.SHA1)
	sb.WriteString("## Input packs\n\n| pack | kind | version | subpack | files | note |\n|---|---|---|---|---|---|\n")
	for _, p := range r.Packs {
		note := ""
		if p.Encrypted {
			note = "encrypted, no key: not converted"
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %d | %s |\n", mdCell(p.Name), p.Kind, p.Version, p.Subpack, p.Files, note)
	}
	sb.WriteString("\n## Summary\n\n| category | converted | approximated | skipped |\n|---|---|---|---|\n")
	cats := make([]string, 0, len(r.Counts))
	for c := range r.Counts {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool { return catOrder(cats[i]) < catOrder(cats[j]) })
	for _, c := range cats {
		fmt.Fprintf(&sb, "| %s | %d | %d | %d |\n", c, r.Counts[c][Converted], r.Counts[c][Approximated], r.Counts[c][Skipped])
	}
	for _, st := range []Status{Skipped, Approximated, Converted} {
		var rows []ReportEntry
		for _, e := range r.Entries {
			if e.Status == st && e.Category != catPack {
				rows = append(rows, e)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "\n## %s (%d)\n\n| category | id | detail |\n|---|---|---|\n", strings.ToUpper(string(st[:1]))+string(st[1:]), len(rows))
		for _, e := range rows {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", e.Category, mdCell(e.ID), mdCell(e.Detail))
		}
	}
	return sb.String()
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

// notConverted reports the parts of a resource pack Java has no counterpart for.
func (b *builder) notConverted(s *Source) {
	kinds := []struct{ dir, what string }{
		{"ui/", "JSON UI screens"},
		{"particles/", "particle effects"},
		{"fogs/", "fog definitions"},
		{"materials/", "custom materials/shaders"},
		{"shaders/", "shaders"},
		{"animation_controllers/", "animation controllers"},
		{"render_controllers/", "render controllers (only the default texture/geometry is used)"},
		{"biomes_client.json", "biome colours"},
		{"font/", "fonts/glyphs"},
		{"camera/", "camera presets"},
		{"textures/ui/", "UI textures"},
	}
	for _, k := range kinds {
		n := 0
		for _, f := range s.Files(k.dir) {
			if strings.HasSuffix(f, ".json") || strings.HasSuffix(f, ".png") || strings.HasSuffix(f, ".material") || strings.HasSuffix(f, ".bin") {
				n++
			}
		}
		if n > 0 {
			b.rep.add(catOther, k.what, Skipped, fmt.Sprintf("%d files in %s (%s): no Java equivalent in a resource pack", n, k.dir, s.Name))
		}
	}
}
