package bridge

import (
	"testing"

	jitem "github.com/df-mc/dragonfly/server/java/protocol/item"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"

	"javabridge/core/internal/javapack"
)

// TestPackItemEquippable checks that worn add-on armour gets minecraft:equippable with the pack
// equipment asset (so Java draws it on the body, not the item on the head), and the slot alone
// when the client has no pack.
func TestPackItemEquippable(t *testing.T) {
	res := &javapack.Result{
		ItemModels: map[string]string{"t:x_helmet": "javabridge:t/x_helmet"},
		ItemNames:  map[string]string{},
		ItemEquip:  map[string]javapack.Equip{"t:x_helmet": {Slot: "head", Asset: "javabridge:x_1"}, "t:x_boots": {Slot: "feet"}},
	}
	p := &Player{}
	p.rem = &Remote{jpack: &javaPack{packBuild: &packBuild{res: res}}}

	var js jitem.Stack
	js.ID, js.Count = 1, 1
	p.packItem("t:x_helmet", &js)
	if !js.Has(jitem.CompEquippable) || js.Equippable == nil || js.Equippable.Slot != jitem.EquipSlotHead || js.Equippable.Model != "javabridge:x_1" {
		t.Fatalf("helmet equippable %+v", js.Equippable)
	}
	var w wire.Writer
	js.Encode(&w)
	var back jitem.Stack
	back.Decode(&wire.Reader{B: w.B})
	if back.Equippable == nil || back.Equippable.Model != "javabridge:x_1" || back.Equippable.Slot != jitem.EquipSlotHead || back.ItemModel != "javabridge:t/x_helmet" {
		t.Fatalf("round trip %+v %q", back.Equippable, back.ItemModel)
	}

	js.Reset()
	p.packItem("t:x_boots", &js)
	if js.Equippable == nil || js.Equippable.Slot != jitem.EquipSlotFeet || js.Equippable.Model != "" {
		t.Fatalf("boots %+v", js.Equippable)
	}

	// The client refused the pack: the slot stays, no asset it does not have.
	p.packs.sent, p.packs.failed = p.rem.jpack, true
	js.Reset()
	p.packItem("t:x_helmet", &js)
	if js.Equippable == nil || js.Equippable.Model != "" || js.Has(jitem.CompItemModel) {
		t.Fatalf("no pack %+v", js.Equippable)
	}
}
