package javapack

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// HUD markers. A Bedrock pack with a HUD of its own (ui/hud_screen.json and the files it pulls in)
// takes its data from the title, subtitle or action bar: the server sends a title that starts
// with or holds a marker ("update:", "sb:"), the pack never shows that title and draws its own
// controls from it instead, often keeping the last one ("title preserve"). On Java such a title
// would flash on screen as text, so the bridge does not show titles that hold a marker; the ones
// whose text the pack writes on screen (a sidebar made of a title) it shows as a Java sidebar.
//
// Packs test for their markers in binding expressions, written in many ways:
//
//	(#hud_title_text_string - 'cbm:party')                     the marker as a literal
//	('%.7s' * #hud_title_text_string) = 'update:'               a prefix of the title
//	(#hud_title_text_string - $update_string)                   the marker in a variable
//	                                                            ("$update_string": "update:")
//	(#preserved_text - 'sb:')                                   a copy of the title: a binding
//	                                                            with binding_name_override, or a
//	                                                            view binding from the title
//
// So a ui file's names for the HUD strings are the strings themselves and every name a binding
// gives them in that file (followed to names of names); the markers are the literals and the
// variables' values in any expression of that file that reads one of those names.
//
// A marker is a panel marker when the pack writes the text it cut the marker from: a view
// binding whose source is the title minus the marker, not compared to anything, and whose target
// is not a switch (#visible, #enabled, #alpha).

var (
	hudStrings = []string{"#hud_title_text_string", "#hud_subtitle_text_string", "#hud_actionbar_text_string"}
	jsonObject = regexp.MustCompile(`\{[^{}]*\}`)
	jsonString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	uiVariable = regexp.MustCompile(`"(\$[\w.]+)(?:\|default)?"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	// A text an expression tests the title with: cut from it or compared to something.
	uiTested = regexp.MustCompile(`[-=]\s*(?:'([^']*)'|(\$[\w.]+))|(?:'([^']*)'|(\$[\w.]+))\s*=`)
	// The whole of an expression that is a name less a text: what a panel writes.
	uiCut       = regexp.MustCompile(`^(#[\w.]+)\s*-\s*(?:'([^']*)'|(\$[\w.]+))$`)
	bindingName = regexp.MustCompile(`"binding_name"\s*:\s*"(#[\w.]+)"`)
	bindingOver = regexp.MustCompile(`"binding_name_override"\s*:\s*"(#[\w.]+)"`)
	viewSource  = regexp.MustCompile(`"source_property_name"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	viewTarget  = regexp.MustCompile(`"target_property_name"\s*:\s*"(#[\w.]+)"`)
	colourCode  = regexp.MustCompile(`§.`)
)

// switchTargets are view binding targets that switch a control rather than give it text.
var switchTargets = map[string]bool{"#visible": true, "#enabled": true, "#alpha": true, "#propagate_alpha": true}

// HudOf lists the HUD markers of the packs' ui files and, of those, the panel markers, sorted.
func HudOf(packs []*Source) (markers, panels []string) {
	vars := map[string][]string{}
	type file struct{ data []byte }
	var files []file
	for _, s := range packs {
		for name, b := range s.files {
			if !strings.HasPrefix(name, "ui/") || !strings.HasSuffix(name, ".json") {
				continue
			}
			for _, m := range uiVariable.FindAllSubmatch(b, -1) {
				vars[string(m[1])] = append(vars[string(m[1])], string(m[2]))
			}
			files = append(files, file{b})
		}
	}
	seen, shown := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		names := hudNames(f.data)
		if len(names) == 0 {
			continue
		}
		reads := func(expr string) bool {
			for n := range names {
				if strings.Contains(expr, n) {
					return true
				}
			}
			return false
		}
		values := func(lit, ref string) []string {
			if ref != "" {
				return vars[ref]
			}
			return []string{lit}
		}
		for _, m := range jsonString.FindAllSubmatch(f.data, -1) {
			expr := string(m[1])
			if !reads(expr) || !strings.ContainsAny(expr, "-=*") {
				continue
			}
			// Only texts the title is tested with: a literal joined to it ('textures/x_' + ...)
			// is not a marker.
			for _, t := range uiTested.FindAllStringSubmatch(expr, -1) {
				for _, v := range append(values(t[1], t[2]), values(t[3], t[4])...) {
					if hudMarkerText(v) {
						seen[v] = true
					}
				}
			}
		}
		for _, o := range jsonObject.FindAll(f.data, -1) {
			src, tgt := viewSource.FindSubmatch(o), viewTarget.FindSubmatch(o)
			if src == nil || tgt == nil || switchTargets[string(tgt[1])] {
				continue
			}
			// The panel's text is the whole title less the marker; a control that shows a part
			// of it (fixed-width fields cut with '%.Ns') is not a panel of text.
			c := uiCut.FindStringSubmatch(strings.Trim(string(src[1]), "() "))
			if c == nil || !names[c[1]] {
				continue
			}
			for _, v := range values(c[2], c[3]) {
				if hudMarkerText(v) {
					shown[v] = true
				}
			}
		}
	}
	for m := range seen {
		markers = append(markers, m)
	}
	for m := range shown {
		if seen[m] {
			panels = append(panels, m)
		}
	}
	sort.Strings(markers)
	sort.Strings(panels)
	return markers, panels
}

// HudMarkersOf lists the HUD markers of the packs' ui files, sorted.
func HudMarkersOf(packs []*Source) []string {
	m, _ := HudOf(packs)
	return m
}

// hudNames are the names a ui file has for the HUD strings: the strings and what its bindings
// call them.
func hudNames(data []byte) map[string]bool {
	names := map[string]bool{}
	for _, s := range hudStrings {
		if strings.Contains(string(data), s) {
			names[s] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	objects := jsonObject.FindAll(data, -1)
	for changed := true; changed; {
		changed = false
		for _, o := range objects {
			var from, to string
			if n, over := bindingName.FindSubmatch(o), bindingOver.FindSubmatch(o); n != nil && over != nil {
				from, to = string(n[1]), string(over[1])
			} else if src, tgt := viewSource.FindSubmatch(o), viewTarget.FindSubmatch(o); src != nil && tgt != nil {
				from, to = strings.TrimSpace(string(src[1])), string(tgt[1])
			}
			if names[from] && !names[to] && !switchTargets[to] {
				names[to], changed = true, true
			}
		}
	}
	return names
}

// hudMarkerText reports whether a literal of an expression can be a marker: three bytes or
// more with a letter or digit, not a format ('%.6s') or only colour codes.
func hudMarkerText(s string) bool {
	if len(s) < 3 || strings.HasPrefix(s, "%") {
		return false
	}
	return strings.ContainsFunc(colourCode.ReplaceAllString(s, ""), func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	})
}
