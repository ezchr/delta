package javapack

import (
	"reflect"
	"testing"
)

// A "title preserve" HUD: a sidebar drawn from titles that start with sb: (the marker in a
// variable, the text from a copy of the title) and data for other controls in titles that start
// with update: (only tested, never written).
const testHudUI = `{
  // comments are allowed in JSON UI
  "namespace": "hud",
  "custom_sidebar": {
    "type": "panel",
    "$update_string": "sb:",
    "controls": [
      {"data_control": {
        "type": "panel", "size": [0, 0],
        "bindings": [
          {"binding_name": "#hud_title_text_string"},
          {"binding_name": "#hud_title_text_string", "binding_name_override": "#preserved_text", "binding_condition": "visibility_changed"},
          {"binding_type": "view", "source_property_name": "(not (#hud_title_text_string = #preserved_text) and not ((#hud_title_text_string - $update_string) = #hud_title_text_string))", "target_property_name": "#visible"}
        ]
      }},
      {"label": {"type": "label", "text": "#text",
        "bindings": [{"binding_type": "view", "source_control_name": "data_control", "source_property_name": "(#preserved_text - $update_string)", "target_property_name": "#text"}]}}
    ]
  },
  "tutorial_bg": {
    "type": "image",
    "bindings": [
      {"binding_name": "#hud_title_text_string", "binding_name_override": "#tb"},
      {"binding_type": "view", "source_property_name": "(('%.7s' * #tb) = 'update:')", "target_property_name": "#visible"}
    ]
  },
  "hide_vanilla_title": {
    "bindings": [{"binding_type": "view", "source_property_name": "((#hud_title_text_string - '§a') = #hud_title_text_string)", "target_property_name": "#visible"}]
  },
  "unrelated": {
    "bindings": [{"binding_type": "view", "source_property_name": "(#form_text - 'nothing:')", "target_property_name": "#text"}]
  },
  "party_slot": {
    "bindings": [
      {"binding_name": "#hud_title_text_string", "binding_name_override": "#party"},
      {"binding_type": "view", "source_property_name": "(not ((#party - 'cbm:party') = #party))", "target_property_name": "#visible"},
      {"binding_type": "view", "source_property_name": "('textures/ui/party/slot_' + (('%.64s' * #party) - ('%.62s' * #party)))", "target_property_name": "#texture"},
      {"binding_type": "view", "source_property_name": "(('%.40s' * #party) - ('%.20s' * #party))", "target_property_name": "#text"}
    ]
  }
}`

func TestHudOf(t *testing.T) {
	src := &Source{files: map[string][]byte{"ui/hud_screen.json": []byte(testHudUI)}}
	markers, panels := HudOf([]*Source{src})
	// cbm:party is tested; the texture prefix joined to the title is not a marker, and the
	// party's fixed-width fields are not a panel of text.
	if want := []string{"cbm:party", "sb:", "update:"}; !reflect.DeepEqual(markers, want) {
		t.Errorf("markers = %q, want %q", markers, want)
	}
	if want := []string{"sb:"}; !reflect.DeepEqual(panels, want) {
		t.Errorf("panels = %q, want %q", panels, want)
	}
}

func TestHudMarkerText(t *testing.T) {
	for s, want := range map[string]bool{"sb:": true, "update:": true, "§a": false, "§l§r": false, "%.7s": false, "x": false, "::": false, "§lsb": true, "|d": false} {
		if got := hudMarkerText(s); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
}
