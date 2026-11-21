package bridge

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/ezchr/go-mcjava/wire"
)

// The "Abyss Menu" as its server sends it: panels, a text and the real buttons, each with its
// place written into its label. (The buttons' strings are made up after the one whose start
// was not known; the title, the panels and the text are the server's.)
const abyssTitle = "dynamic:440:280:440:011:500:489:506:515:000:000:500:500:000|100:113:114:159:160:196:197:200:201:208:Abyss Menu:textures/ui/custom_forms/blue/form"

var abyssButtons = []string{
	"uhzfJXv3tR:5050:5175:431:258:textures/ui/custom_forms/blue/form_inner_border",                                   // 0: a panel
	"aDif2XyYsN:5070:5195:427:254:textures/ui/custom_forms/blue/drift",                                               // 1: a panel
	"lTC2UbQ1Bd5487:5305:100|040:054:055:062:§7No rank yet:default",                                                  // 2: a text
	"kR8sPq0WnE:6400:5327:100:default::textures/ui/custom_forms/blue/shop:shop_hover:§aShop",                         // 3: row 1, middle
	"pQ7mZk2LxA:5207:5327:100:default::textures/ui/custom_forms/blue/polls:polls_hover:§ePolls\nVote on what's next", // 4: row 1, left
	"xW3nHt6VbC:7600:5330:120:default::default::§bWarps",                                                             // 5: row 1, right
	"gF5dJs9KoP:6410:5900:100:default::textures/ui/custom_forms/blue/quit:quit_hover:§cClose",                        // 6: row 2, under Shop
	"something the adapter has no rule for",                                                                          // 7
}

func abyssForm(t *testing.T) *bedrockForm {
	t.Helper()
	type btn struct {
		Text string `json:"text"`
	}
	form := struct {
		Type    string `json:"type"`
		Title   string `json:"title"`
		Content string `json:"content"`
		Buttons []btn  `json:"buttons"`
	}{Type: "form", Title: abyssTitle}
	for _, b := range abyssButtons {
		form.Buttons = append(form.Buttons, btn{b})
	}
	data, _ := json.Marshal(form)
	var bf bedrockForm
	if err := json.Unmarshal(data, &bf); err != nil {
		t.Fatal(err)
	}
	return &bf
}

// abyssRule is the adapter of docs/FORM_LAYOUTS.md's example.
func abyssRule(t *testing.T) *layoutRule {
	t.Helper()
	data, err := os.ReadFile("testdata/form_layouts_abyss.json")
	if err != nil {
		t.Fatal(err)
	}
	rules, errs := parseFormLayouts(data)
	if len(errs) > 0 || len(rules) != 1 {
		t.Fatal(errs, len(rules))
	}
	return rules[0]
}

// TestLayoutDecodeAbyss: the documented example's expressions read the server's strings.
func TestLayoutDecodeAbyss(t *testing.T) {
	r := abyssRule(t)
	near := func(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }

	e, ok := r.decode(abyssButtons[0], 1)
	if !ok || e.kind != layoutDecoration || !near(e.x, 5) || !near(e.y, 17.5) || e.w != 431 || e.h != 258 ||
		e.texture != "textures/ui/custom_forms/blue/form_inner_border" {
		t.Errorf("panel: %+v %v", e, ok)
	}
	e, ok = r.decode(abyssButtons[2], 1)
	if !ok || e.kind != layoutText || !near(e.x, 48.7) || !near(e.y, 30.5) || e.w != 100 || e.label != "§7No rank yet" {
		t.Errorf("text: %+v %v", e, ok)
	}
	e, ok = r.decode(abyssButtons[4], 0.5)
	if !ok || e.kind != layoutButton || !e.hasPos || !near(e.x, 10.35) || !near(e.y, 16.35) || e.w != 50 ||
		e.texture != "textures/ui/custom_forms/blue/polls" || e.label != "§ePolls\nVote on what's next" {
		t.Errorf("button: %+v %v", e, ok)
	}
	if _, ok = r.decode(abyssButtons[7], 1); ok {
		t.Error("a text no rule matches was decoded")
	}
}

// TestLayoutViewAbyss: the buttons are a grid by their places, the panels are gone, the text is
// the body, and each cell answers with the button's number in the server's form.
func TestLayoutViewAbyss(t *testing.T) {
	r := abyssRule(t)
	bf := abyssForm(t)
	v := r.view(bf)
	if v == nil {
		t.Fatal("the title did not match")
	}
	if v.title != "Abyss Menu" {
		t.Errorf("title %q", v.title)
	}
	if len(v.body) != 1 || v.body[0] != "§7No rank yet" {
		t.Errorf("body %q", v.body)
	}
	if v.columns != 3 {
		t.Fatalf("%d columns", v.columns)
	}
	// Row 1: Polls, Shop, Warps (by x, not the form's order). Row 2: Close, alone (a dialog
	// centres a last row that is not full; it has no empty places).
	want := []struct {
		index   int32
		label   string
		tooltip string
		texture string
	}{
		{4, "§ePolls", "Vote on what's next", "textures/ui/custom_forms/blue/polls"},
		{3, "§aShop", "", "textures/ui/custom_forms/blue/shop"},
		{5, "§bWarps", "", ""},
		{6, "§cClose", "", "textures/ui/custom_forms/blue/quit"},
	}
	if len(v.cells) != len(want) {
		t.Fatalf("cells %+v", v.cells)
	}
	for i, w := range want {
		c := v.cells[i]
		if c.index != w.index || c.label != w.label || c.tooltip != w.tooltip || c.texture != w.texture {
			t.Errorf("cell %d: %+v, want %+v", i, c, w)
		}
	}
	// The canvas (440) is wider than max_width (400): every size shrinks by 400/440.
	// Close is in the first column: as wide as Polls.
	if v.cells[0].width != 91 || v.cells[2].width != 109 || v.cells[3].width != 91 {
		t.Errorf("widths %d %d %d", v.cells[0].width, v.cells[2].width, v.cells[3].width)
	}

	// A click on a cell is the dialog's own answer with that number: the server's button index.
	for _, c := range v.cells {
		resp, err := formResponse(bf, c.index, nil)
		if c.spacer() {
			if resp != nil || err != nil {
				t.Errorf("an empty place answered %s %v", resp, err)
			}
			continue
		}
		var got int32
		if err != nil || json.Unmarshal(resp, &got) != nil || got != c.index {
			t.Errorf("button %d answered %s %v", c.index, resp, err)
		}
	}

	// The packet: a dialog with the labels, the tooltip and the picture's glyph where there is one.
	w := &wire.Writer{}
	writeLayoutDialog(w, v, 7, func(img formImage) (string, rune) {
		if img.Data == "textures/ui/custom_forms/blue/polls" {
			return "delta:icons", 0xE005
		}
		return "", 0
	})
	out := w.B
	for _, s := range []string{"minecraft:multi_action", "Abyss Menu", "No rank yet", "Polls", "Vote on what's next", "tooltip", "columns", "delta:icons", "Close"} {
		if !bytes.Contains(out, []byte(s)) {
			t.Errorf("the dialog has no %q", s)
		}
	}
	if bytes.Contains(out, []byte("form_inner_border")) || bytes.Contains(out, []byte("no rule for")) {
		t.Error("a dropped element is in the dialog")
	}
}

// TestLayoutOtherForms: a form with another title, or of another type, is not the adapter's, and
// what no rule matches stays a button unless the adapter drops it.
func TestLayoutOtherForms(t *testing.T) {
	r := abyssRule(t)
	bf := abyssForm(t)
	bf.Title = "Shop"
	if r.view(bf) != nil {
		t.Error("another title matched")
	}
	bf = abyssForm(t)
	bf.Type = "modal"
	if r.view(bf) != nil {
		t.Error("a modal matched")
	}
	keep := *r
	keep.cfg.Unmatched = ""
	v := keep.view(abyssForm(t))
	last := v.cells[len(v.cells)-1]
	if last.index != 7 || last.label != abyssButtons[7] {
		t.Errorf("the unmatched button: %+v", v.cells)
	}
}

// TestLayoutGrid: rows by y (overlapping heights and near tops are one row), columns by x, plain
// widths when nothing is sized and narrower ones when the row does not fit.
func TestLayoutGrid(t *testing.T) {
	b := func(i int32, x, y, w, h float64) layoutElem {
		return layoutElem{kind: layoutButton, index: i, x: x, y: y, w: w, h: h, hasPos: true, label: "b"}
	}
	cells, cols := layoutGrid([]layoutElem{b(0, 10, 100, 0, 0), b(1, 10, 10, 0, 0), b(2, 200, 12, 0, 0)}, 4, 400)
	if cols != 2 || len(cells) != 3 || cells[0].index != 1 || cells[1].index != 2 || cells[2].index != 0 || cells[0].width != 0 {
		t.Errorf("plain: %d %+v", cols, cells)
	}
	// Tops 15 apart but the second's middle is inside the first's height: one row.
	_, cols = layoutGrid([]layoutElem{b(0, 10, 10, 50, 40), b(1, 100, 25, 50, 20), b(2, 10, 60, 50, 40)}, 4, 400)
	if cols != 2 {
		t.Errorf("overlap: %d columns", cols)
	}
	// Four 200 pixel buttons in a row of 400: 100 each.
	cells, cols = layoutGrid([]layoutElem{b(0, 0, 0, 200, 20), b(1, 200, 0, 200, 20), b(2, 400, 0, 200, 20), b(3, 600, 0, 200, 20)}, 4, 400)
	if cols != 4 || cells[3].width != 100 {
		t.Errorf("shrunk: %d %+v", cols, cells)
	}
	// A row that is not full and a button without a place follow the full rows, in order.
	cells, cols = layoutGrid([]layoutElem{{kind: layoutButton, index: 0, label: "loose"}, b(1, 0, 0, 0, 0), b(2, 100, 0, 0, 0), b(3, 0, 50, 0, 0)}, 4, 400)
	if cols != 2 || len(cells) != 4 || cells[2].index != 3 || cells[3].index != 0 {
		t.Errorf("loose: %d %+v", cols, cells)
	}
}

// TestLayoutConfigErrors: a wrong adapter is named and left out, the others load.
func TestLayoutConfigErrors(t *testing.T) {
	rules, errs := parseFormLayouts([]byte(`{"layouts":[
		{"name":"bad","title":"("},
		{"name":"kind","title":"x","elements":[{"kind":"panel","match":"x"}]},
		{"name":"good","title":"^menu:"}]}`))
	if len(rules) != 1 || rules[0].cfg.Name != "good" || len(errs) != 2 {
		t.Errorf("%d rules, errors %v", len(rules), errs)
	}
	if _, errs = parseFormLayouts([]byte(`not json`)); len(errs) != 1 {
		t.Errorf("errors %v", errs)
	}
}

// TestLayoutLabelWidth: a column is wide enough for its longest label (with its picture).
func TestLayoutLabelWidth(t *testing.T) {
	b := func(i int32, x float64, label, tex string) layoutElem {
		return layoutElem{kind: layoutButton, index: i, x: x, y: 0, w: 100, hasPos: true, label: label, texture: tex}
	}
	cells, _ := layoutGrid([]layoutElem{b(0, 0, "§bRanks & Boosts", "textures/x"), b(1, 120, "§6Server Management\nmore", "textures/y"), b(2, 240, "§fTrade", "")}, 4, 400)
	if cells[0].width <= 100 || cells[1].width <= 120 || cells[2].width != 100 {
		t.Errorf("widths %d %d %d", cells[0].width, cells[1].width, cells[2].width)
	}
}
