package bridge

import (
	"reflect"
	"testing"
)

func TestPackedText(t *testing.T) {
	for _, c := range []struct {
		in      string
		runs    []string
		texture string
	}{
		{"uhzfJXv3tR:5050:5175:431:258:textures/ui/custom_forms/blue/form_inner_border", nil, "textures/ui/custom_forms/blue/form_inner_border"},
		{"lTC2UbQ1Bd5487:5305:100|040:054:055:062:§7No rank yet:default", []string{"§7No rank yet"}, ""},
		{"l8K17XCm6e5443:5425:150|040:051:052:064:OSUEA(2):MinecraftTen", []string{"OSUEA(2)"}, ""},
		{"aBc12dEf:207:327:100:default::textures/ui/custom_forms/x:polls_hover:§ePolls\nVote on what's next",
			[]string{"§ePolls\nVote on what's next"}, "textures/ui/custom_forms/x"},
		{"dynamic:440:280:440:011:500|100:113:Abyss Menu:textures/ui/custom_forms/blue/bg", []string{"Abyss Menu"}, "textures/ui/custom_forms/blue/bg"},
		{"x1:5:6:7:Balance: $775K", []string{"Balance: $775K"}, ""},
	} {
		if !looksPacked(c.in) {
			t.Errorf("%q does not look packed", c.in)
		}
		runs, tex := packedText(c.in)
		if !reflect.DeepEqual(runs, c.runs) || tex != c.texture {
			t.Errorf("%q: %q %q, want %q %q", c.in, runs, tex, c.runs, c.texture)
		}
	}
	for _, s := range []string{"Shop", "§aShop\n§7Click to open", "Time: 10:30", "Kanto 1", "a:b:c:d"} {
		if looksPacked(s) {
			t.Errorf("%q looks packed", s)
		}
		if readableText(s) != s {
			t.Errorf("%q changed", s)
		}
	}
}

func TestButtonLines(t *testing.T) {
	for in, want := range map[string][2]string{"Shop": {"Shop", ""}, "§ePolls\nVote on what's next": {"§ePolls", "Vote on what's next"},
		"a\r\nb\nc": {"a", "b\nc"}} {
		if f, r := buttonLines(in); f != want[0] || r != want[1] {
			t.Errorf("%q: %q %q, want %q", in, f, r, want)
		}
	}
}

func TestPanelLines(t *testing.T) {
	got := panelLines("\n§l§bMY SERVER\n\nMoney: §a$5\nq1:5:6:7:textures/ui/bar\nid9Xa2:1:2:3:§eQuest one\n\n")
	want := []string{"§l§bMY SERVER", "", "Money: §a$5", "§eQuest one"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}
