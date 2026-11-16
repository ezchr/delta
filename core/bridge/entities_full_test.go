package bridge

import (
	"testing"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestAnimFullSet: the nearest anim_full_mobs mobs are shown with the full-detail part set, the
// others with the merged one; a mob changes set in one bundle, and only once it is well past
// the boundary.
func TestAnimFullSet(t *testing.T) {
	p, take := soundPlayer(t, version.Newest)
	pack := animTestPack()
	m := pack.res.Entities["t:walker"].Anim
	m.Full, m.Parts = m.Parts, m.Parts[:2] // 4 parts in full detail, 2 merged
	p.rem.jpack = pack
	animFullMobs = 1
	defer func() { animFullMobs = 0 }()
	tick := func(n int) {
		for i := 0; i < n; i++ {
			p.animTick()
		}
	}
	var mobs []*modelEntity
	for i := 0; i < 4; i++ {
		rid := uint64(10 + i)
		p.dispatchBedrock(&packet.AddActor{EntityType: "t:walker", EntityRuntimeID: rid, EntityUniqueID: -int64(rid), Position: mgl32.Vec3{4 + 3*float32(i), 0, 0}})
		mobs = append(mobs, p.packs.ents[rid])
	}
	tick(1)
	counts := func() (n [4]int) {
		for i, me := range mobs {
			n[i] = len(me.anim.parts)
		}
		return n
	}
	if got := counts(); got != [4]int{4, 2, 2, 2} || !mobs[0].anim.full {
		t.Fatalf("parts per mob %v, want the nearest in full detail", got)
	}
	take()
	// The player walks past the second mob: it is nearest now, but the first is only one place
	// down and keeps its full set too.
	p.mv.pos[0] = 8
	tick(animRankTicks)
	if got := counts(); got != [4]int{4, 4, 2, 2} {
		t.Fatalf("parts per mob %v after one place", got)
	}
	// The swap of the second mob came as one bundle: delimiter, removal of its two displays,
	// four new ones with their data, passengers, delimiter.
	var seq []int32
	for _, o := range take() {
		switch o.id {
		case v777.ClientboundPlayBundleDelimiter, v777.ClientboundPlayRemoveEntities, v777.ClientboundPlayAddEntity, v777.ClientboundPlaySetPassengers:
			seq = append(seq, o.id)
		}
	}
	D, R, A, P := v777.ClientboundPlayBundleDelimiter, v777.ClientboundPlayRemoveEntities, v777.ClientboundPlayAddEntity, v777.ClientboundPlaySetPassengers
	want := []int32{D, R, A, A, A, A, P, D}
	found := false
	for i := 0; i+len(want) <= len(seq) && !found; i++ {
		found = true
		for j := range want {
			found = found && seq[i+j] == want[j]
		}
	}
	if !found {
		t.Fatalf("no swap in one bundle in %v", seq)
	}
	// Far on the other side: the first mob is last now and is merged again.
	p.mv.pos[0] = 14
	tick(animRankTicks)
	if got := counts(); got[0] != 2 || got[3] != 4 {
		t.Fatalf("parts per mob %v from the far side", got)
	}
}
