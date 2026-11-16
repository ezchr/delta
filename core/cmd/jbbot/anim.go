package main

// Animated custom mobs: the bridge draws them with item displays that ride the mob's own
// display and sends each one's transformation (entity data 11 translation, 12 scale, 13 left
// rotation, with 8 = 0 to start the interpolation). The bot counts those updates per display
// instead of logging each (-animlog logs them all).
//
// Step: animstat (one ANIM line per mob with part displays, and what they were sent since the
// last animstat).

import (
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

var animLog = flag.Bool("animlog", false, "log every display transformation update")

// displayAnim is what a display entity was sent since the last animstat.
type displayAnim struct {
	updates  int // set_entity_data with a translation or rotation
	started  int // of those, with the interpolation start (index 8)
	bad      int // a rotation that is no unit quaternion, or a value that is no number
	rots     int // move_entity_rot
	distinct map[string]bool
}

var animSince = time.Now()

// animData looks at the entity data of a display; it reports whether it was a transformation
// update (which is then not logged line by line).
func animData(e *botEntity, parts []string) bool {
	if e == nil || e.typ != "item_display" {
		return false
	}
	var tr, rot string
	start, item := false, false
	for _, p := range parts {
		k, v, _ := strings.Cut(p, "=")
		switch k {
		case "8":
			start = true
		case "11":
			tr = v
		case "13":
			rot = v
		case "23":
			item = true
		}
	}
	if tr == "" && rot == "" || item {
		return false
	}
	if e.anim == nil {
		e.anim = &displayAnim{distinct: map[string]bool{}}
	}
	a := e.anim
	a.updates++
	if start {
		a.started++
	}
	if rot != "" {
		var x, y, z, w float64
		if n, _ := fmt.Sscanf(rot, "q(%g,%g,%g,%g)", &x, &y, &z, &w); n != 4 {
			a.bad++
		} else if l := x*x + y*y + z*z + w*w; l < 0.99 || l > 1.01 {
			a.bad++
		}
	}
	if strings.Contains(tr+rot, "NaN") || strings.Contains(tr+rot, "Inf") {
		a.bad++
	}
	if len(a.distinct) < 4096 {
		a.distinct[tr+rot] = true
	}
	return !*animLog
}

// animStat prints what the part displays of each mob were sent since the last call.
func (b *bot) animStat() {
	secs := time.Since(animSince).Seconds()
	animSince = time.Now()
	ids := make([]int, 0, len(es.byID))
	for id := range es.byID {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	mobs, displays, total := 0, 0, 0
	for _, id := range ids {
		v := es.byID[int32(id)]
		var n, upd, started, bad, rots, moved int
		sample := ""
		for _, pid := range v.passengers {
			d := es.byID[pid]
			if d == nil || d.typ != "item_display" {
				continue
			}
			n++
			if a := d.anim; a != nil {
				upd += a.updates
				started += a.started
				bad += a.bad
				rots += a.rots
				if len(a.distinct) > 1 {
					moved++
				}
				d.anim = nil
			}
			if sample == "" {
				sample = fmt.Sprintf("part %d: 9=%s 10=%s 11=%s 12=%s 13=%s 23=%s", pid, d.meta[9], d.meta[10], d.meta[11], d.meta[12], d.meta[13], d.meta[23])
			}
		}
		if n == 0 {
			continue
		}
		mobs++
		displays += n
		total += upd
		log.Printf("ANIM mob %d %s at %.1f %.1f %.1f (%.1f away): %d part displays, own item %s; in %.1f s %d updates (%.1f/s, %d with interpolation start, %d bad), %d parts changed pose, %d rotations; %s",
			v.id, v.typ, v.pos[0], v.pos[1], v.pos[2], dist(v.pos, b.pos), n, v.meta[23], secs, upd, float64(upd)/secs, started, bad, moved, rots, sample)
	}
	log.Printf("ANIM %d animated mobs, %d part displays, %d updates in %.1f s (%.1f/s)", mobs, displays, total, secs, float64(total)/secs)
}
