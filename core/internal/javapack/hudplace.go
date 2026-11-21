package javapack

import (
	"encoding/json"
	"sort"
	"strings"
)

// Where a pack draws a HUD panel (hud.go): the bridge shows a panel at the side as a sidebar, one
// at the top as boss bar lines and one at the bottom as the action bar.
//
// A panel's text is a label, mostly inside a picture (its background) inside the pack's own
// root panel, which the HUD screen takes in by reference ("x@hud.my_board": {}). Each control's
// anchor_from / anchor_to place it in its parent, so the place on the screen is the anchor of the
// outermost control that has one and does not fill its parent: the board's own frame, not the
// label's corner inside it. The controls around a definition are the ones around the control
// that takes it in, wherever that is.

// HudPlace is where on the screen a panel is.
type HudPlace int

const (
	// HudSide is the left or right edge, a corner or the middle, and a panel whose place was
	// not found: a sidebar.
	HudSide HudPlace = iota
	// HudTop is the middle of the top edge: lines above the game, as boss bars' names are.
	HudTop
	// HudBottom is the middle of the bottom edge: the action bar's place.
	HudBottom
)

func (p HudPlace) String() string {
	switch p {
	case HudTop:
		return "top"
	case HudBottom:
		return "bottom"
	}
	return "side"
}

// HudPanel is a panel marker and where the pack draws its text.
type HudPanel struct {
	Marker string
	// Anchor is the anchor found ("right_middle", "top_middle"...), "" when none was.
	Anchor string
	Place  HudPlace
}

// hudPlaceOf is the place of an anchor.
func hudPlaceOf(anchor string) HudPlace {
	switch strings.ToLower(strings.TrimSpace(anchor)) {
	case "top_middle":
		return HudTop
	case "bottom_middle":
		return HudBottom
	}
	return HudSide
}

// uiNode is a control (or any object) of a ui file; a chain of them is a control and the
// objects it is in, outermost first.
type uiNode struct {
	name string // its key ("sb_text", "sidebar@hud.delta_sidebar"; "" for an object of an array)
	obj  map[string]any
}

// HudPanelsOf is HudOf's panels with where each is drawn, sorted by marker.
func HudPanelsOf(packs []*Source) []HudPanel {
	_, panels := HudOf(packs)
	if len(panels) == 0 {
		return nil
	}
	isPanel := map[string]bool{}
	for _, m := range panels {
		isPanel[m] = true
	}
	vars := map[string][]string{}
	type file struct {
		name string
		data []byte
	}
	var files []file
	for _, s := range packs {
		for name, b := range s.files {
			if !strings.HasPrefix(name, "ui/") || !strings.HasSuffix(name, ".json") {
				continue
			}
			for _, m := range uiVariable.FindAllSubmatch(b, -1) {
				vars[string(m[1])] = append(vars[string(m[1])], string(m[2]))
			}
			files = append(files, file{name, b})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].name < files[j].name })

	// Every control that takes in a definition, by the definition's name, and the chain of
	// every control that writes a panel's text, by marker.
	refs := map[string][][]uiNode{}
	writers := map[string][][]uiNode{}
	for _, f := range files {
		var root map[string]any
		if json.Unmarshal(stripJSON(f.data), &root) != nil {
			continue
		}
		names := hudNames(f.data)
		var walk func(v any, chain []uiNode, depth int)
		walk = func(v any, chain []uiNode, depth int) {
			if depth > 64 {
				return
			}
			switch v := v.(type) {
			case []any:
				for _, e := range v {
					if obj, ok := e.(map[string]any); ok {
						walk(obj, append(chain[:len(chain):len(chain)], uiNode{"", obj}), depth+1)
					} else {
						walk(e, chain, depth+1)
					}
				}
			case map[string]any:
				for _, m := range hudWrites(v, names, vars) {
					if isPanel[m] {
						writers[m] = append(writers[m], chain)
					}
				}
				keys := make([]string, 0, len(v))
				for k := range v {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					obj, ok := v[k].(map[string]any)
					if !ok {
						walk(v[k], chain, depth+1)
						continue
					}
					sub := append(chain[:len(chain):len(chain)], uiNode{k, obj})
					if _, base, ok := strings.Cut(k, "@"); ok {
						base = base[strings.LastIndex(base, ".")+1:]
						refs[base] = append(refs[base], sub)
					}
					walk(obj, sub, depth+1)
				}
			}
		}
		walk(root, nil, 0)
	}

	out := make([]HudPanel, 0, len(panels))
	for _, m := range panels {
		hp := HudPanel{Marker: m}
		for _, chain := range writers[m] {
			if a := hudAnchor(hudFullChain(chain, refs, 0), vars); a != "" {
				hp.Anchor = a
				break
			}
		}
		hp.Place = hudPlaceOf(hp.Anchor)
		out = append(out, hp)
	}
	return out
}

// hudWrites lists the markers whose text the control obj writes: its view bindings from the
// title less a marker to a property that is not a switch.
func hudWrites(obj map[string]any, names map[string]bool, vars map[string][]string) []string {
	bindings, _ := obj["bindings"].([]any)
	var out []string
	for _, b := range bindings {
		bo, _ := b.(map[string]any)
		src, _ := bo["source_property_name"].(string)
		tgt, _ := bo["target_property_name"].(string)
		if src == "" || tgt == "" || switchTargets[tgt] {
			continue
		}
		c := uiCut.FindStringSubmatch(strings.Trim(src, "() "))
		if c == nil || !names[c[1]] {
			continue
		}
		if c[3] != "" {
			out = append(out, vars[c[3]]...)
		} else {
			out = append(out, c[2])
		}
	}
	return out
}

// hudFullChain is chain with, before it, the controls around the control that takes in the
// definition chain starts in (and so on outwards).
func hudFullChain(chain []uiNode, refs map[string][][]uiNode, depth int) []uiNode {
	if len(chain) == 0 || depth > 8 {
		return chain
	}
	top, _, _ := strings.Cut(chain[0].name, "@")
	for _, ref := range refs[top] {
		if len(ref) > 0 && ref[0].name == chain[0].name {
			continue // a definition that takes in itself
		}
		outer := hudFullChain(ref, refs, depth+1)
		return append(outer[:len(outer):len(outer)], chain...)
	}
	return chain
}

// hudAnchor is the anchor that places a chain of controls (outermost first) on the screen: the
// outermost control's that has one and a size of its own; without such a control the innermost
// anchor there is.
func hudAnchor(chain []uiNode, vars map[string][]string) string {
	value := func(i int, key string) string {
		s, _ := chain[i].obj[key].(string)
		if !strings.HasPrefix(s, "$") {
			return s
		}
		// A variable: set by the control or one around it (a default only when none sets it),
		// else anywhere in the pack.
		for _, k := range []string{s, s + "|default"} {
			for j := i; j >= 0; j-- {
				if v, ok := chain[j].obj[k].(string); ok {
					return v
				}
			}
		}
		if v := vars[s]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	anchor := func(i int) string {
		if a := value(i, "anchor_from"); a != "" {
			return a
		}
		return value(i, "anchor_to")
	}
	for i := range chain {
		if a := anchor(i); a != "" && hudOwnSize(chain[i].obj) {
			return a
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if a := anchor(i); a != "" {
			return a
		}
	}
	return ""
}

// hudOwnSize reports whether a control has a size that is not all of its parent, so that its
// anchor moves it.
func hudOwnSize(obj map[string]any) bool {
	size, ok := obj["size"].([]any)
	if !ok || len(size) != 2 {
		return false
	}
	full := func(v any) bool {
		s, ok := v.(string)
		return ok && strings.TrimSpace(s) == "100%"
	}
	return !(full(size[0]) && full(size[1]))
}
