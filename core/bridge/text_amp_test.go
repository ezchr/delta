package bridge

import "testing"

func TestAmpColours(t *testing.T) {
	for in, want := range map[string]string{
		"&l&6My Server\nMoney: &a500": "§l§6My Server\nMoney: §a500",
		"R&&D and salt && pepper":      "R&D and salt & pepper",
		"&gcoins &zno":                 "§gcoins &zno",
		"no codes":                     "no codes",
	} {
		if got := ampColours(in); got != want {
			t.Errorf("ampColours(%q) = %q, want %q", in, got, want)
		}
	}
}
