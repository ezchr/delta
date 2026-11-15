package packconv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BDSPacks are the packs a Bedrock Dedicated Server world uses, read from disk.
type BDSPacks struct {
	// Resource are the world's resource packs in conversion order: the bottom of the stack
	// first, so later packs override earlier ones (Input.Packs order).
	Resource []*Source
	// Behaviour are the world's behaviour packs, same order (Input.BehaviorPacks).
	Behaviour []*Source
	// Missing lists pack ids the world names but no folder has.
	Missing []string
}

// FromBDS reads the packs the world of a BDS installation uses: worlds/<world>/
// world_resource_packs.json and world_behavior_packs.json, resolved against the world's own
// resource_packs/behavior_packs folders and the server's. world "" is server.properties'
// level-name. In those files the first entry is the top of the stack (it wins), as in the
// game's pack list.
func FromBDS(root, world string) (*BDSPacks, error) {
	if world == "" {
		world = levelName(filepath.Join(root, "server.properties"))
	}
	if world == "" {
		world = "Bedrock level"
	}
	wdir := filepath.Join(root, "worlds", world)
	if _, err := os.Stat(wdir); err != nil {
		return nil, fmt.Errorf("world folder: %w", err)
	}
	out := &BDSPacks{}
	for _, kind := range []struct {
		list string
		dirs []string
		into *[]*Source
	}{
		{"world_resource_packs.json", []string{filepath.Join(wdir, "resource_packs"), filepath.Join(root, "resource_packs")}, &out.Resource},
		{"world_behavior_packs.json", []string{filepath.Join(wdir, "behavior_packs"), filepath.Join(root, "behavior_packs")}, &out.Behaviour},
	} {
		b, err := os.ReadFile(filepath.Join(wdir, kind.list))
		if err != nil {
			continue
		}
		var list []struct {
			ID string `json:"pack_id"`
		}
		if !parseJSON(b, &list) {
			return nil, fmt.Errorf("%s: not a pack list", kind.list)
		}
		byUUID := indexPacks(kind.dirs)
		var stack []*Source
		for _, e := range list {
			p, ok := byUUID[strings.ToLower(e.ID)]
			if !ok {
				out.Missing = append(out.Missing, e.ID)
				continue
			}
			s, err := ReadPack(p)
			if err != nil {
				out.Missing = append(out.Missing, e.ID)
				continue
			}
			stack = append(stack, s)
		}
		for i := len(stack) - 1; i >= 0; i-- { // bottom of the stack first
			*kind.into = append(*kind.into, stack[i])
		}
	}
	return out, nil
}

// indexPacks maps the header uuid of every pack folder/zip directly inside dirs to its path
// (the first directory wins).
func indexPacks(dirs []string) map[string]string {
	m := map[string]string{}
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(d, e.Name())
			var id string
			if e.IsDir() {
				b, err := os.ReadFile(filepath.Join(p, "manifest.json"))
				if err != nil {
					continue
				}
				var mf struct {
					Header struct {
						UUID string `json:"uuid"`
					} `json:"header"`
				}
				if !parseJSON(b, &mf) {
					continue
				}
				id = mf.Header.UUID
			} else if n := strings.ToLower(e.Name()); strings.HasSuffix(n, ".zip") || strings.HasSuffix(n, ".mcpack") {
				s, err := ReadPack(p)
				if err != nil {
					continue
				}
				id = s.UUID
			}
			id = strings.ToLower(id)
			if id != "" && m[id] == "" {
				m[id] = p
			}
		}
	}
	return m
}

func levelName(props string) string {
	f, err := os.Open(props)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "level-name="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
