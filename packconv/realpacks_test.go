package packconv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Real-pack tests: they convert packs that live on the machine the tests run on and compare the
// result with hand-made Java versions of them. Nothing of those packs is in this repository; the
// tests skip when the variables are not set. PACKCONV_ROOT prefixes every path (a copy of the
// files elsewhere); PACKCONV_OUT, when set, receives the converted packs, mappings, reports and
// the comparison tables.
//
//	PACKCONV_ADDON_RP, _BP, _JAVA, _MANIFEST  an add-on with a hand-made Java port and its manifest
//	    ({"items": [{id, addon_id, java_base, kind, equipment_asset, head_model}],
//	      "blocks": [{id, addon_id, java_state}]}; ids are the port's item_model ids)
//	PACKCONV_PARTS_RP, _BP, _ENTITY, _JAVA, _PATTERN  an entity drawn bone by bone by hand
//	    (_ENTITY: a suffix of its identifier; _PATTERN: the hand-made part model's zip path with
//	    %s for the lower-case bone name)
//	PACKCONV_WHOLE_RP, _JAVA, _PATTERN  entities with hand-made whole models (%s: the identifier
//	    without its namespace; a model whose parent has the elements is followed)
//	PACKCONV_MODELS_RP, _JAVA           any pack and a hand-made counterpart: element counts/sizes
//	PACKCONV_UI_RP                      a pack with JSON UI (must be reported)

func realPath(t *testing.T, env string) string {
	t.Helper()
	p := os.Getenv(env)
	if p == "" {
		t.Skipf("%s not set", env)
	}
	p = filepath.Join(os.Getenv("PACKCONV_ROOT"), p)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s: %v", env, err)
	}
	return p
}

func readReal(t *testing.T, env string) *Source {
	t.Helper()
	s, err := ReadPack(realPath(t, env))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSubpack(s.DefaultSubpack())
	return s
}

func writeOut(t *testing.T, name string, res *Result, extra string) {
	dir := os.Getenv("PACKCONV_OUT")
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, name+".zip"), res.Zip, 0o644)
	_ = os.WriteFile(filepath.Join(dir, name+"_mapping.json"), res.Mapping.MarshalIndent(), 0o644)
	_ = os.WriteFile(filepath.Join(dir, name+"_report.md"), []byte(res.Report.Markdown()), 0o644)
	if extra != "" {
		_ = os.WriteFile(filepath.Join(dir, name+"_compare.md"), []byte(extra), 0o644)
	}
}

func checkValid(t *testing.T, res *Result) map[string][]byte {
	files := unzip(t, res.Zip)
	for n, b := range files {
		if (strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".mcmeta")) && !json.Valid(b) {
			t.Errorf("%s: invalid JSON", n)
		}
	}
	// Every model's textures and every item definition's models exist.
	for n, b := range files {
		if !strings.Contains(n, "/models/") || !strings.HasSuffix(n, ".json") {
			continue
		}
		var m struct {
			Textures map[string]string `json:"textures"`
		}
		_ = json.Unmarshal(b, &m)
		for _, tex := range m.Textures {
			if strings.HasPrefix(tex, "#") || strings.HasPrefix(tex, "minecraft:") {
				continue
			}
			ns, p, _ := strings.Cut(tex, ":")
			if _, ok := files["assets/"+ns+"/textures/"+p+".png"]; !ok {
				t.Errorf("%s: texture %s missing", n, tex)
			}
		}
	}
	return files
}

// itemTexture follows an item definition (its GUI model) to its layer0 texture and decodes it.
func itemTexture(files map[string][]byte, itemModel string) (image.Image, string) {
	ns, p, _ := strings.Cut(itemModel, ":")
	b, ok := files["assets/"+ns+"/items/"+p+".json"]
	if !ok {
		return nil, ""
	}
	var def struct {
		Model json.RawMessage `json:"model"`
	}
	_ = json.Unmarshal(b, &def)
	var model string
	kind := "flat"
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var m struct {
			Type     string          `json:"type"`
			Model    string          `json:"model"`
			Fallback json.RawMessage `json:"fallback"`
			Cases    []struct {
				When  any             `json:"when"`
				Model json.RawMessage `json:"model"`
			} `json:"cases"`
		}
		_ = json.Unmarshal(raw, &m)
		if m.Model != "" {
			model = m.Model
			return
		}
		for _, c := range m.Cases {
			w := fmt.Sprint(c.When)
			if strings.Contains(w, "head") {
				kind = "head"
				walk(m.Fallback)
				return
			}
			if strings.Contains(w, "gui") {
				kind = "3d"
				walk(c.Model)
				return
			}
		}
		walk(m.Fallback)
	}
	walk(def.Model)
	mns, mp, _ := strings.Cut(model, ":")
	mb, ok := files["assets/"+mns+"/models/"+mp+".json"]
	if !ok {
		return nil, kind
	}
	var m struct {
		Textures map[string]string `json:"textures"`
	}
	_ = json.Unmarshal(mb, &m)
	tex := m.Textures["layer0"]
	if tex == "" {
		tex = m.Textures["all"]
	}
	if tex == "" {
		return nil, kind
	}
	tns, tp, _ := strings.Cut(tex, ":")
	img, err := png.Decode(bytes.NewReader(files["assets/"+tns+"/textures/"+tp+".png"]))
	if err != nil {
		return nil, kind
	}
	return img, kind
}

// pixelDiff is the share of pixels that differ (1 when the sizes differ).
func pixelDiff(a, b image.Image) float64 {
	if a == nil || b == nil || a.Bounds().Size() != b.Bounds().Size() {
		return 1
	}
	n, d := 0, 0
	ab, bb := a.Bounds(), b.Bounds()
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			n++
			r1, g1, b1, a1 := a.At(ab.Min.X+x, ab.Min.Y+y).RGBA()
			r2, g2, b2, a2 := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			if a1 == 0 && a2 == 0 {
				continue
			}
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				d++
			}
		}
	}
	return float64(d) / float64(max(n, 1))
}

func TestRealAddonPort(t *testing.T) {
	rp, bp := readReal(t, "PACKCONV_ADDON_RP"), readReal(t, "PACKCONV_ADDON_BP")
	res, err := Convert(&Input{Packs: []*Source{rp}, BehaviorPacks: []*Source{bp}, Slots: Candidates()})
	if err != nil {
		t.Fatal(err)
	}
	ours := checkValid(t, res)
	var cmp strings.Builder
	defer func() { writeOut(t, "addon", res, cmp.String()) }()
	javaPath := realPath(t, "PACKCONV_ADDON_JAVA")
	zb, _ := os.ReadFile(javaPath)
	theirs := unzip(t, zb)
	mb, err := os.ReadFile(realPath(t, "PACKCONV_ADDON_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Items []struct {
			ID             string `json:"id"`
			AddonID        string `json:"addon_id"`
			JavaBase       string `json:"java_base"`
			Kind           string `json:"kind"`
			EquipmentAsset string `json:"equipment_asset"`
			HeadModel      bool   `json:"head_model"`
		} `json:"items"`
		Blocks []struct {
			ID        string `json:"id"`
			AddonID   string `json:"addon_id"`
			JavaState string `json:"java_state"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	type row struct{ id, present, base, model, icon, equip string }
	var rows []row
	counts := map[string]int{}
	setDiff := map[string]float64{}
	for _, it := range man.Items {
		if it.Kind == "block" {
			continue
		}
		counts["items"]++
		r := row{id: it.AddonID}
		im := res.Mapping.Items[it.AddonID]
		if im == nil {
			r.present = "**missing**"
			counts["missing"]++
			rows = append(rows, r)
			continue
		}
		r.present = "yes"
		if im.JavaBase == it.JavaBase {
			r.base = "same (" + im.JavaBase + ")"
			counts["base same"]++
		} else {
			r.base = fmt.Sprintf("**%s** vs %s", im.JavaBase, it.JavaBase)
		}
		theirIcon, theirKind := itemTexture(theirs, it.ID)
		ourIcon, _ := itemTexture(ours, im.ItemModel)
		if it.HeadModel {
			theirKind = "head"
		}
		ourKind := im.Model
		if ourKind == "handheld" {
			ourKind = "flat"
		}
		if ourKind == theirKind {
			r.model = "same (" + ourKind + ")"
			counts["model same"]++
		} else {
			r.model = fmt.Sprintf("**%s** vs %s", ourKind, theirKind)
		}
		switch d := pixelDiff(ourIcon, theirIcon); {
		case d == 0:
			r.icon = "identical"
			counts["icon identical"]++
		case ourIcon == nil || theirIcon == nil:
			r.icon = "**not comparable**"
		default:
			r.icon = fmt.Sprintf("**%.0f%% of pixels differ**", d*100)
		}
		if it.EquipmentAsset != "" {
			counts["equipment"]++
			if im.Equipment == nil || im.Equipment.Asset == "" {
				r.equip = "**none** vs " + it.EquipmentAsset
				if im.Model == "head" {
					r.equip = "3D head model (no asset) vs " + it.EquipmentAsset
				}
			} else {
				r.equip = "asset"
				counts["equipment asset"]++
				// Compare the layer textures.
				sns, set, _ := strings.Cut(it.EquipmentAsset, ":")
				our := strings.TrimPrefix(im.Equipment.Asset, res.Mapping.Namespace+":")
				for _, layer := range []string{"humanoid", "humanoid_leggings"} {
					a, _ := png.Decode(bytes.NewReader(ours["assets/"+res.Mapping.Namespace+"/textures/entity/equipment/"+layer+"/"+our+".png"]))
					b, _ := png.Decode(bytes.NewReader(theirs["assets/"+sns+"/textures/entity/equipment/"+layer+"/"+set+".png"]))
					if a != nil && b != nil {
						setDiff[set+" "+layer] = pixelDiff(a, b)
					}
				}
			}
		}
		rows = append(rows, r)
	}
	// Blocks.
	type brow struct{ id, present, tex string }
	var brows []brow
	for _, bl := range man.Blocks {
		counts["blocks"]++
		r := brow{id: bl.AddonID}
		bm := res.Mapping.Blocks[bl.AddonID]
		if bm == nil || bm.Slot < 0 {
			r.present = "**missing**"
			brows = append(brows, r)
			continue
		}
		r.present = bm.JavaState
		counts["blocks present"]++
		ourTex, _ := blockTextures(ours, res.Mapping.Namespace+":block/"+strings.ReplaceAll(bl.AddonID, ":", "/"))
		bns, bid, _ := strings.Cut(bl.ID, ":")
		theirTex, _ := blockTextures(theirs, bns+":block/"+bid)
		worst := 0.0
		for f, ti := range theirTex {
			worst = max(worst, pixelDiff(ourTex[f], ti))
		}
		if len(theirTex) == 0 {
			r.tex = "**hand-made model not found**"
		} else if worst == 0 {
			r.tex = "identical"
			counts["block textures identical"]++
		} else {
			r.tex = fmt.Sprintf("**%.0f%% differ**", worst*100)
		}
		brows = append(brows, r)
	}
	fmt.Fprintf(&cmp, "# Add-on: converted vs hand-made port\n\n%d items: %d missing, java base same %d, model kind same %d, icon identical %d; "+
		"%d with an equipment asset in the hand-made pack, %d with one here. %d blocks: %d converted, textures identical %d.\n\n",
		counts["items"], counts["missing"], counts["base same"], counts["model same"], counts["icon identical"],
		counts["equipment"], counts["equipment asset"], counts["blocks"], counts["blocks present"], counts["block textures identical"])
	cmp.WriteString("## Differences\n\n| item | base (ours vs hand-made) | model | icon | equipment |\n|---|---|---|---|---|\n")
	for _, r := range rows {
		if strings.Contains(r.present+r.base+r.model+r.icon+r.equip, "**") {
			fmt.Fprintf(&cmp, "| %s | %s | %s | %s | %s |\n", r.id, r.base, r.model, r.icon, r.equip)
		}
	}
	cmp.WriteString("\n## Armour layers (share of pixels that differ from the hand-made layer)\n\n| set layer | differ |\n|---|---|\n")
	for _, k := range sortedKeys(setDiff) {
		fmt.Fprintf(&cmp, "| %s | %.1f%% |\n", k, setDiff[k]*100)
	}
	cmp.WriteString("\n## Blocks\n\n| block | state here | textures vs hand-made |\n|---|---|---|\n")
	for _, r := range brows {
		fmt.Fprintf(&cmp, "| %s | %s | %s |\n", r.id, r.present, r.tex)
	}
	cmp.WriteString("\n## All items\n\n| item | present | base | model | icon | equipment |\n|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&cmp, "| %s | %s | %s | %s | %s | %s |\n", r.id, r.present, r.base, r.model, r.icon, r.equip)
	}
	t.Logf("add-on: %v", counts)
	if counts["missing"] > 0 {
		t.Errorf("%d items missing", counts["missing"])
	}
}

// blockTextures decodes a block model's face textures (cube_all / cube parents).
func blockTextures(files map[string][]byte, model string) (map[string]image.Image, bool) {
	ns, p, _ := strings.Cut(model, ":")
	b, ok := files["assets/"+ns+"/models/"+p+".json"]
	if !ok {
		return nil, false
	}
	var m struct {
		Textures map[string]string `json:"textures"`
	}
	_ = json.Unmarshal(b, &m)
	out := map[string]image.Image{}
	for _, f := range []string{"down", "up", "north", "south", "west", "east"} {
		tex := m.Textures[f]
		if tex == "" {
			tex = m.Textures["all"]
		}
		tns, tp, _ := strings.Cut(tex, ":")
		img, err := png.Decode(bytes.NewReader(files["assets/"+tns+"/textures/"+tp+".png"]))
		if err == nil {
			out[f] = img
		}
	}
	return out, true
}

// elementStats counts a model file's elements and their bounds.
func elementStats(files map[string][]byte, file string) (int, [3]float64, [3]float64) {
	var m struct {
		Elements []struct {
			From, To [3]float64
		} `json:"elements"`
	}
	_ = json.Unmarshal(files[file], &m)
	lo := [3]float64{1e9, 1e9, 1e9}
	hi := [3]float64{-1e9, -1e9, -1e9}
	for _, e := range m.Elements {
		for a := 0; a < 3; a++ {
			lo[a] = min(lo[a], e.From[a], e.To[a])
			hi[a] = max(hi[a], e.From[a], e.To[a])
		}
	}
	return len(m.Elements), lo, hi
}

func sz(lo, hi [3]float64) string {
	return fmt.Sprintf("%.1f x %.1f x %.1f", hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2])
}

func TestRealEntityParts(t *testing.T) {
	rp, bp := readReal(t, "PACKCONV_PARTS_RP"), readReal(t, "PACKCONV_PARTS_BP")
	res, err := Convert(&Input{Packs: []*Source{rp}, BehaviorPacks: []*Source{bp}, Slots: Candidates()})
	if err != nil {
		t.Fatal(err)
	}
	ours := checkValid(t, res)
	var cmp strings.Builder
	defer func() { writeOut(t, "parts", res, cmp.String()) }()
	var id string
	for k := range res.Entities {
		if strings.HasSuffix(k, os.Getenv("PACKCONV_PARTS_ENTITY")) {
			id = k
		}
	}
	e, ok := res.Entities[id]
	if !ok {
		t.Fatalf("no entity %q: %v", os.Getenv("PACKCONV_PARTS_ENTITY"), res.Stats.Missing)
	}
	zb, err := os.ReadFile(realPath(t, "PACKCONV_PARTS_JAVA"))
	if err != nil {
		t.Skip()
	}
	theirs := unzip(t, zb)
	pattern := os.Getenv("PACKCONV_PARTS_PATTERN")
	cmp.WriteString("# " + id + ": converted parts vs hand-made parts\n\n| bone | elements here | size here (px) | offset here (blocks) | hand-made elements | hand-made size (px) |\n|---|---|---|---|---|---|\n")
	for _, p := range e.Parts {
		n, lo, hi := elementStats(ours, "assets/"+res.Mapping.Namespace+"/models/"+strings.TrimPrefix(p.Model, res.Mapping.Namespace+":")+".json")
		hn, hlo, hhi := elementStats(theirs, fmt.Sprintf(pattern, strings.ToLower(p.Bone)))
		fmt.Fprintf(&cmp, "| %s | %d | %s | %v | %d | %s |\n", p.Bone, n, sz(lo, hi), p.Offset, hn, sz(hlo, hhi))
		if hn != 0 && hn != n {
			t.Errorf("bone %s: %d elements, hand-made %d", p.Bone, n, hn)
		}
	}
}

func TestRealWholeEntities(t *testing.T) {
	rp := readReal(t, "PACKCONV_WHOLE_RP")
	res, err := Convert(&Input{Packs: []*Source{rp}, Slots: Candidates()})
	if err != nil {
		t.Fatal(err)
	}
	ours := checkValid(t, res)
	var cmp strings.Builder
	defer func() { writeOut(t, "whole", res, cmp.String()) }()
	zb, err := os.ReadFile(realPath(t, "PACKCONV_WHOLE_JAVA"))
	if err != nil {
		t.Skip()
	}
	theirs := unzip(t, zb)
	pattern := os.Getenv("PACKCONV_WHOLE_PATTERN")
	cmp.WriteString("# Entities: converted whole models vs hand-made ones\n\nConverted models are at full size, shrunk only when they exceed the -16..32 box (the scale is in brackets).\n\n| entity | elements here | size here (px) | hand-made elements | hand-made size (px) |\n|---|---|---|---|---|\n")
	for _, id := range sortedKeys(res.Entities) {
		e := res.Entities[id]
		n, lo, hi := elementStats(ours, "assets/"+res.Mapping.Namespace+"/models/"+strings.TrimPrefix(e.Model, res.Mapping.Namespace+":")+".json")
		_, name, _ := strings.Cut(id, ":")
		file := fmt.Sprintf(pattern, name)
		hn, hlo, hhi := elementStats(theirs, file)
		if hn == 0 { // a variant whose parent has the elements
			var m struct {
				Parent string `json:"parent"`
			}
			_ = json.Unmarshal(theirs[file], &m)
			if m.Parent != "" {
				ns, p, _ := strings.Cut(m.Parent, ":")
				hn, hlo, hhi = elementStats(theirs, "assets/"+ns+"/models/"+p+".json")
			}
		}
		fmt.Fprintf(&cmp, "| %s | %d | %s (x%.2f) | %d | %s |\n", id, n, sz(lo, hi), e.Scale, hn, sz(hlo, hhi))
		if hn != 0 && hn != n {
			t.Errorf("%s: %d elements, hand-made %d", id, n, hn)
		}
	}
}

func TestRealModels(t *testing.T) {
	rp := readReal(t, "PACKCONV_MODELS_RP")
	res, err := Convert(&Input{Packs: []*Source{rp}, Slots: Candidates()})
	if err != nil {
		t.Fatal(err)
	}
	ours := checkValid(t, res)
	var cmp strings.Builder
	defer func() { writeOut(t, "models", res, cmp.String()) }()
	zb, err := os.ReadFile(realPath(t, "PACKCONV_MODELS_JAVA"))
	if err != nil {
		t.Skip()
	}
	theirs := unzip(t, zb)
	cmp.WriteString("# Converted vs hand-made models\n\n| model | elements | size (px) |\n|---|---|---|\n")
	var names []string
	for n := range ours {
		if strings.Contains(n, "/models/") {
			names = append(names, n)
		}
	}
	for n := range theirs {
		if strings.Contains(n, "/models/") {
			names = append(names, "ref:"+n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		files := ours
		if strings.HasPrefix(n, "ref:") {
			files = theirs
		}
		c, lo, hi := elementStats(files, strings.TrimPrefix(n, "ref:"))
		fmt.Fprintf(&cmp, "| %s | %d | %s |\n", n, c, sz(lo, hi))
	}
}

func TestRealUIPack(t *testing.T) {
	rp := readReal(t, "PACKCONV_UI_RP")
	res, err := Convert(&Input{Packs: []*Source{rp}, Slots: Candidates()})
	if err != nil {
		t.Fatal(err)
	}
	checkValid(t, res)
	writeOut(t, "ui", res, "")
	if res.Report.Count(catOther, Skipped) == 0 {
		t.Error("JSON UI not reported")
	}
}
