package bridge

import "testing"

func TestNetherNetURL(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:29532":        "http://127.0.0.1:29532",
		"[::1]:19132":            "http://[::1]:19132",
		"127.0.0.1":              "http://127.0.0.1:19132",
		"http://127.0.0.1:19134": "http://127.0.0.1:19134",
	} {
		if got := netherNetURL(in); got != want {
			t.Errorf("netherNetURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := loginServerAddress("nethernet:127.0.0.1:29532"); got != "127.0.0.1:29532" {
		t.Errorf("loginServerAddress(nethernet) = %q", got)
	}
}
