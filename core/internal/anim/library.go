package anim

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"io"
	"sync"
)

// Library holds the animation and animation controller files of a set of packs and finds an
// animation or controller in them by id. Files are added while a conversion runs; Compact then
// drops the files nothing asked for and compresses the rest, so a Library can stay in memory
// for rigs compiled later (when a mob of that type is first shown).
type Library struct {
	mu    sync.Mutex
	index map[string]string // animation or controller id -> file
	files map[string]*libFile
	// cache is the files parsed last.
	cache [3]struct {
		name  string
		items map[string]json.RawMessage
	}
	next int
}

type libFile struct {
	raw  []byte // as added (dropped by Compact)
	z    []byte // compressed
	used bool
}

// NewLibrary returns an empty library.
func NewLibrary() *Library {
	return &Library{index: map[string]string{}, files: map[string]*libFile{}}
}

// AddFile adds a file (JSON without comments). A file added later wins for the ids it has.
func (l *Library) AddFile(name string, data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files[name] = &libFile{raw: data}
	for _, id := range scanIDs(data) {
		l.index[id] = name
	}
}

// scanIDs finds the object keys that look like animation or controller ids, without parsing.
func scanIDs(b []byte) []string {
	var out []string
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], '"')
		if j < 0 {
			break
		}
		start := i + j + 1
		end := start
		for end < len(b) && b[end] != '"' {
			if b[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(b) {
			break
		}
		i = end + 1
		s := b[start:end]
		if !bytes.HasPrefix(s, []byte("animation.")) && !bytes.HasPrefix(s, []byte("controller.animation.")) {
			continue
		}
		k := i
		for k < len(b) && (b[k] == ' ' || b[k] == '\t' || b[k] == '\n' || b[k] == '\r') {
			k++
		}
		if k < len(b) && b[k] == ':' {
			out = append(out, string(s))
		}
	}
	return out
}

// Has reports whether the library has an animation or controller of this id.
func (l *Library) Has(id string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.index[id]
	return ok
}

// get is the JSON of an animation or controller.
func (l *Library) get(id string) (json.RawMessage, bool) {
	if l == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	name, ok := l.index[id]
	if !ok {
		return nil, false
	}
	for i := range l.cache {
		if l.cache[i].name == name && l.cache[i].items != nil {
			r, ok := l.cache[i].items[id]
			return r, ok
		}
	}
	f := l.files[name]
	if f == nil {
		return nil, false
	}
	f.used = true
	data := f.raw
	if data == nil {
		b, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(f.z)), 64<<20))
		if err != nil {
			return nil, false
		}
		data = b
	}
	var top struct {
		A map[string]json.RawMessage `json:"animations"`
		C map[string]json.RawMessage `json:"animation_controllers"`
	}
	if json.Unmarshal(data, &top) != nil {
		return nil, false
	}
	items := top.A
	if items == nil {
		items = top.C
	} else {
		for k, v := range top.C {
			items[k] = v
		}
	}
	c := &l.cache[l.next%len(l.cache)]
	l.next++
	c.name, c.items = name, items
	r, ok := items[id]
	return r, ok
}

// Compact drops the files no lookup used and compresses the others. It returns the bytes kept.
func (l *Library) Compact() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for name, f := range l.files {
		if !f.used {
			delete(l.files, name)
			continue
		}
		if f.raw != nil {
			var buf bytes.Buffer
			w, _ := flate.NewWriter(&buf, flate.BestSpeed)
			_, _ = w.Write(f.raw)
			_ = w.Close()
			f.z, f.raw = buf.Bytes(), nil
		}
		total += len(f.z)
	}
	for id, name := range l.index {
		if l.files[name] == nil {
			delete(l.index, id)
		}
	}
	for i := range l.cache {
		l.cache[i].name, l.cache[i].items = "", nil
	}
	return total
}
