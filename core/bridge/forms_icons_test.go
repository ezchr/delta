package bridge

import (
	"encoding/json"
	"testing"

	"delta/core/internal/javapack"
)

// TestMenuColumns: a plain menu is two columns only when it is long and every label is short.
func TestMenuColumns(t *testing.T) {
	menu := func(n int, label string, icon rune) []formButton {
		var out []formButton
		for i := 0; i < n; i++ {
			out = append(out, formButton{label, "f", icon})
		}
		return out
	}
	for _, c := range []struct {
		name    string
		buttons []formButton
		want    int32
	}{
		{"a few buttons", menu(7, "Sell", 0), 1},
		{"many short ones", menu(8, "Sell", 0), 2},
		{"colour codes do not count", menu(8, "§l§a1234567890123456789012", 0), 2},
		{"a long label", append(menu(8, "Sell", 0), formButton{label: "A label of more than 22 letters"}), 1},
		{"two lines", append(menu(8, "Sell", 0), formButton{label: "Sword\n§7120 coins"}), 1},
		{"fits without a picture only", menu(8, "12345678901234567890", 0xE000), 1},
		{"short beside a picture", menu(8, "1234567890123456789", 0xE000), 2},
	} {
		if got := menuColumns(c.buttons); got != c.want {
			t.Errorf("%s: %d columns, want %d", c.name, got, c.want)
		}
	}
}

// TestIconLabel: the picture's glyph is its own component in the icon font before the label,
// and a button without one keeps its label as it was.
func TestIconLabel(t *testing.T) {
	plain := labelText("§aShop")
	if got := iconLabel(plain, javapack.IconFont, 0); len(got.Extra) != len(plain.Extra) || got.Text != plain.Text || got.Font != "" {
		t.Errorf("no icon: %+v", got)
	}
	got := iconLabel(plain, javapack.IconFont, 0xE123)
	if len(got.Extra) != 3 || got.Extra[0].Text != "" || got.Extra[0].Font != javapack.IconFont ||
		got.Extra[0].Color != "white" || got.Extra[1].Text != " " || got.Extra[1].Font != "" {
		t.Errorf("icon: %+v", got)
	}
}

// TestFormImage: a button's image as servers send it.
func TestFormImage(t *testing.T) {
	var f bedrockForm
	err := json.Unmarshal([]byte(`{"type":"form","title":"t","content":"","buttons":[
		{"text":"a","image":{"type":"path","data":"textures/items/diamond"}},
		{"text":"b","image":{"type":"url","data":"https://example.com/a.png"}},
		{"text":"c"},{"text":"d","image":null}]}`), &f)
	if err != nil || len(f.Buttons) != 4 {
		t.Fatal(err, len(f.Buttons))
	}
	if f.Buttons[0].Image.Data != "textures/items/diamond" || f.Buttons[1].Image.Type != "url" || f.Buttons[2].Image.Data != "" || f.Buttons[3].Image.Data != "" {
		t.Errorf("%+v", f.Buttons)
	}
}
