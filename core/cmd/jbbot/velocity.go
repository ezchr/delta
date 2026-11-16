package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"log"
	"os"
	"strings"

	"github.com/ezchr/go-mcjava/server"
	"github.com/ezchr/go-mcjava/wire"
)

// velocityAnswer answers a login custom query. With -velocity, the "velocity:player_info" query
// gets what a Velocity proxy with modern forwarding sends: an HMAC-SHA256 (the forwarding secret)
// of the version, the address, the offline profile of -name and no properties.
func velocityAnswer(body []byte, name, secretFile string) []byte {
	r := wire.NewReader(body)
	id := r.VarInt()
	channel := r.String(32767)
	w := &wire.Writer{}
	w.VarInt(id)
	if channel != "velocity:player_info" || secretFile == "" {
		w.Bool(false)
		return w.B
	}
	secret, err := os.ReadFile(secretFile)
	if err != nil {
		log.Printf("velocity secret: %v", err)
		w.Bool(false)
		return w.B
	}
	data := &wire.Writer{}
	data.VarInt(1) // MODERN_DEFAULT
	data.String("127.0.0.1")
	data.UUID(server.OfflineUUID(name))
	data.String(name)
	data.VarInt(0) // properties
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(string(secret))))
	mac.Write(data.B)
	w.Bool(true)
	w.Raw(mac.Sum(nil))
	w.Raw(data.B)
	return w.B
}
