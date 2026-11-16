package main

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

// Sound and particle checks: the bot logs sound and sound_entity (SOUND, with the entity's type),
// stop_sound (STOP SOUND), level_particles (PARTICLE, with its options) and level_event (LEVEL
// EVENT) as a 26.3 client reads them, and counts them by name. Steps: "expectsound NAME [N]" (at
// least N sounds of that name came since the last "soundreset"; a trailing * matches a prefix),
// "expectnosound NAME" (none came), "expectparticle NAME [N]", "expectlevelevent ID [N]",
// "soundreset", "sounds" (the counts).

var soundNames = func() map[int32]string {
	m := map[int32]string{}
	for k, v := range v777.Builtin["minecraft:sound_event"] {
		m[v] = strings.TrimPrefix(k, "minecraft:")
	}
	return m
}()

var sndCounts = map[string]int{}

var soundSources = []string{"master", "music", "record", "weather", "block", "hostile", "neutral", "player", "ambient", "voice", "ui"}

func readSoundHolder(r *wire.Reader) string {
	id := r.VarInt()
	if id > 0 {
		if n, ok := soundNames[id-1]; ok {
			return n
		}
		return "#" + strconv.Itoa(int(id-1))
	}
	name := r.String(32767)
	if r.Bool() {
		r.Float32()
	}
	return "direct:" + name
}

func srcName(i int32) string {
	if i >= 0 && int(i) < len(soundSources) {
		return soundSources[i]
	}
	return strconv.Itoa(int(i))
}

// readParticleOptions reads a 26.3 particle type and its options.
func readParticleOptions(r *wire.Reader) (string, string) {
	t := r.VarInt()
	name, ok := particleNames[t]
	if !ok {
		name = "#" + strconv.Itoa(int(t))
	}
	name = strings.TrimPrefix(name, "minecraft:")
	switch name {
	case "block", "block_marker", "falling_dust", "dust_pillar", "block_crumble":
		return name, stateName(uint32(r.VarInt()))
	case "dust":
		c := r.Int32()
		return name, fmt.Sprintf("#%06x x%.1f", c&0xffffff, r.Float32())
	case "dust_color_transition":
		a, b := r.Int32(), r.Int32()
		return name, fmt.Sprintf("#%06x->#%06x x%.1f", a&0xffffff, b&0xffffff, r.Float32())
	case "entity_effect", "tinted_leaves", "flash":
		return name, fmt.Sprintf("#%08x", uint32(r.Int32()))
	case "effect", "instant_effect":
		c := r.Int32()
		return name, fmt.Sprintf("#%08x power %.1f", uint32(c), r.Float32())
	case "dragon_breath", "sculk_charge":
		return name, fmt.Sprintf("%.2f", r.Float32())
	case "shriek":
		return name, fmt.Sprintf("delay %d", r.VarInt())
	}
	return name, ""
}

func init() {
	extraHandlers = append(extraHandlers, func(b *bot, id int32, r *wire.Reader) bool {
		switch id {
		case v777.ClientboundPlaySound:
			name := readSoundHolder(r)
			src := r.VarInt()
			x, y, z := float64(r.Int32())/8, float64(r.Int32())/8, float64(r.Int32())/8
			vol, pitch := r.Float32(), r.Float32()
			r.Int64()
			sndCounts["sound "+name]++
			log.Printf("SOUND %s %s at %.1f %.1f %.1f vol %.2f pitch %.2f (%.1f away)", name, srcName(src), x, y, z, vol, pitch,
				dist([3]float64{x, y, z}, b.pos))
		case v777.ClientboundPlaySoundEntity:
			name := readSoundHolder(r)
			src := r.VarInt()
			eid := r.VarInt()
			vol, pitch := r.Float32(), r.Float32()
			r.Int64()
			typ := "?"
			if eid == 1 {
				typ = "self"
			} else if e := es.byID[eid]; e != nil {
				typ = e.typ
			}
			sndCounts["sound "+name]++
			log.Printf("SOUND %s %s on %d (%s) vol %.2f pitch %.2f", name, srcName(src), eid, typ, vol, pitch)
		case v777.ClientboundPlayStopSound:
			f := r.Byte()
			src, name := "any", "any"
			if f&1 != 0 {
				src = srcName(r.VarInt())
			}
			if f&2 != 0 {
				name = r.String(32767)
			}
			log.Printf("STOP SOUND %s %s", src, name)
		case v777.ClientboundPlayLevelParticles:
			name, opt := readParticleOptions(r)
			r.Bool()
			r.Bool()
			x, y, z := r.Float64(), r.Float64(), r.Float64()
			dx, dy, dz := r.Float32(), r.Float32(), r.Float32()
			sx, _, _ := r.Float32(), r.Float32(), r.Float32()
			n := r.VarInt()
			r.VarInt()
			sndCounts["particle "+name]++
			log.Printf("PARTICLE %s %s at %.1f %.1f %.1f spread %.2f %.2f %.2f speed %.2f count %d", name, opt, x, y, z, dx, dy, dz, sx, n)
		case v777.ClientboundPlayLevelEvent:
			t := r.Int32()
			x, y, z := r.Position()
			data := r.Int32()
			r.Bool()
			sndCounts["event "+strconv.Itoa(int(t))]++
			extra := ""
			if t == 2001 || t == 2014 {
				extra = " " + stateName(uint32(data))
			}
			log.Printf("LEVEL EVENT %d at %d %d %d data %d%s", t, x, y, z, data, extra)
		default:
			return false
		}
		return true
	})
	count := func(prefix, name string) int {
		n := 0
		for k, c := range sndCounts {
			k = strings.TrimPrefix(k, prefix)
			if k == name || strings.HasSuffix(name, "*") && strings.HasPrefix(k, strings.TrimSuffix(name, "*")) {
				n += c
			}
		}
		return n
	}
	expect := func(kind, prefix string) func(b *bot, a []string) time.Duration {
		return func(b *bot, a []string) time.Duration {
			want := 1
			if len(a) > 2 {
				want, _ = strconv.Atoi(a[2])
			}
			b.mu.Lock()
			n := count(prefix, invArg(a, 1))
			b.mu.Unlock()
			log.Printf("EXPECT%s %s >= %d: %v (%d)", kind, invArg(a, 1), want, n >= want, n)
			return 0
		}
	}
	extraSteps["expectsound"] = expect("SOUND", "sound ")
	extraSteps["expectparticle"] = expect("PARTICLE", "particle ")
	extraSteps["expectlevelevent"] = expect("LEVELEVENT", "event ")
	extraSteps["expectnosound"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		n := count("sound ", invArg(a, 1))
		b.mu.Unlock()
		log.Printf("EXPECTNOSOUND %s: %v (%d)", invArg(a, 1), n == 0, n)
		return 0
	}
	extraSteps["soundreset"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		clear(sndCounts)
		b.mu.Unlock()
		return 0
	}
	extraSteps["sounds"] = func(b *bot, a []string) time.Duration {
		b.mu.Lock()
		var ks []string
		for k, c := range sndCounts {
			ks = append(ks, fmt.Sprintf("%s=%d", k, c))
		}
		b.mu.Unlock()
		sort.Strings(ks)
		log.Printf("SOUNDS %s", strings.Join(ks, " "))
		return 0
	}
}
