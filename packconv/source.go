package packconv

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Source is one Bedrock pack (resource or behaviour pack), decrypted, with paths relative to the
// pack's root (the directory holding manifest.json), lower-cased.
type Source struct {
	UUID, Version, Name string
	// Type is the manifest's first module type: "resources", "data" (behaviour pack),
	// "script", ... ("" without a manifest).
	Type string
	// Subpacks are the manifest's subpacks in manifest order.
	Subpacks []Subpack
	// Encrypted is set when the pack's files are encrypted and no (working) content key was
	// given: its files are unreadable and it converts to nothing.
	Encrypted bool
	// Origin is where the pack was read from (a path, "zip" or "memory").
	Origin string

	files   map[string][]byte // lower-case path -> content
	subpack string            // active subpack folder ("" none)
}

// Subpack is a manifest subpack entry.
type Subpack struct {
	Folder, Name string
	MemoryTier   int
}

// Read limits: a hostile server or pack could hold a zip bomb.
const (
	maxPackFiles = 20000
	maxFileSize  = 32 << 20
	maxPackBytes = 512 << 20
)

// ReadSource reads a Bedrock pack (a zip) of size bytes from r. contentKey is the key the server
// gave in ResourcePacksInfo ("" for a plain pack). uuid and version override the manifest's when
// not empty.
func ReadSource(r io.ReaderAt, size int64, uuid, version, contentKey string) (*Source, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	if len(zr.File) > maxPackFiles {
		return nil, fmt.Errorf("pack has %d files", len(zr.File))
	}
	root := zipRoot(zr)
	s := &Source{files: map[string][]byte{}, Origin: "zip"}
	total := 0
	for _, f := range zr.File {
		name := normPath(f.Name)
		if f.FileInfo().IsDir() || !strings.HasPrefix(name, root) {
			continue
		}
		if f.UncompressedSize64 > maxFileSize {
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
	s.finish(contentKey)
	if uuid != "" {
		s.UUID = uuid
	}
	if version != "" {
		s.Version = version
	}
	return s, nil
}

// zipRoot is the shallowest directory of a zip holding a manifest.json ("" for the top).
func zipRoot(zr *zip.Reader) string {
	root, depth := "", -1
	for _, f := range zr.File {
		name := normPath(f.Name)
		if path.Base(name) != "manifest.json" || strings.Contains(name, "subpacks/") {
			continue
		}
		d := strings.Count(name, "/")
		if depth < 0 || d < depth {
			root, depth = strings.TrimSuffix(name, "manifest.json"), d
		}
	}
	return root
}

func normPath(p string) string {
	return strings.TrimPrefix(strings.ToLower(strings.ReplaceAll(p, "\\", "/")), "./")
}

// ReadPack reads one pack from a directory, a .zip or a .mcpack. A key file next to an encrypted
// pack ("<pack>.key", as BDS keeps them) is used when present.
func ReadPack(p string) (*Source, error) {
	key := ""
	if b, err := os.ReadFile(p + ".key"); err == nil {
		key = strings.TrimSpace(string(b))
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		s, err := ReadSource(bytes.NewReader(b), int64(len(b)), "", "", key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		s.Origin = p
		return s, nil
	}
	root := p
	if _, err := os.Stat(filepath.Join(p, "manifest.json")); err != nil {
		// A folder holding the pack one level down (an unzipped download).
		ents, _ := os.ReadDir(p)
		for _, e := range ents {
			if e.IsDir() {
				if _, err := os.Stat(filepath.Join(p, e.Name(), "manifest.json")); err == nil {
					root = filepath.Join(p, e.Name())
					break
				}
			}
		}
	}
	s := &Source{files: map[string][]byte{}, Origin: p}
	total, count := 0, 0
	err = filepath.WalkDir(root, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		count++
		if count > maxPackFiles {
			return fmt.Errorf("pack has more than %d files", maxPackFiles)
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize {
			return nil
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return nil
		}
		total += len(b)
		if total > maxPackBytes {
			return fmt.Errorf("pack is larger than %d bytes", maxPackBytes)
		}
		rel, _ := filepath.Rel(root, fp)
		s.files[normPath(filepath.ToSlash(rel))] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	s.finish(key)
	return s, nil
}

// ReadPacks reads every pack under p: p itself when it is a pack, the packs inside an .mcaddon
// (a zip of packs), or each pack folder/zip directly inside a directory without a manifest.
func ReadPacks(p string) ([]*Source, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		if _, err := os.Stat(filepath.Join(p, "manifest.json")); err == nil {
			s, err := ReadPack(p)
			if err != nil {
				return nil, err
			}
			return []*Source{s}, nil
		}
		ents, _ := os.ReadDir(p)
		var out []*Source
		for _, e := range ents {
			n := strings.ToLower(e.Name())
			if e.IsDir() || strings.HasSuffix(n, ".zip") || strings.HasSuffix(n, ".mcpack") {
				if s, err := ReadPack(filepath.Join(p, e.Name())); err == nil && s.UUID != "" {
					out = append(out, s)
				}
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s: no packs found", p)
		}
		return out, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	// An .mcaddon: several top folders (or nested .mcpack files), each with a manifest.
	roots := map[string]bool{}
	var nested []*zip.File
	for _, f := range zr.File {
		n := normPath(f.Name)
		if strings.HasSuffix(n, ".mcpack") || (strings.HasSuffix(n, ".zip") && !strings.Contains(n, "/")) {
			nested = append(nested, f)
		}
		if path.Base(n) == "manifest.json" && !strings.Contains(n, "subpacks/") {
			roots[strings.TrimSuffix(n, "manifest.json")] = true
		}
	}
	if len(roots) <= 1 && len(nested) == 0 {
		s, err := ReadPack(p)
		if err != nil {
			return nil, err
		}
		return []*Source{s}, nil
	}
	var out []*Source
	for _, f := range nested {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxPackBytes))
		_ = rc.Close()
		if err != nil {
			continue
		}
		if s, err := ReadSource(bytes.NewReader(data), int64(len(data)), "", "", ""); err == nil {
			s.Origin = p + "!" + f.Name
			out = append(out, s)
		}
	}
	rootList := make([]string, 0, len(roots))
	for r := range roots {
		rootList = append(rootList, r)
	}
	sort.Strings(rootList)
	for _, r := range rootList {
		s := &Source{files: map[string][]byte{}, Origin: p + "!" + r}
		for _, f := range zr.File {
			n := normPath(f.Name)
			if f.FileInfo().IsDir() || !strings.HasPrefix(n, r) || f.UncompressedSize64 > maxFileSize {
				continue
			}
			deeper := false // a file of a pack nested below this one
			for o := range roots {
				if o != r && strings.HasPrefix(o, r) && strings.HasPrefix(n, o) {
					deeper = true
				}
			}
			if deeper {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(rc, maxFileSize))
			_ = rc.Close()
			if err == nil {
				s.files[strings.TrimPrefix(n, r)] = data
			}
		}
		s.finish("")
		out = append(out, s)
	}
	return out, nil
}

// NewSource makes a pack from files in memory (paths relative to the pack root), for tests and
// for callers that have the files already.
func NewSource(files map[string][]byte) *Source {
	s := &Source{files: map[string][]byte{}, Origin: "memory"}
	for k, v := range files {
		s.files[normPath(k)] = v
	}
	s.finish("")
	return s
}

// finish decrypts the pack when it is encrypted and a key is known, and reads its manifest.
func (s *Source) finish(contentKey string) {
	if c, ok := s.files["contents.json"]; ok && len(c) >= 256 && binary.LittleEndian.Uint32(c[4:8]) == contentsMagic {
		if contentKey == "" {
			s.Encrypted = true
		} else if err := s.decrypt(contentKey); err != nil {
			s.Encrypted = true
		}
	}
	var m struct {
		Header struct {
			Name    string          `json:"name"`
			UUID    string          `json:"uuid"`
			Version json.RawMessage `json:"version"`
		} `json:"header"`
		Modules []struct {
			Type string `json:"type"`
		} `json:"modules"`
		Subpacks []struct {
			Folder string `json:"folder_name"`
			Name   string `json:"name"`
			Tier   int    `json:"memory_tier"`
		} `json:"subpacks"`
	}
	if b, ok := s.files["manifest.json"]; ok {
		_ = json.Unmarshal(stripJSON(b), &m)
	}
	s.Name, s.UUID = m.Header.Name, m.Header.UUID
	s.Version = versionString(m.Header.Version)
	if len(m.Modules) > 0 {
		s.Type = m.Modules[0].Type
	}
	for _, sp := range m.Subpacks {
		s.Subpacks = append(s.Subpacks, Subpack{Folder: sp.Folder, Name: sp.Name, MemoryTier: sp.Tier})
	}
}

func versionString(raw json.RawMessage) string {
	var a []int
	if json.Unmarshal(raw, &a) == nil {
		parts := make([]string, len(a))
		for i, v := range a {
			parts[i] = fmt.Sprint(v)
		}
		return strings.Join(parts, ".")
	}
	var str string
	_ = json.Unmarshal(raw, &str)
	return str
}

// IsBehaviour reports whether the pack is a behaviour pack (a "data" module).
func (s *Source) IsBehaviour() bool { return s.Type == "data" || s.Type == "script" }

// SetSubpack makes the files of subpacks/<folder>/ override the pack's own (as the Bedrock
// client does for the chosen subpack). "" uses none. It reports whether the subpack has files.
func (s *Source) SetSubpack(folder string) bool {
	folder = strings.ToLower(strings.Trim(folder, "/"))
	if folder == "" {
		s.subpack = ""
		return true
	}
	prefix := "subpacks/" + folder + "/"
	for n := range s.files {
		if strings.HasPrefix(n, prefix) {
			s.subpack = folder
			return true
		}
	}
	return false
}

// DefaultSubpack is the subpack the Bedrock client starts with: the manifest's first one that
// has files ("" none).
func (s *Source) DefaultSubpack() string {
	for _, sp := range s.Subpacks {
		prefix := "subpacks/" + strings.ToLower(sp.Folder) + "/"
		for n := range s.files {
			if strings.HasPrefix(n, prefix) {
				return sp.Folder
			}
		}
	}
	return ""
}

// ActiveSubpack is the active subpack folder ("" none).
func (s *Source) ActiveSubpack() string { return s.subpack }

// contentsMagic is the magic number in an encrypted pack's contents.json header.
const contentsMagic = 0x9BCFB9FC

// decrypt decrypts an encrypted pack: contents.json (after a 256 byte header) is encrypted with
// the content key and lists each file's own key; files are AES-256-CFB8 with the first 16 bytes
// of their key as IV.
func (s *Source) decrypt(contentKey string) error {
	c := s.files["contents.json"]
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
		name := normPath(e.Path)
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
		return nil, errors.New("content key of the wrong length")
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

// File returns a file of the pack by path (case-insensitive), the active subpack's first.
func (s *Source) File(name string) ([]byte, bool) {
	n := normPath(strings.TrimPrefix(name, "/"))
	if s.subpack != "" {
		if b, ok := s.files["subpacks/"+s.subpack+"/"+n]; ok {
			return b, true
		}
	}
	b, ok := s.files[n]
	return b, ok
}

// Files lists the pack's paths under dir (lower-case, sorted), with the active subpack's files
// merged in under their normal paths. Other subpacks' files are left out.
func (s *Source) Files(dir string) []string {
	dir = normPath(dir)
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if strings.HasPrefix(n, dir) && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for n := range s.files {
		if strings.HasPrefix(n, "subpacks/") {
			if s.subpack != "" {
				if rest, ok := strings.CutPrefix(n, "subpacks/"+s.subpack+"/"); ok {
					add(rest)
				}
			}
			continue
		}
		add(n)
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
			if trailingComma(b, i+1) {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// trailingComma reports whether only white space and comments come between j and a } or ].
func trailingComma(b []byte, j int) bool {
	for j < len(b) {
		switch {
		case b[j] == ' ' || b[j] == '\n' || b[j] == '\r' || b[j] == '\t':
			j++
		case b[j] == '/' && j+1 < len(b) && b[j+1] == '/':
			for j < len(b) && b[j] != '\n' {
				j++
			}
		case b[j] == '/' && j+1 < len(b) && b[j+1] == '*':
			j += 2
			for j+1 < len(b) && !(b[j] == '*' && b[j+1] == '/') {
				j++
			}
			j += 2
		default:
			return b[j] == '}' || b[j] == ']'
		}
	}
	return false
}

// readJSON decodes one of the pack's JSON files into v.
func (s *Source) readJSON(name string, v any) bool {
	b, ok := s.File(name)
	if !ok {
		return false
	}
	return json.Unmarshal(stripJSON(b), v) == nil
}

// parseJSON decodes lenient JSON into v.
func parseJSON(b []byte, v any) bool { return json.Unmarshal(stripJSON(b), v) == nil }

// CleanJSON makes Bedrock's lenient JSON (comments, a BOM, trailing commas) standard JSON.
func CleanJSON(b []byte) []byte { return stripJSON(b) }
