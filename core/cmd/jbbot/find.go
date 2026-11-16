package main

import (
	"bytes"
	"flag"
	"log"
	"strings"
)

// -find "TEXT|TEXT...": every play packet that holds one of the texts is logged as
// `FOUND "TEXT" in packet 0x.. (N bytes) at OFFSET`. It is for what the bot does not decode, such
// as a book's content inside an item (the item package cannot read that component).
var findFlag = flag.String("find", "", "log the play packets that hold one of these texts (separated by |)")

func init() {
	var needles [][]byte
	ready := false
	packetHooks = append(packetHooks, func(b *bot, id int32, body []byte) {
		if !ready {
			ready = true
			for _, t := range strings.Split(*findFlag, "|") {
				if t != "" {
					needles = append(needles, []byte(t))
				}
			}
		}
		for _, n := range needles {
			if i := bytes.Index(body, n); i >= 0 {
				log.Printf("FOUND %q in packet %#x (%d bytes) at %d", n, id, len(body), i)
			}
		}
	})
}
