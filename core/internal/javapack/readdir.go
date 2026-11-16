package javapack

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadDir reads an unpacked Bedrock pack (the folder holding manifest.json), for tools and
// tests: the bridge gets its packs from the server as zips (ReadSource).
func ReadDir(dir string) (*Source, error) {
	s := &Source{files: map[string][]byte{}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := strings.ToLower(filepath.ToSlash(rel))
		if unusedFile(name) {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxFileSize {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s.files[name] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	var m struct {
		Header struct {
			Name, UUID string
		} `json:"header"`
	}
	if b, ok := s.files["manifest.json"]; ok {
		_ = json.Unmarshal(stripJSON(b), &m)
	}
	s.Name, s.UUID = m.Header.Name, m.Header.UUID
	return s, nil
}
