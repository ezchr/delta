// Package javapack builds a Java Edition resource pack from the resource packs a Bedrock server
// sends: vanilla texture overrides, custom item icons (item models for the item_model component),
// custom blocks (models on spare Java block states), custom entities (item models built from their
// geometry, shown on item_display entities) and the packs' lang entries. It knows nothing about
// connections: the bridge gives it the decrypted packs and the server's custom item and block
// definitions and gets a zip and the tables it needs back.
package javapack

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// Source is one Bedrock pack's files, decrypted, with paths relative to the pack's root (the
// directory holding manifest.json).
type Source struct {
	UUID, Version, Name string
	files               map[string][]byte // lower-case path -> content
}

// maxPackFiles and maxFileSize bound what is read from a pack (a hostile server could send a zip
// bomb).
const (
	// Big add-ons are big: a Pokemon pack has 36000 files and 290 MB.
	maxPackFiles = 300000
	maxFileSize  = 32 << 20
	maxPackBytes = 2 << 30
)

// ReadSource reads a Bedrock pack (a zip) of size bytes from r. contentKey is the key the server
// gave in ResourcePacksInfo ("" for a plain pack).
func ReadSource(r io.ReaderAt, size int64, uuid, version, contentKey string) (*Source, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	if len(zr.File) > maxPackFiles {
		return nil, fmt.Errorf("pack has %d files", len(zr.File))
	}
	// The pack's root is the shallowest directory with a manifest.json.
	root, depth := "", -1
	for _, f := range zr.File {
		name := strings.ToLower(strings.ReplaceAll(f.Name, "\\", "/"))
		if path.Base(name) != "manifest.json" {
			continue
		}
		d := strings.Count(name, "/")
		if depth < 0 || d < depth {
			root, depth = strings.TrimSuffix(name, "manifest.json"), d
		}
	}
	s := &Source{UUID: uuid, Version: version, files: map[string][]byte{}}
	total := 0
	for _, f := range zr.File {
		name := strings.ToLower(strings.ReplaceAll(f.Name, "\\", "/"))
		if f.FileInfo().IsDir() || !strings.HasPrefix(name, root) {
			continue
		}
		if f.UncompressedSize64 > maxFileSize || unusedFile(name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxFileSize))
		_ = rc.Close()
		if err != nil {
			continue
		}
		total += len(b)
		if total > maxPackBytes {
			return nil, fmt.Errorf("pack is larger than %d bytes unpacked", maxPackBytes)
		}
		s.files[strings.TrimPrefix(name, root)] = b
	}
	if contentKey != "" {
		if err := s.decrypt(contentKey); err != nil {
			return nil, fmt.Errorf("decrypt: %w", err)
		}
	}
	var m struct {
		Header struct {
			Name string `json:"name"`
		} `json:"header"`
	}
	if b, ok := s.files["manifest.json"]; ok {
		_ = json.Unmarshal(stripJSON(b), &m)
	}
	s.Name = m.Header.Name
	return s, nil
}

// unusedFile reports whether the converter never looks at a file of this kind: sounds are most of
// a big pack's size, and are not converted.
func unusedFile(name string) bool {
	switch path.Ext(name) {
	case ".ogg", ".fsb", ".wav", ".mp3", ".mcstructure", ".js", ".psd", ".bbmodel":
		return true
	}
	return false
}

// contentsMagic is the magic number in an encrypted pack's contents.json header.
const contentsMagic = 0x9BCFB9FC

// decrypt decrypts an encrypted pack: contents.json (after a 256 byte header) is encrypted with
// the content key and lists each file's own key; files are AES-256-CFB8 with the first 16 bytes
// of their key as IV.
func (s *Source) decrypt(contentKey string) error {
	c, ok := s.files["contents.json"]
	if !ok || len(c) < 256 {
		return nil // nothing encrypted
	}
	if binary.LittleEndian.Uint32(c[4:8]) != contentsMagic {
		return nil // a plain contents.json
	}
	plain, err := cfb8Decrypt([]byte(contentKey), c[256:])
	if err != nil {
		return err
	}
	var list struct {
		Content []struct {
			Path string `json:"path"`
			Key  string `json:"key"`
		} `json:"content"`
	}
	if err := json.Unmarshal(stripJSON(plain), &list); err != nil {
		return fmt.Errorf("contents.json: %w (wrong content key?)", err)
	}
	for _, e := range list.Content {
		if e.Key == "" {
			continue
		}
		name := strings.ToLower(strings.ReplaceAll(e.Path, "\\", "/"))
		b, ok := s.files[name]
		if !ok {
			continue
		}
		d, err := cfb8Decrypt([]byte(e.Key), b)
		if err != nil {
			continue
		}
		s.files[name] = d
	}
	return nil
}

// cfb8Decrypt decrypts AES CFB8 (8-bit feedback) data with key and the key's first 16 bytes as IV.
func cfb8Decrypt(key, data []byte) ([]byte, error) {
	if len(key) != 32 && len(key) != 16 && len(key) != 24 {
		return nil, fmt.Errorf("key of %d bytes", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cfb8(block, key[:16], data), nil
}

func cfb8(block cipher.Block, iv, data []byte) []byte {
	reg := make([]byte, 16)
	copy(reg, iv)
	enc := make([]byte, 16)
	out := make([]byte, len(data))
	for i, c := range data {
		block.Encrypt(enc, reg)
		out[i] = c ^ enc[0]
		copy(reg, reg[1:])
		reg[15] = c
	}
	return out
}

// File returns a file of the pack by path (case-insensitive).
func (s *Source) File(name string) ([]byte, bool) {
	b, ok := s.files[strings.ToLower(strings.TrimPrefix(name, "/"))]
	return b, ok
}

// Files lists the pack's paths under dir (lower-case, sorted).
func (s *Source) Files(dir string) []string {
	dir = strings.ToLower(dir)
	var out []string
	for n := range s.files {
		if strings.HasPrefix(n, dir) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Len is the number of files in the pack.
func (s *Source) Len() int { return len(s.files) }

// stripJSON makes Bedrock's lenient JSON (comments, a BOM, trailing commas) parseable.
func stripJSON(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		case c == ',':
			// Drop a trailing comma before } or ].
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\n' || b[j] == '\r' || b[j] == '\t') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// readJSON decodes one of the pack's JSON files into v.
func (s *Source) readJSON(name string, v any) bool {
	b, ok := s.File(name)
	if !ok {
		return false
	}
	return json.Unmarshal(stripJSON(b), v) == nil
}
