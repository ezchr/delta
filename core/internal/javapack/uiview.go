package javapack

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// FormButtonIn is a button of a form as the server sent it: its text and its picture's path.
type FormButtonIn struct{ Text, Image string }

// ShownButton is a button of a form screen that is there: the form's number of it (what the
// server wants back) and what to write on it.
type ShownButton struct {
	Index int
	Label string
}

// FormView is a form as its screen shows it, for a client that has no such layouts.
type FormView struct {
	Body    []string
	Buttons []ShownButton
	// Columns is how many buttons the screen puts side by side (1: a plain list) and Width a
	// button's width in pixels for them to fit, 0 for the client's own.
	Columns, Width int
}

// View lays a form out as the screen does: the labels that are text, every button that is there
// under the name the screen gives it, and the buttons in as many columns as the screen's fullest
// row of them.
//
// A button is named after what the layout writes on it; one of several with the same text, with
// nothing written on it, after the heading above it and its place there ("Kanto 2"); else by its
// own text. The one of such a group whose picture differs from all the others' (the frame of
// the chosen one) is marked "> Kanto 2 <". A heading that names buttons is not in the body.
func (s *FormScreen) View(d FormData, in []FormButtonIn) FormView {
	var v FormView
	same := map[string]int{}
	image := map[string]map[string]int{}
	for i, b := range in {
		if s.Hidden(i, b.Image, d) {
			continue
		}
		same[b.Text]++
		if image[b.Text] == nil {
			image[b.Text] = map[string]int{}
		}
		image[b.Text][b.Image]++
	}
	headers := map[int]bool{}
	for i, b := range in {
		if s.Hidden(i, b.Image, d) {
			continue
		}
		label := s.ButtonLabel(i, d)
		if label == "" && same[b.Text] > 1 {
			if label = s.ButtonHeader(i, d); label != "" {
				headers[s.button(i).header] = true
			}
		}
		if label == "" {
			label = ButtonWords(b.Text)
		}
		if same[b.Text] > 2 && len(image[b.Text]) == 2 && image[b.Text][b.Image] == 1 {
			label = "> " + label + " <"
		}
		v.Buttons = append(v.Buttons, ShownButton{Index: i, Label: label})
	}
	v.Body = s.body(d, headers)
	v.Columns, v.Width = s.columns(v.Buttons)
	return v
}

// ButtonWords makes a button's internal name readable: "arrow_up" -> "Arrow up".
func ButtonWords(t string) string {
	t = strings.TrimSpace(strings.NewReplacer("_", " ", ".", " ").Replace(t))
	if t == "" {
		return t
	}
	return strings.ToUpper(t[:1]) + t[1:]
}

// columns is the number of buttons in the screen's fullest row of the buttons shown, cut down
// to what fits beside each other at the width their longest label needs.
func (s *FormScreen) columns(shown []ShownButton) (cols, width int) {
	var ys []float64
	longest := 0
	for _, sb := range shown {
		if b := s.button(sb.Index); b != nil {
			ys = append(ys, b.Y)
		}
		if n := textLen(sb.Label); n > longest {
			longest = n
		}
	}
	sort.Float64s(ys)
	const sameRow = 3
	row, start := 0, 0.0
	for i, y := range ys {
		if i == 0 || y-start > sameRow {
			row, start = 0, y
		}
		row++
		cols = max(cols, row)
	}
	if cols <= 1 {
		return 1, 0
	}
	// A letter of the client's font is 6 pixels wide at most, a button's edges take 12, and 320
	// pixels of buttons fit a window at every GUI scale.
	width = min(max(longest*6+12, 44), 150)
	cols = min(cols, 320/(width+2))
	if cols <= 1 {
		return 1, 0
	}
	return cols, width
}

// textLen is the number of letters of t that show (formatting codes do not).
func textLen(t string) int {
	return utf8.RuneCountInString(t) - 2*strings.Count(t, "§")
}
