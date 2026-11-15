package packconv

import (
	"encoding/json"
	"sort"
	"strings"
)

// attachable is a resource pack attachable (attachables/*.json): how an item looks when held or
// worn.
type attachable struct {
	id         string // the attachable's own identifier
	cond       string // the condition it is used under ("" always)
	geometry   string // default geometry id
	texture    string // default texture path
	animations []string
	file       string
	rank       [3]int // pack index, from the subpack, used for players
}

// readAttachables reads a pack's attachables into b.attach by the item each is for. Later packs,
// a pack's subpack files and attachables meant for players win.
func (b *builder) readAttachables(s *Source) {
	pack := 0
	for i, p := range b.in.Packs {
		if p == s {
			pack = i
		}
	}
	files := s.Files("attachables/")
	// The pack's own files first, the subpack's after (they win on equal rank otherwise).
	sort.SliceStable(files, func(i, j int) bool { return !s.fromSubpack(files[i]) && s.fromSubpack(files[j]) })
	for _, n := range files {
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		var f struct {
			A struct {
				D struct {
					Identifier string            `json:"identifier"`
					Item       map[string]string `json:"item"`
					Textures   map[string]string `json:"textures"`
					Geometry   map[string]string `json:"geometry"`
					Animations map[string]string `json:"animations"`
				} `json:"description"`
			} `json:"minecraft:attachable"`
		}
		if !s.readJSON(n, &f) {
			continue
		}
		d := f.A.D
		if d.Identifier == "" {
			continue
		}
		a := &attachable{id: d.Identifier, geometry: pick(d.Geometry), texture: pick(d.Textures), file: n}
		for _, k := range sortedKeys(d.Animations) {
			a.animations = append(a.animations, d.Animations[k])
		}
		sub := 0
		if s.fromSubpack(n) {
			sub = 1
		}
		targets := map[string]string{d.Identifier: ""}
		if len(d.Item) > 0 {
			targets = d.Item
		}
		for item, cond := range targets {
			c := *a
			c.cond = cond
			player := 0
			if strings.Contains(cond, "minecraft:player") || cond == "" {
				player = 1
			}
			c.rank = [3]int{pack, sub, player}
			old := b.attach[item]
			if old == nil || !rankLess(c.rank, old.rank) {
				b.attach[item] = &c
			}
		}
	}
}

func rankLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// fromSubpack reports whether the active subpack has its own copy of path n.
func (s *Source) fromSubpack(n string) bool {
	if s.subpack == "" {
		return false
	}
	_, ok := s.files["subpacks/"+s.subpack+"/"+n]
	return ok
}

// pick is a client entity/attachable map's "default" entry, else its first by key.
func pick(m map[string]string) string {
	if v, ok := m["default"]; ok {
		return v
	}
	keys := sortedKeys(m)
	if len(keys) == 0 {
		return ""
	}
	return m[keys[0]]
}

// isArmourGeometry reports whether a geometry id is the game's own armour geometry.
func isArmourGeometry(id string) bool {
	return strings.HasPrefix(id, "geometry.humanoid.armor") || strings.HasPrefix(id, "geometry.player.armor") ||
		strings.HasPrefix(id, "geometry.humanoid.armour")
}

// blocksJSONEntry is a resource pack blocks.json entry.
type blocksJSONEntry struct {
	textures map[string]string // face ("*", up, down, side, north, ...) -> terrain_texture key
	sound    string
}

func (b *builder) readBlocksJSON(s *Source) {
	var top map[string]json.RawMessage
	if !s.readJSON("blocks.json", &top) {
		return
	}
	for id, raw := range top {
		if id == "format_version" {
			continue
		}
		var e struct {
			Textures json.RawMessage `json:"textures"`
			Sound    string          `json:"sound"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		ent := blocksJSONEntry{textures: map[string]string{}, sound: e.Sound}
		var one string
		var faces map[string]string
		if json.Unmarshal(e.Textures, &one) == nil {
			ent.textures["*"] = one
		} else if json.Unmarshal(e.Textures, &faces) == nil {
			for f, t := range faces {
				ent.textures[f] = t
			}
		}
		if !strings.Contains(id, ":") {
			id = "minecraft:" + id
		}
		b.blocksJSON[id] = ent
	}
}
