package javapack

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// HudSlots are the vanilla HUD texts a pack moves to a side of the screen as a whole, so that
// whatever the server sends there is a sidebar: a "sidebar" pack that redefines hud_title_text
// (the title) at right_middle shows every title as a box of lines at the right, with no marker
// at all (the server sends its sidebar with /titleraw ... title). The bridge shows such a text
// as the Java sidebar, for as long as Bedrock would show it.
type HudSlots struct {
	Title     bool // every title is a sidebar
	ActionBar bool // every action bar text is a sidebar
	// Strip are the marker texts the pack's moved controls cut out of the text ("_hud+:"): they
	// are not shown on Java either.
	Strip []string
}

// slotLiteral is a text an expression takes out of another ("($atitle - '_hud+:')").
var slotLiteral = regexp.MustCompile(`-\s*'([^'\\]{3,40})'`)

// HudSlotsOf finds the vanilla HUD texts the packs move to a side.
func HudSlotsOf(packs []*Source) HudSlots {
	var h HudSlots
	for _, s := range packs {
		b, ok := s.File("ui/hud_screen.json")
		if !ok {
			continue
		}
		var root map[string]any
		if json.Unmarshal(stripJSON(b), &root) != nil {
			continue
		}
		for key, v := range root {
			name, _, _ := strings.Cut(key, "@")
			obj, _ := v.(map[string]any)
			if obj == nil {
				continue
			}
			var slot *bool
			switch name {
			case "hud_title_text":
				slot = &h.Title
			case "hud_actionbar_text":
				slot = &h.ActionBar
			default:
				continue
			}
			anchor, _ := obj["anchor_from"].(string)
			if anchor == "" {
				anchor, _ = obj["anchor_to"].(string)
			}
			// At a side: the left or the right edge or a corner. Anywhere else (the vanilla
			// places, the middle) it is still a title or an action bar.
			if a := strings.ToLower(anchor); !strings.Contains(a, "left") && !strings.Contains(a, "right") {
				continue
			}
			*slot = true
			raw, _ := json.Marshal(obj)
			for _, m := range slotLiteral.FindAllStringSubmatch(string(raw), -1) {
				if lit := m[1]; strings.ContainsAny(lit, ":+|#") && !strings.Contains(lit, "%") && !slices.Contains(h.Strip, lit) {
					h.Strip = append(h.Strip, lit)
				}
			}
		}
	}
	return h
}
