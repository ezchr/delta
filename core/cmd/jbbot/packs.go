package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
)

// Resource packs (bridge/packs.go): the bot answers resource_pack_push like a client that accepts
// the pack: it downloads it, checks its SHA-1, checks every JSON file in it parses and logs what
// it holds (PACK lines), then reports accepted, downloaded and loaded. -packdecline declines
// instead. Item models in slots and entity data are logged as ITEM MODEL lines.

var (
	packDecline = flag.Bool("packdecline", false, "decline resource packs")
	packList    = flag.Bool("packlist", false, "log every file of a resource pack")
)

var modelRef = regexp.MustCompile(`javabridge:[a-z0-9/._-]+`)

func init() {
	// First, so container packets (inv.go answers them) are seen too; it never stops the chain.
	extraHandlers = append([]func(b *bot, id int32, r *wire.Reader) bool{packObserve}, extraHandlers...)
	extraHandlers = append(extraHandlers, packHandle)
}

var seenModels = map[string]bool{}

func packObserve(b *bot, id int32, r *wire.Reader) bool {
	rest := r.B[r.Off:]
	for _, m := range modelRef.FindAll(rest, -1) {
		k := string(m)
		if !seenModels[k] {
			seenModels[k] = true
			log.Printf("ITEM MODEL %s (packet %#x)", k, id)
		}
	}
	return false
}

func packHandle(b *bot, id int32, r *wire.Reader) bool {
	switch id {
	case v777.ClientboundPlayResourcePackPop:
		if r.Bool() {
			u := r.UUID()
			log.Printf("PACK POP %x", u)
		} else {
			log.Printf("PACK POP all")
		}
		return true
	case v777.ClientboundPlayResourcePackPush:
		u := r.UUID()
		url := r.String(32767)
		hash := r.String(40)
		forced := r.Bool()
		prompt := r.Bool()
		log.Printf("PACK PUSH %x url %s sha1 %s required %v prompt %v", u, url, hash, forced, prompt)
		go packDownload(b, u, url, hash)
		return true
	}
	return false
}

func packStatus(b *bot, u [16]byte, status int32) {
	w := &wire.Writer{}
	w.UUID(u)
	w.VarInt(status)
	b.mu.Lock()
	b.write(v777.ServerboundPlayResourcePack, w)
	b.mu.Unlock()
}

func packDownload(b *bot, u [16]byte, url, hash string) {
	if *packDecline {
		packStatus(b, u, 1)
		log.Printf("PACK declined")
		return
	}
	packStatus(b, u, 3)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		log.Printf("PACK ERROR download: %v", err)
		packStatus(b, u, 2)
		return
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		log.Printf("PACK ERROR download: status %d err %v", resp.StatusCode, err)
		packStatus(b, u, 2)
		return
	}
	sum := sha1.Sum(data)
	if hex.EncodeToString(sum[:]) != hash {
		log.Printf("PACK ERROR sha1 mismatch: got %x", sum)
		packStatus(b, u, 2)
		return
	}
	packStatus(b, u, 4)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		log.Printf("PACK ERROR zip: %v", err)
		packStatus(b, u, 6)
		return
	}
	kinds := map[string]int{}
	bad := 0
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		parts := strings.Split(f.Name, "/")
		k := f.Name
		if len(parts) >= 3 {
			k = strings.Join(parts[:3], "/")
		}
		kinds[k]++
		if strings.HasSuffix(f.Name, ".json") || strings.HasSuffix(f.Name, ".mcmeta") {
			rc, _ := f.Open()
			body, _ := io.ReadAll(rc)
			rc.Close()
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				bad++
				log.Printf("PACK ERROR bad json %s: %v", f.Name, err)
			}
			if f.Name == "pack.mcmeta" {
				log.Printf("PACK mcmeta %s", strings.Join(strings.Fields(string(body)), " "))
			}
		}
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		log.Printf("PACK has %s: %d", k, kinds[k])
	}
	if *packList {
		sort.Strings(names)
		for _, n := range names {
			log.Printf("PACK file %s", n)
		}
	}
	log.Printf("PACK ok: %d bytes, %d files, %d bad json", len(data), len(zr.File), bad)
	packStatus(b, u, 0)
}
