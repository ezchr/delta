package javapack

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Bedrock packs can redraw a server form as a screen of their own (ui/server_form.json): the form's
// title names the screen and its body is packed data, fixed-width fields the layout cuts apart
// with expressions such as (('%.16s' * #form_text) - ('%.0s' * #form_text)), some of them text
// for labels and some the names of textures. Java has no such layouts, and shown as it is the
// form is a block of codes. A FormScreen is what a layout says about one screen, enough to show
// the form as plain text: its labels with the expressions that fill and show them, and which
// label is drawn on which button.

// FormScreen is one screen a pack draws for forms whose title holds Marker.
type FormScreen struct {
	Marker  string
	Title   string // the screen's heading ("" if the layout has none)
	Labels  []FormLabel
	Buttons []FormButton
	// HiddenTexture: a button whose image path ends with it is not there (the layout switches
	// it off); "" if the layout hides none.
	HiddenTexture string
}

// FormLabel is a label of a screen: Text if it is fixed, else the value of Expr (a binding
// expression over the form's title and body). It shows when every expression in Visible is
// true; one that cannot be evaluated counts as true.
type FormLabel struct {
	Name     string
	X, Y     float64
	Text     string
	Expr     string
	Visible  []string
	onButton bool
	depth    int // how deep in the screen's controls it is (1: the screen's own)
}

// FormButton is the place of the form's button number Index on the screen.
type FormButton struct {
	Index      int
	X, Y, W, H float64
	Visible    []string
	label      int // index in Labels of the label drawn on it, -1 if none
	header     int // index in Labels of the heading above it, -1 if none
	ordinal    int // its number among the buttons under that heading, from 1
}

var (
	formTitleMarker = regexp.MustCompile(`#title_text\s*-\s*'([^']{2,64})'`)
	formHidden      = regexp.MustCompile(`#form_button_texture\s*-\s*'([^']{1,64})'`)
)

// FormScreensOf reads the form screens of the packs' ui/server_form.json files.
func FormScreensOf(packs []*Source) []FormScreen {
	var out []FormScreen
	seen := map[string]bool{}
	for i := len(packs) - 1; i >= 0; i-- { // the top pack's layout wins
		b, ok := packs[i].files["ui/server_form.json"]
		if !ok {
			continue
		}
		var defs map[string]json.RawMessage
		if json.Unmarshal(stripJSON(b), &defs) != nil {
			continue
		}
		ui := &formUI{defs: map[string]map[string]any{}}
		for name, raw := range defs {
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				ui.defs[strings.SplitN(name, "@", 2)[0]] = m
			}
		}
		names := make([]string, 0, len(ui.defs))
		for name := range ui.defs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			def := ui.defs[name]
			marker := ""
			for _, bd := range bindingsOf(def) {
				if m := formTitleMarker.FindStringSubmatch(bd.source); m != nil && bd.target == "#visible" {
					marker = m[1]
				}
			}
			if marker == "" || seen[marker] {
				continue
			}
			seen[marker] = true
			s := FormScreen{Marker: marker}
			ui.walk(&s, name, def, 0, 0, 0, nil)
			s.finish()
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Marker < out[j].Marker })
	return out
}

type formUI struct {
	defs map[string]map[string]any
}

type uiBinding struct{ source, target, name string }

func bindingsOf(node map[string]any) []uiBinding {
	list, _ := node["bindings"].([]any)
	out := make([]uiBinding, 0, len(list))
	for _, v := range list {
		m, _ := v.(map[string]any)
		if m == nil {
			continue
		}
		s, _ := m["source_property_name"].(string)
		t, _ := m["target_property_name"].(string)
		n, _ := m["binding_name"].(string)
		out = append(out, uiBinding{s, t, n})
	}
	return out
}

func pair(v any) (float64, float64, bool) {
	a, _ := v.([]any)
	if len(a) != 2 {
		return 0, 0, false
	}
	x, ok1 := a[0].(float64)
	y, ok2 := a[1].(float64)
	return x, y, ok1 && ok2
}

// walk collects the labels and buttons of node, at ox, oy on the screen. vis are the conditions
// under which the controls above it show.
func (ui *formUI) walk(s *FormScreen, name string, node map[string]any, ox, oy float64, depth int, vis []string) {
	if depth > 16 || len(s.Labels) > 4000 {
		return
	}
	if x, y, ok := pair(node["offset"]); ok {
		ox, oy = ox+x, oy+y
	}
	typ, _ := node["type"].(string)
	binds := bindingsOf(node)
	for _, bd := range binds {
		if bd.target == "#visible" && bd.source != "" {
			vis = append(vis[:len(vis):len(vis)], bd.source)
		}
	}
	switch typ {
	case "label":
		txt, _ := node["text"].(string)
		l := FormLabel{Name: name, X: ox, Y: oy, depth: depth, Visible: vis}
		switch {
		case txt != "" && !strings.HasPrefix(txt, "#") && !strings.HasPrefix(txt, "$"):
			l.Text = txt
			s.Labels = append(s.Labels, l)
		case strings.HasPrefix(txt, "#"):
			for _, bd := range binds {
				if bd.target == txt && strings.Contains(bd.source, "#form_text") {
					l.Expr = bd.source
					s.Labels = append(s.Labels, l)
					break
				}
			}
		}
	case "button":
		if idx, ok := node["collection_index"].(float64); ok {
			w, h, _ := pair(node["size"])
			s.Buttons = append(s.Buttons, FormButton{Index: int(idx), X: ox, Y: oy, W: w, H: h, Visible: vis, label: -1, header: -1})
		}
		for _, bd := range binds {
			if m := formHidden.FindStringSubmatch(bd.source); m != nil && s.HiddenTexture == "" {
				s.HiddenTexture = m[1]
			}
		}
		return // what is inside a button is its own picture
	}
	controls, _ := node["controls"].([]any)
	for _, c := range controls {
		cm, _ := c.(map[string]any)
		for key, v := range cm {
			child, _ := v.(map[string]any)
			if child == nil {
				continue
			}
			cname := key
			if at := strings.IndexByte(key, '@'); at >= 0 {
				cname = key[:at]
				ref := key[at+1:]
				if dot := strings.LastIndexByte(ref, '.'); dot >= 0 {
					ref = ref[dot+1:]
				}
				if def := ui.defs[ref]; def != nil {
					merged := make(map[string]any, len(def)+len(child))
					for k, dv := range def {
						merged[k] = dv
					}
					for k, cv := range child {
						merged[k] = cv
					}
					child = merged
				}
			}
			ui.walk(s, cname, child, ox, oy, depth+1, vis)
		}
	}
}

// finish orders the labels as they are read, finds the heading and ties labels to buttons.
func (s *FormScreen) finish() {
	sort.SliceStable(s.Labels, func(i, j int) bool {
		a, b := s.Labels[i], s.Labels[j]
		if a.Y != b.Y {
			return a.Y < b.Y
		}
		return a.X < b.X
	})
	sort.SliceStable(s.Buttons, func(i, j int) bool { return s.Buttons[i].Index < s.Buttons[j].Index })
	for i := range s.Labels {
		// The screen's own heading, not that of a panel inside it.
		if l := &s.Labels[i]; l.Expr == "" && l.depth <= 1 && s.Title == "" && strings.EqualFold(l.Name, "title") {
			s.Title = l.Text
			l.onButton = true // not part of the body
		}
	}
	const margin = 3
	for bi := range s.Buttons {
		b := &s.Buttons[bi]
		for li := range s.Labels {
			l := &s.Labels[li]
			if l.onButton {
				continue
			}
			if l.X >= b.X-margin && l.X <= b.X+b.W+margin && l.Y >= b.Y-margin && l.Y <= b.Y+b.H+margin && b.W > 0 {
				b.label = li
				l.onButton = true
				break
			}
		}
	}
	// A button with nothing written on it is named after the heading above it: the nearest
	// label over it that is no button's own.
	count := map[int]int{}
	for bi := range s.Buttons {
		b := &s.Buttons[bi]
		if b.label >= 0 {
			continue
		}
		best, bestD := -1, 60.0
		for li, l := range s.Labels {
			if l.onButton || l.Y > b.Y || l.X > b.X+b.W || l.X < b.X-120 {
				continue
			}
			if d := b.Y - l.Y; d < bestD {
				best, bestD = li, d
			}
		}
		if best >= 0 {
			count[best]++
			b.header, b.ordinal = best, count[best]
		}
	}
}

// FormData is a form as its screen's expressions see it.
type FormData map[string]string

// NewFormData are the variables of a form with this title and body.
func NewFormData(title, body string) FormData {
	return FormData{"#title_text": title, "#form_text": body}
}

func shown(vis []string, d FormData) bool {
	for _, e := range vis {
		v, err := evalUI(e, d)
		if err == nil && !v.truth() {
			return false
		}
	}
	return true
}

// text is the label's text in the form, "" when it does not show.
func (l *FormLabel) text(d FormData) string {
	if !shown(l.Visible, d) {
		return ""
	}
	if l.Expr == "" {
		return l.Text
	}
	v, err := evalUI(l.Expr, d)
	if err != nil {
		return ""
	}
	return cleanFormText(v.str())
}

// cleanFormText trims a field: its padding, a cut-off multi-byte character, and formatting codes
// left alone at the end.
func cleanFormText(t string) string {
	t = strings.ToValidUTF8(t, "")
	t = strings.TrimFunc(t, unicode.IsSpace)
	for strings.HasSuffix(t, "§r") {
		t = strings.TrimFunc(strings.TrimSuffix(t, "§r"), unicode.IsSpace)
	}
	return t
}

func visibleText(t string) bool {
	skip := false
	for _, r := range t {
		if skip {
			skip = false
			continue
		}
		if r == '§' {
			skip = true
			continue
		}
		if !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// Body is the form's text as the screen shows it: the labels that are not written on buttons,
// row by row.
func (s *FormScreen) Body(d FormData) []string { return s.body(d, nil) }

// body is Body without the labels of skip (the headings that are written on buttons).
func (s *FormScreen) body(d FormData, skip map[int]bool) []string {
	var lines []string
	var row []string
	rowY := 0.0
	flush := func() {
		if len(row) > 0 {
			lines = append(lines, strings.Join(row, "   "))
			row = nil
		}
	}
	for i := range s.Labels {
		l := &s.Labels[i]
		if l.onButton || skip[i] {
			continue
		}
		t := l.text(d)
		if !visibleText(t) {
			continue
		}
		if len(row) > 0 && l.Y-rowY > 4 {
			flush()
		}
		if len(row) == 0 {
			rowY = l.Y
		}
		row = append(row, t)
	}
	flush()
	return lines
}

func (s *FormScreen) button(index int) *FormButton {
	for i := range s.Buttons {
		if s.Buttons[i].Index == index {
			return &s.Buttons[i]
		}
	}
	return nil
}

// ButtonLabel is what the screen writes on the form's button index, "" if the layout does not
// say (the button's own text is then the best there is).
func (s *FormScreen) ButtonLabel(index int, d FormData) string {
	if b := s.button(index); b != nil && b.label >= 0 {
		if t := s.Labels[b.label].text(d); visibleText(t) {
			return t
		}
	}
	return ""
}

// ButtonHeader names a button the screen writes nothing on after the heading above it and its
// number under that heading ("Kanto 2"), "" if there is none. It is for buttons that would
// otherwise all read the same.
func (s *FormScreen) ButtonHeader(index int, d FormData) string {
	if b := s.button(index); b != nil && b.header >= 0 {
		if t := s.Labels[b.header].text(d); visibleText(t) {
			return t + " " + strconv.Itoa(b.ordinal)
		}
	}
	return ""
}

// Hidden reports whether the form's button index, with this image, is not on the screen: the
// layout switches it off by its image or by a condition on the form.
func (s *FormScreen) Hidden(index int, image string, d FormData) bool {
	if s.HiddenTexture != "" && image != "" && (image == s.HiddenTexture || strings.HasSuffix(image, "/"+s.HiddenTexture)) {
		return true
	}
	if b := s.button(index); b != nil {
		return !shown(b.Visible, d)
	}
	return false
}

// PlainTitle is a title for the screen when its layout has no heading: the marker without its
// namespace, capitalised ("cbm:pokedex" -> "Pokedex").
func (s *FormScreen) PlainTitle() string {
	if s.Title != "" {
		return s.Title
	}
	t := s.Marker
	if i := strings.LastIndexAny(t, ":."); i >= 0 {
		t = t[i+1:]
	}
	t = strings.ReplaceAll(t, "_", " ")
	if t == "" {
		return s.Marker
	}
	return strings.ToUpper(t[:1]) + t[1:]
}
