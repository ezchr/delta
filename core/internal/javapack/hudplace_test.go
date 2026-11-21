package javapack

import (
	"reflect"
	"testing"
)

// A HUD with three boards made of titles: a sidebar at the right, a bar at the top and one at
// the bottom. The boards are definitions the root panel takes in through another panel; each
// text is a label with a corner of its own inside the board's frame.
const testHudPlaces = `{
  "namespace": "hud",
  // the screen's root takes the boards in
  "root_panel": {"modifications": [{"array_name": "controls", "operation": "insert_back",
    "value": [{"boards@hud.my_boards": {}}]}]},
  "my_boards": {"type": "panel", "size": ["100%", "100%"], "anchor_from": "top_left", "anchor_to": "top_left", "controls": [
    {"sidebar@hud.my_sidebar": {}}, {"topbar@hud.my_topbar": {}},
    {"bottom@hud.my_bottom": {"$where": "bottom_middle"}}, {"lost@hud.my_lost": {}}]},
  "my_sidebar": {"type": "image", "size": [140, 130], "anchor_from": "right_middle", "anchor_to": "right_middle", "controls": [
    {"sb_data": {"type": "panel", "size": [0, 0], "bindings": [
      {"binding_name": "#hud_title_text_string"},
      {"binding_name": "#hud_title_text_string", "binding_name_override": "#preserved_text"},
      {"binding_type": "view", "source_property_name": "(not ((#hud_title_text_string - 'sb:') = #hud_title_text_string))", "target_property_name": "#visible"}]}},
    {"sb_text": {"type": "label", "text": "#text", "size": [130, "default"], "anchor_from": "top_left", "anchor_to": "top_left",
      "bindings": [{"binding_type": "view", "source_control_name": "sb_data", "source_property_name": "(#preserved_text - 'sb:')", "target_property_name": "#text"}]}}
  ]},
  "my_topbar": {"type": "image", "size": [240, 14], "anchor_to": "top_middle", /* anchor_to only */ "controls": [
    {"tb_text": {"type": "label", "text": "#text", "anchor_from": "center", "anchor_to": "center",
      "bindings": [{"binding_name": "#hud_title_text_string", "binding_name_override": "#tb"},
        {"binding_type": "view", "source_property_name": "(#tb - 'tb:')", "target_property_name": "#text"}]}}
  ]},
  "my_bottom": {"type": "panel", "size": [200, 12], "$where|default": "center", "anchor_from": "$where", "anchor_to": "$where", "controls": [
    {"ab_text": {"type": "label", "text": "#text",
      "bindings": [{"binding_name": "#hud_title_text_string", "binding_name_override": "#ab"},
        {"binding_type": "view", "source_property_name": "(#ab - 'ab:')", "target_property_name": "#text"}]}},
  ]},
  "my_lost": {"type": "label", "text": "#text",
    "bindings": [{"binding_name": "#hud_title_text_string", "binding_name_override": "#lost"},
      {"binding_type": "view", "source_property_name": "(#lost - 'zz:')", "target_property_name": "#text"}]}
}`

func TestHudPanelsOf(t *testing.T) {
	src := &Source{files: map[string][]byte{"ui/hud_screen.json": []byte(testHudPlaces)}}
	got := HudPanelsOf([]*Source{src})
	want := []HudPanel{
		{"ab:", "bottom_middle", HudBottom}, // the anchor is a variable the taking control sets
		{"sb:", "right_middle", HudSide},    // the frame's anchor, not the label's top_left
		{"tb:", "top_middle", HudTop},
		{"zz:", "top_left", HudSide}, // only the full-size panel around it has an anchor
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("panels = %+v, want %+v", got, want)
	}
	// The pack of TestHudOf says nothing about where its sidebar is: a sidebar.
	src = &Source{files: map[string][]byte{"ui/hud_screen.json": []byte(testHudUI)}}
	if got := HudPanelsOf([]*Source{src}); !reflect.DeepEqual(got, []HudPanel{{"sb:", "", HudSide}}) {
		t.Errorf("no anchor: %+v", got)
	}
}

func TestHudPlaceOf(t *testing.T) {
	for a, want := range map[string]HudPlace{"top_middle": HudTop, "bottom_middle": HudBottom, "right_middle": HudSide,
		"left_middle": HudSide, "center": HudSide, "top_right": HudSide, "bottom_left": HudSide, "": HudSide} {
		if got := hudPlaceOf(a); got != want {
			t.Errorf("%q: %v, want %v", a, got, want)
		}
	}
}
