package service

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestPortMux: HTTP requests reach the handler and everything else the Minecraft listener, with
// the first bytes intact, on one port.
func TestPortMux(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := newPortMux(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "pack "+r.URL.Path)
	}))
	defer mux.Close()
	got := make(chan string, 4)
	go func() {
		for {
			c, err := mux.Accept()
			if err != nil {
				return
			}
			b := make([]byte, 16)
			n, _ := c.Read(b)
			got <- string(b[:n])
			c.Close()
		}
	}()
	addr := ln.Addr().String()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(method, "http://"+addr+"/abc.zip", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if method == http.MethodGet && string(body) != "pack /abc.zip" {
			t.Fatalf("GET body %q", body)
		}
	}
	// A handshake (length 16, packet 0), a legacy ping, and a handshake 71 bytes long, whose first
	// byte is 'G'.
	for _, first := range []string{"\x10\x00\x89\x06", "\xfe\x01", "G\x00\x89\x06\x09"} {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte(first))
		select {
		case s := <-got:
			if s != first {
				t.Fatalf("minecraft side got %q, want %q", s, first)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("minecraft side got nothing for %q", first)
		}
		c.Close()
	}
}
