package javapack

import (
	"os"
	"strings"
	"testing"
)

const testFormUI = `{
 "namespace": "server_form",
 "my_item": {"type": "panel", "controls": [{"tile": {"type": "button", "size": [20, 10]}}]},
 "my_screen": {
  "type": "panel",
  "bindings": [{"binding_name": "#title_text"},
   {"binding_type": "view", "source_property_name": "(not ((#title_text - 'my:pick') = #title_text))", "target_property_name": "#visible"}],
  "controls": [
   {"title": {"type": "label", "text": "Pick One", "offset": [50, 2]}},
   {"icon": {"type": "image", "bindings": [{"binding_type": "view", "source_property_name": "('textures/ui/' + (('%.14s' * #form_text) - ('%.10s' * #form_text)))", "target_property_name": "#texture"}]}},
   {"name": {"type": "label", "text": "#value", "offset": [10, 20], "bindings": [{"binding_name": "#form_text"},
     {"binding_type": "view", "source_property_name": "(('%.10s' * #form_text) - ('%.0s' * #form_text))", "target_property_name": "#value"}]}},
   {"group": {"type": "label", "text": "#value", "offset": [100, 40], "bindings": [
     {"binding_type": "view", "source_property_name": "(('%.20s' * #form_text) - ('%.14s' * #form_text))", "target_property_name": "#value"}]}},
   {"desc": {"type": "label", "text": "#value", "offset": [10, 90], "bindings": [
     {"binding_type": "view", "source_property_name": "(#form_text - ('%.20s' * #form_text))", "target_property_name": "#value"}]}},
   {"go_l": {"type": "label", "text": "Go!", "offset": [15, 123]}},
   {"tab": {"type": "panel", "offset": [0, 60], "bindings": [
     {"binding_type": "view", "source_property_name": "((('%.1s' * #form_text) - ('%.0s' * #form_text)) = 'Z')", "target_property_name": "#visible"}],
     "controls": [{"other": {"type": "label", "text": "Other tab"}}]}},
   {"buttons": {"type": "collection_panel", "collection_name": "form_buttons", "controls": [
     {"b0": {"type": "button", "size": [20, 20], "offset": [100, 50], "collection_index": 0,
       "bindings": [{"binding_type": "view", "source_property_name": "((#form_button_texture - 'none') = #form_button_texture)", "target_property_name": "#focus_enabled"}]}},
     {"b1": {"type": "button", "size": [20, 20], "offset": [125, 50], "collection_index": 1}},
     {"b2": {"type": "button", "size": [60, 14], "offset": [10, 120], "collection_index": 2}}
   ]}}
  ]
 }
}`

// TestFormScreens: a layout's labels become the body, its codes for pictures do not, and buttons
// are named after what is written on them or the heading above them.
func TestFormScreens(t *testing.T) {
	src := &Source{files: map[string][]byte{"ui/server_form.json": []byte(testFormUI)}}
	screens := FormScreensOf([]*Source{src})
	if len(screens) != 1 || screens[0].Marker != "my:pick" {
		t.Fatalf("screens: %+v", screens)
	}
	s := &screens[0]
	body := "Bulbasaur " + "i001" + "Kanto " + "A seed on its back."
	if s.PlainTitle() != "Pick One" {
		t.Errorf("title %q", s.PlainTitle())
	}
	got := strings.Join(s.Body(NewFormData("my:pick", body)), "|")
	if got != "Bulbasaur|Kanto|A seed on its back." {
		t.Errorf("body %q", got)
	}
	d := NewFormData("my:pick", body)
	if l := s.ButtonLabel(2, d); l != "Go!" {
		t.Errorf("button 2 label %q", l)
	}
	if l := s.ButtonLabel(0, d); l != "" {
		t.Errorf("button 0 label %q", l)
	}
	if h0, h1 := s.ButtonHeader(0, d), s.ButtonHeader(1, d); h0 != "Kanto 1" || h1 != "Kanto 2" {
		t.Errorf("headers %q %q", h0, h1)
	}
	if !s.Hidden(0, "textures/ui/x/none", d) || s.Hidden(0, "textures/ui/x/slot", d) {
		t.Errorf("hidden texture %q", s.HiddenTexture)
	}
	// The tab that shows only for a body starting with Z.
	if got := strings.Join(s.Body(NewFormData("my:pick", "Zulbasaur "+body[10:])), "|"); !strings.Contains(got, "Other tab") {
		t.Errorf("body with the other tab %q", got)
	}
}

func TestUIExpr(t *testing.T) {
	vars := map[string]string{"#form_text": "abcdefgh", "#title_text": "cbm:party|d"}
	for expr, want := range map[string]string{
		"(('%.5s' * #form_text) - ('%.2s' * #form_text))":                                       "cde",
		"(#form_text - ('%.6s' * #form_text))":                                                  "gh",
		"('x/' + (('%.2s' * #form_text) - ('%.0s' * #form_text)))":                              "x/ab",
		"((('%.3s' * #form_text) - ('%.2s' * #form_text)) = 'c')":                               "true",
		"(not ((#title_text - 'cbm:party') = #title_text))":                                     "true",
		"((not ((#title_text - '|d') = #title_text)) and ((#title_text - '|t') = #title_text))": "true",
		"(not (not ((#title_text - '|t') = #title_text)))":                                      "true",
	} {
		v, err := evalUI(expr, vars)
		if err != nil || v.str() != want {
			t.Errorf("%s = %q, %v; want %q", expr, v.str(), err, want)
		}
	}
	if _, err := evalUI("(#form_button_text = '')", vars); err == nil {
		t.Error("an unknown variable must be an error")
	}
	for _, bad := range []string{"", "(", "('a' +", "'abc", "((#form_text))) x"} {
		_, _ = evalUI(bad, vars) // must not panic
	}
}

// TestRealFormScreens prints the screens of a real pack: JAVAPACK_UI=<unpacked pack>/ui/server_form.json.
func TestRealFormScreens(t *testing.T) {
	path := os.Getenv("JAVAPACK_UI")
	if path == "" {
		t.Skip("JAVAPACK_UI not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range FormScreensOf([]*Source{{files: map[string][]byte{"ui/server_form.json": b}}}) {
		dyn := 0
		for _, l := range s.Labels {
			if l.Expr != "" {
				dyn++
			}
		}
		t.Logf("%s title %q labels %d (from the body: %d) buttons %d hidden %q", s.Marker, s.PlainTitle(), len(s.Labels), dyn, len(s.Buttons), s.HiddenTexture)
	}
}
