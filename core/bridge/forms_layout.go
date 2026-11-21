package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

// Form layout adapters (optional, per server): docs/FORM_LAYOUTS.md.
//
// A Bedrock server whose resource pack redraws a form as a screen of its own (JSON UI) often
// packs the layout into the form's texts: the title carries the canvas, and every "button" is a
// panel, a text or a real button with its position, size and texture written into its label.
// Every server invents its own encoding, so the bridge cannot read it by itself. A server owner
// describes his in form_layouts.json: a regular expression that picks the form by its title and
// one per kind of element, with named groups for x, y, w, h, texture and label.
//
// A form that matches is still a Java dialog (forms.go), laid out as a grid from what was decoded:
//
//   - decorations are dropped;
//   - texts are the dialog's body, in reading order (top to bottom, left to right);
//   - the real buttons are put in rows by their y and ordered by their x; the dialog has as many
//     columns as the longest row, a shorter row's buttons go under the nearest ones of that row,
//     and each button is as wide as its own w (in GUI pixels) allows;
//   - a button's label is the first line of the decoded label, the other lines are its tooltip,
//     and its texture is its picture (the icon font, as any form button's image).
//
// A click answers with the button's number in the form as the server sent it, so the server sees
// what a Bedrock client would send. Without the file nothing here runs.

// formLayoutsFile is the adapters' file in the data directory ([core.options] form_layouts names
// another one, "off" none).
const formLayoutsFile = "form_layouts.json"

// formLayouts are the adapters of the running bridge (loadFormLayouts), in the file's order.
var formLayouts []*layoutRule

type layoutFile struct {
	Layouts []layoutConfig `json:"layouts"`
}

// layoutConfig is one adapter as the file has it.
type layoutConfig struct {
	Name string `json:"name"`
	// Title picks the form: a regular expression (Go's RE2 syntax) the form's title has to match.
	// Named groups: title (what the player reads as the title), width and height (the canvas).
	Title string `json:"title"`
	// Scale is GUI pixels for one pixel of the encoding (after Units). 0: 1, or what makes a
	// canvas wider than MaxWidth fit when the title gives its width.
	Scale float64 `json:"scale"`
	// Units turns the numbers of the groups x, y, w and h into pixels: (number + offset) * scale.
	Units map[string]layoutUnit `json:"units"`
	// RowTolerance: buttons whose tops are this many GUI pixels apart or less are one row, as
	// are buttons that overlap in height (0: 4).
	RowTolerance float64 `json:"row_tolerance"`
	// MaxWidth is the most GUI pixels a row of buttons takes (0: 400; at most 1024).
	MaxWidth int `json:"max_width"`
	// Unmatched is what becomes of an element no rule matches: "button" (the default: it stays
	// a button with its text as it is) or "drop".
	Unmatched string `json:"unmatched"`
	// Content false leaves the form's own content text out of the body.
	Content  *bool              `json:"content"`
	Elements []layoutElemConfig `json:"elements"`
}

type layoutUnit struct {
	Offset float64 `json:"offset"`
	Scale  float64 `json:"scale"` // 0: 1
}

// layoutElemConfig is a rule for the form's elements: the first one whose Match matches an
// element's text says what it is.
type layoutElemConfig struct {
	// Kind is "decoration" (dropped), "text" (body) or "button".
	Kind string `json:"kind"`
	// Match is a regular expression with the named groups x, y, w, h, texture and label (each
	// optional).
	Match string `json:"match"`
	// Units replaces the adapter's for this rule's groups.
	Units map[string]layoutUnit `json:"units"`
	// Defaults are the values of groups the expression has not or that matched nothing.
	Defaults map[string]string `json:"defaults"`
}

const (
	layoutDecoration = "decoration"
	layoutText       = "text"
	layoutButton     = "button"
)

// layoutRule is a compiled adapter.
type layoutRule struct {
	cfg   layoutConfig
	title *regexp.Regexp
	elems []layoutElemRule
}

type layoutElemRule struct {
	kind     string
	re       *regexp.Regexp
	units    map[string]layoutUnit
	defaults map[string]string
}

// parseFormLayouts compiles an adapters file. Adapters that are wrong are left out and named
// in errs; the others still work.
func parseFormLayouts(data []byte) (rules []*layoutRule, errs []error) {
	var f layoutFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, []error{err}
	}
	for i, c := range f.Layouts {
		name := c.Name
		if name == "" {
			name = "#" + strconv.Itoa(i+1)
		}
		r, err := compileLayout(c)
		if err != nil {
			errs = append(errs, fmt.Errorf("layout %s: %w", name, err))
			continue
		}
		rules = append(rules, r)
	}
	return rules, errs
}

func compileLayout(c layoutConfig) (*layoutRule, error) {
	if c.Title == "" {
		return nil, errors.New("no title expression")
	}
	re, err := regexp.Compile(c.Title)
	if err != nil {
		return nil, fmt.Errorf("title: %w", err)
	}
	switch c.Unmatched {
	case "", layoutButton, "drop":
	default:
		return nil, fmt.Errorf("unmatched is \"button\" or \"drop\", not %q", c.Unmatched)
	}
	r := &layoutRule{cfg: c, title: re}
	for i, e := range c.Elements {
		switch e.Kind {
		case layoutDecoration, layoutText, layoutButton:
		default:
			return nil, fmt.Errorf("element %d: kind is \"decoration\", \"text\" or \"button\", not %q", i+1, e.Kind)
		}
		ere, err := regexp.Compile(e.Match)
		if err != nil || e.Match == "" {
			return nil, fmt.Errorf("element %d: match: %v", i+1, err)
		}
		units := e.Units
		if units == nil {
			units = c.Units
		}
		r.elems = append(r.elems, layoutElemRule{e.Kind, ere, units, e.Defaults})
	}
	return r, nil
}

// loadFormLayouts reads the adapters file of the running bridge (Run). No file: no adapters.
func loadFormLayouts() {
	formLayouts = nil
	path := strings.TrimSpace(option("form_layouts", ""))
	if strings.EqualFold(path, "off") {
		return
	}
	if path == "" {
		if cfg.DataDir == "" {
			return
		}
		path = filepath.Join(cfg.DataDir, formLayoutsFile)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			cfg.Log.Warn("form layouts: file not read", "file", path, "err", err)
		}
		return
	}
	rules, errs := parseFormLayouts(data)
	for _, e := range errs {
		cfg.Log.Warn("form layouts: "+e.Error(), "file", path)
	}
	formLayouts = rules
	cfg.Log.Info("form layout adapters loaded", "file", path, "layouts", len(rules))
}

// groups are the named groups of a match that matched something.
func groups(re *regexp.Regexp, m []string) map[string]string {
	out := map[string]string{}
	for i, n := range re.SubexpNames() {
		if n != "" && i < len(m) && m[i] != "" {
			out[n] = m[i]
		}
	}
	return out
}

// layoutElem is a decoded element of a form.
type layoutElem struct {
	kind       string
	index      int32 // its number among the form's buttons
	x, y, w, h float64
	hasPos     bool // x and y were decoded
	label      string
	texture    string
	image      formImage // the element's own picture
}

// decode says what the element with this text is; false when no rule matches.
func (r *layoutRule) decode(s string, scale float64) (layoutElem, bool) {
	for _, er := range r.elems {
		m := er.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		g := groups(er.re, m)
		for k, v := range er.defaults {
			if _, ok := g[k]; !ok {
				g[k] = v
			}
		}
		num := func(key string) (float64, bool) {
			v, err := strconv.ParseFloat(strings.TrimSpace(g[key]), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return 0, false
			}
			u := er.units[key]
			if u.Scale == 0 {
				u.Scale = 1
			}
			return (v + u.Offset) * u.Scale * scale, true
		}
		e := layoutElem{kind: er.kind, label: g["label"], texture: g["texture"]}
		var okx, oky bool
		e.x, okx = num("x")
		e.y, oky = num("y")
		e.w, _ = num("w")
		e.h, _ = num("h")
		e.hasPos = okx && oky
		return e, true
	}
	return layoutElem{}, false
}

// layoutCell is a button of the dialog's grid.
type layoutCell struct {
	index   int32 // the button's number in the server's form; -2: an empty place (it closes)
	label   string
	tooltip string
	texture string    // decoded picture path ("": none)
	image   formImage // the form button's own picture, shown when texture gives none
	width   int32     // 0: the client's own
}

func (c *layoutCell) spacer() bool { return c.index < 0 }

// layoutView is a matched form as the dialog shows it.
type layoutView struct {
	title   string
	body    []string
	cells   []layoutCell // row after row, columns in each
	columns int32
}

const (
	layoutDefaultMaxWidth = 400
	layoutHardMaxWidth    = 1024 // a dialog button's widest
	layoutMinButton       = 24
	layoutPlainButton     = 150 // a dialog button without a width
)

func (r *layoutRule) maxWidth() float64 {
	m := r.cfg.MaxWidth
	if m <= 0 {
		m = layoutDefaultMaxWidth
	}
	return float64(min(m, layoutHardMaxWidth))
}

// view decodes a form; nil when its title is not this adapter's.
func (r *layoutRule) view(bf *bedrockForm) *layoutView {
	if bf.Type != "form" {
		return nil
	}
	m := r.title.FindStringSubmatch(string(bf.Title))
	if m == nil {
		return nil
	}
	tg := groups(r.title, m)
	v := &layoutView{title: string(bf.Title)}
	if _, ok := tg["title"]; ok || r.title.SubexpIndex("title") >= 0 {
		v.title = tg["title"]
	}
	scale := r.cfg.Scale
	if scale <= 0 {
		scale = 1
		// A canvas wider than the dialog may be: everything shrinks by the same amount.
		if cw, err := strconv.ParseFloat(tg["width"], 64); err == nil {
			u := r.cfg.Units["w"]
			if u.Scale == 0 {
				u.Scale = 1
			}
			if cw = (cw + u.Offset) * u.Scale; cw > r.maxWidth() {
				scale = r.maxWidth() / cw
			}
		}
	}

	if r.cfg.Content == nil || *r.cfg.Content {
		var content string
		_ = json.Unmarshal(bf.Content, &content)
		v.body = appendBody(v.body, content)
	}
	elems := bf.Buttons
	if len(elems) == 0 {
		elems = bf.Elements
	}
	var texts, buttons []layoutElem
	var labels []string // the form's own labels and headers, after the decoded texts
	index := int32(0)
	for _, fe := range elems {
		switch fe.Type {
		case "", "button":
		case "label", "header":
			labels = appendBody(labels, string(fe.Text))
			continue
		default:
			continue
		}
		e, ok := r.decode(string(fe.Text), scale)
		if !ok {
			e = layoutElem{kind: layoutButton, label: string(fe.Text)}
			if r.cfg.Unmatched == "drop" {
				e.kind = layoutDecoration
			}
		}
		e.index, e.image = index, fe.Image
		index++
		switch e.kind {
		case layoutText:
			if strings.TrimSpace(text.Strip(e.label)) != "" {
				texts = append(texts, e)
			}
		case layoutButton:
			buttons = append(buttons, e)
		}
	}
	tol := r.cfg.RowTolerance
	if tol <= 0 {
		tol = 4
	}
	v.body = append(v.body, layoutLines(texts, tol)...)
	v.body = append(v.body, labels...)
	v.cells, v.columns = layoutGrid(buttons, tol, r.maxWidth())
	return v
}

// layoutRows puts positioned elements in rows from the top, each row from the left: an element
// is in a row when its top is within tol of the row's first, or its middle is not below that
// one's bottom.
func layoutRows(elems []layoutElem, tol float64) [][]layoutElem {
	sorted := append([]layoutElem(nil), elems...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].y < sorted[j].y })
	var rows [][]layoutElem
	for _, e := range sorted {
		if n := len(rows); n > 0 {
			first := rows[n-1][0]
			if e.y-first.y <= tol || first.h > 0 && e.y+e.h/2 <= first.y+first.h {
				rows[n-1] = append(rows[n-1], e)
				continue
			}
		}
		rows = append(rows, []layoutElem{e})
	}
	for _, row := range rows {
		sort.SliceStable(row, func(i, j int) bool { return row[i].x < row[j].x })
	}
	return rows
}

// layoutLines are the texts as body paragraphs in reading order: the ones on a line side by
// side, then the ones without a position as the form has them.
func layoutLines(texts []layoutElem, tol float64) []string {
	var placed, loose []layoutElem
	for _, t := range texts {
		if t.hasPos {
			placed = append(placed, t)
		} else {
			loose = append(loose, t)
		}
	}
	var out []string
	for _, row := range layoutRows(placed, tol) {
		parts := make([]string, len(row))
		for i, t := range row {
			parts[i] = t.label
		}
		out = append(out, strings.Join(parts, "§r  "))
	}
	for _, t := range loose {
		out = append(out, t.label)
	}
	return out
}

// layoutGrid lays the buttons out: the cells row after row and the number of columns. Buttons
// with a position make the rows, ordered by x; the dialog has as many columns as the longest.
// A dialog fills its grid place after place and has nothing to leave a place empty with (a 1
// pixel button still draws its frame), but it centres a last row that is not full: so the full
// rows come first, in order, and the buttons of shorter rows and those without a position after
// them, in order, in rows as long as the others (the last one centred).
//
// Widths: a column is as wide as its widest button, decoded or as the label needs, all columns
// narrowed alike when they do not fit maxWidth.
func layoutGrid(buttons []layoutElem, tol, maxWidth float64) ([]layoutCell, int32) {
	var placed, loose []layoutElem
	for _, b := range buttons {
		if b.hasPos {
			placed = append(placed, b)
		} else {
			loose = append(loose, b)
		}
	}
	rows := layoutRows(placed, tol)
	columns := 1
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	var grid [][]layoutElem
	var rest []layoutElem
	for _, row := range rows {
		if len(row) == columns {
			grid = append(grid, row)
		} else {
			rest = append(rest, row...)
		}
	}
	rest = append(rest, loose...)
	for i := 0; i < len(rest); i += columns {
		grid = append(grid, rest[i:min(i+columns, len(rest))])
	}

	sized := false
	for _, b := range buttons {
		sized = sized || b.w > 0
	}
	width := func(e layoutElem) float64 {
		w := e.w
		if w <= 0 {
			w = layoutPlainButton
		}
		return max(w, labelWidth(e))
	}
	colWidth := make([]float64, columns)
	for _, line := range grid {
		for c, e := range line {
			colWidth[c] = max(colWidth[c], width(e))
		}
	}
	total := 0.0
	for _, w := range colWidth {
		total += w
	}
	shrink := 1.0
	if total > maxWidth {
		shrink, sized = maxWidth/total, true
	}
	var cells []layoutCell
	for _, line := range grid {
		for c, e := range line {
			label, tooltip, _ := strings.Cut(strings.ReplaceAll(e.label, "\r", ""), "\n")
			cell := layoutCell{index: e.index, label: label, tooltip: strings.TrimSpace(tooltip), image: e.image}
			if t := strings.TrimSpace(e.texture); t != "" && !strings.EqualFold(t, "default") {
				cell.texture = t
			}
			if sized {
				cell.width = int32(min(max(math.Round(colWidth[c]*shrink), layoutMinButton), layoutHardMaxWidth))
			}
			cells = append(cells, cell)
		}
	}
	return cells, int32(columns)
}

// labelWidth is about how many GUI pixels a button needs for its label's first line in the
// Java font, with a picture before it when it has one, and the button's frame.
func labelWidth(e layoutElem) float64 {
	first, _, _ := strings.Cut(e.label, "\n")
	w := 0.0
	bold := false
	skip := false
	for _, r := range first {
		switch {
		case skip:
			skip = false
			switch r {
			case 'l', 'L':
				bold = true
			case 'r', 'R':
				bold = false
			}
			continue
		case r == '\u00a7':
			skip = true
			continue
		}
		w += float64(glyphAdvance(r))
		if bold {
			w++
		}
	}
	if strings.TrimSpace(e.texture) != "" && !strings.EqualFold(strings.TrimSpace(e.texture), "default") || e.image.Data != "" {
		w += 22 // the picture (a 16 pixel glyph) and a space
	}
	return w + 12 // the frame
}

// glyphAdvance is a character's advance in Java's default font (most are 6).
func glyphAdvance(r rune) int {
	switch {
	case strings.ContainsRune("!',.:;i|", r):
		return 2
	case strings.ContainsRune("`l", r):
		return 3
	case strings.ContainsRune(" I[]t", r):
		return 4
	case strings.ContainsRune("\"()*<>fk{}", r):
		return 5
	case r >= 0x2E80:
		return 9 // wide scripts (unifont)
	}
	return 6
}

// showLayoutForm shows a form one of the adapters matches as a dialog laid out from what the
// form's texts encode; false: no adapter matches, the form is shown as any other. The form is
// in p.win.forms.open already, so a click is answered as any dialog's (formClicked). p.mu is held.
func (p *Player) showLayoutForm(formID uint32, bf *bedrockForm) bool {
	for _, r := range formLayouts {
		v := r.view(bf)
		if v == nil {
			continue
		}
		w := p.pk()
		writeLayoutDialog(w, v, int32(formID), p.formIcon)
		p.send(v777.ClientboundPlayShowDialog, w)
		p.log.Debug("form shown by a layout adapter", "id", formID, "layout", r.cfg.Name, "cells", len(v.cells), "columns", v.columns)
		return true
	}
	return false
}

// writeLayoutDialog writes the show_dialog packet of a view. icon gives a picture's glyph
// (Player.formIcon; nil: no pictures).
func writeLayoutDialog(w *wire.Writer, v *layoutView, id int32, icon func(formImage) (string, rune)) {
	w.VarInt(0) // a dialog given inline
	w.Byte(text.TagCompound)
	text.StringTag(w, "type", "minecraft:multi_action")
	title := bedrockText(v.title)
	text.ComponentTag(w, "title", &title)
	text.BoolTag(w, "pause", false)
	text.StringTag(w, "after_action", "close")
	writeBody(w, v.body)
	cells := v.cells
	if len(cells) == 0 {
		cells = []layoutCell{{}}
	}
	text.ListStart(w, "actions", text.TagCompound, len(cells))
	for i := range cells {
		c := &cells[i]
		label := labelText(c.label)
		if icon != nil && !c.spacer() {
			font, glyph := "", rune(0)
			if c.texture != "" {
				font, glyph = icon(formImage{Type: "path", Data: c.texture})
			}
			if glyph == 0 {
				font, glyph = icon(c.image)
			}
			label = iconLabel(label, font, glyph)
		}
		if c.tooltip != "" {
			tip := bedrockText(c.tooltip)
			text.ComponentTag(w, "tooltip", &tip)
		}
		writeButtonWidth(w, label, id, c.index, c.width)
	}
	text.IntTag(w, "columns", max(v.columns, 1))
	text.Key(w, text.TagCompound, "exit_action")
	writeButton(w, text.Translatable("gui.cancel"), id, -2)
	w.Byte(text.TagEnd)
}
