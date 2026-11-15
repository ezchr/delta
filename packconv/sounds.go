package packconv

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Sounds: every sound event of the packs' sounds/sound_definitions.json whose files the packs
// have becomes a Java sound event "<ns>:<event>" in assets/<ns>/sounds.json, with the .ogg files
// copied (Java plays Ogg Vorbis only: .fsb and .wav files are skipped and reported). The
// translator plays a Bedrock sound by name as the Java event the mapping gives. sounds.json's
// entity sound events (entity id -> event -> sound name) are kept in the mapping too.

type soundDef struct {
	Category string            `json:"category"`
	Sounds   []json.RawMessage `json:"sounds"`
	MaxDist  *float64          `json:"max_distance"`
	MinDist  *float64          `json:"min_distance"`
	Subtitle string            `json:"subtitle"`
}

type soundFile struct {
	Name   string   `json:"name"`
	Volume *float64 `json:"volume"`
	Pitch  *float64 `json:"pitch"`
	Weight *int     `json:"weight"`
	Stream bool     `json:"stream"`
}

func (b *builder) sounds() {
	defs := map[string]soundDef{}
	for _, s := range b.in.Packs {
		if s.Encrypted {
			continue
		}
		var top map[string]json.RawMessage
		if !s.readJSON("sounds/sound_definitions.json", &top) {
			continue
		}
		if raw, ok := top["sound_definitions"]; ok {
			_ = json.Unmarshal(raw, &top)
		}
		for name, raw := range top {
			if name == "format_version" {
				continue
			}
			var d soundDef
			if json.Unmarshal(raw, &d) == nil {
				defs[name] = d
			}
		}
	}
	out := map[string]any{}
	copied := map[string]string{} // bedrock path -> java sound name
	for _, name := range sortedKeys(defs) {
		d := defs[name]
		var list []any
		skipped, missing := 0, 0
		for _, raw := range d.Sounds {
			var sf soundFile
			if json.Unmarshal(raw, &sf.Name) != nil && json.Unmarshal(raw, &sf) != nil {
				continue
			}
			if sf.Name == "" {
				continue
			}
			jname, ok := copied[strings.ToLower(sf.Name)]
			if !ok {
				data, ext, found := b.findSound(sf.Name)
				if !found {
					missing++
					continue
				}
				if ext != ".ogg" {
					skipped++
					continue
				}
				rel := b.safeName("sound", strings.TrimPrefix(texKey(strings.TrimSuffix(strings.ToLower(sf.Name), ".ogg")), "sounds/"))
				b.files[b.assetPath("sounds", rel+".ogg")] = data
				jname = b.ns + ":" + rel
				copied[strings.ToLower(sf.Name)] = jname
			}
			e := map[string]any{"name": jname}
			if sf.Volume != nil && *sf.Volume != 1 {
				e["volume"] = r4(*sf.Volume)
			}
			if sf.Pitch != nil && *sf.Pitch != 1 {
				e["pitch"] = r4(*sf.Pitch)
			}
			if sf.Weight != nil && *sf.Weight != 1 {
				e["weight"] = *sf.Weight
			}
			if sf.Stream {
				e["stream"] = true
			}
			if d.MaxDist != nil && *d.MaxDist > 0 {
				e["attenuation_distance"] = int(*d.MaxDist)
			}
			list = append(list, e)
		}
		if len(list) == 0 {
			if skipped > 0 {
				b.rep.add(catSound, name, Skipped, fmt.Sprintf("%d files in formats Java cannot play (.fsb/.wav)", skipped))
			}
			// Events whose files no pack has are vanilla sounds re-declared: nothing to convert.
			continue
		}
		event := safeSoundEvent(name)
		entry := map[string]any{"sounds": list}
		if d.Subtitle != "" {
			entry["subtitle"] = d.Subtitle
		}
		out[event] = entry
		b.m.Sounds[name] = &SoundMapping{JavaEvent: b.ns + ":" + event, Category: d.Category, Files: len(list), Skipped: skipped}
		b.res.Stats.Sounds++
		st, detail := Converted, fmt.Sprintf("%s:%s, %d files", b.ns, event, len(list))
		if skipped > 0 || missing > 0 {
			st = Approximated
			detail += fmt.Sprintf(" (%d unplayable, %d missing files left out)", skipped, missing)
		}
		b.rep.add(catSound, name, st, detail)
	}
	if len(out) > 0 {
		b.json("assets/"+b.ns+"/sounds.json", out)
	}
	b.entitySounds()
}

// findSound finds a sound file by Bedrock path (without extension) in the packs, the last first.
func (b *builder) findSound(p string) ([]byte, string, bool) {
	p = strings.ToLower(strings.TrimPrefix(p, "/"))
	for _, ext := range []string{".ogg", ".fsb", ".wav"} {
		p = strings.TrimSuffix(p, ext)
	}
	for i := len(b.in.Packs) - 1; i >= 0; i-- {
		s := b.in.Packs[i]
		if s.Encrypted {
			continue
		}
		for _, ext := range []string{".ogg", ".fsb", ".wav"} {
			if f, ok := s.File(p + ext); ok {
				return f, ext, true
			}
		}
	}
	return nil, "", false
}

// safeSoundEvent makes a Bedrock sound event name a valid Java resource path.
func safeSoundEvent(name string) string {
	s := unsafeChars.ReplaceAllString(strings.ToLower(name), "_")
	return strings.Trim(s, "/")
}

// entitySounds keeps sounds.json's per-entity events (custom entities only) in the mapping.
func (b *builder) entitySounds() {
	m := map[string]map[string]string{}
	for _, s := range b.in.Packs {
		if s.Encrypted {
			continue
		}
		var f struct {
			EntitySounds struct {
				Entities map[string]struct {
					Events map[string]json.RawMessage `json:"events"`
				} `json:"entities"`
			} `json:"entity_sounds"`
		}
		if !s.readJSON("sounds.json", &f) {
			continue
		}
		for ent, e := range f.EntitySounds.Entities {
			id := ent
			if !strings.Contains(id, ":") {
				continue // vanilla entities ("zombie")
			}
			if strings.HasPrefix(id, "minecraft:") {
				continue
			}
			for ev, raw := range e.Events {
				var name string
				if json.Unmarshal(raw, &name) != nil {
					var obj struct {
						Sound string `json:"sound"`
					}
					_ = json.Unmarshal(raw, &obj)
					name = obj.Sound
				}
				if name == "" {
					continue
				}
				if m[id] == nil {
					m[id] = map[string]string{}
				}
				m[id][ev] = name
			}
		}
	}
	if len(m) > 0 {
		b.m.EntitySounds = m
	}
}
