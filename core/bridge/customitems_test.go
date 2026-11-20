package bridge

import (
	"testing"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/wire"
)

func TestParseCustomItem(t *testing.T) {
	sword := map[string]any{"components": map[string]any{
		"item_properties":      map[string]any{"max_stack_size": int32(1), "hand_equipped": uint8(1)},
		"minecraft:durability": map[string]any{"max_durability": int32(6244)},
	}}
	c, ok := parseCustomItem(sword)
	if !ok || c.maxDamage != 6244 || c.useAnim != 0 || c.equipSlot != -1 {
		t.Fatalf("sword: %v %v", ok, c)
	}
	food := map[string]any{"components": map[string]any{
		"item_properties": map[string]any{"use_animation": int32(1), "use_duration": int32(32)},
		"minecraft:food":  map[string]any{"nutrition": int32(5), "saturation_modifier": float32(0.6), "can_always_eat": uint8(1)},
	}}
	c, ok = parseCustomItem(food)
	if !ok || !c.food || c.nutrition != 5 || !c.alwaysEat || c.useAnim != javaUseEat || c.useSeconds != 1.6 {
		t.Fatalf("food: %v %v", ok, c)
	}
	helmet := map[string]any{"components": map[string]any{
		"minecraft:wearable":   map[string]any{"slot": "slot.armor.head"},
		"minecraft:durability": map[string]any{"max_durability": int32(300)},
	}}
	c, ok = parseCustomItem(helmet)
	if !ok || c.equipSlot != jitem.EquipSlotHead || c.maxDamage != 300 {
		t.Fatalf("helmet: %v %v", ok, c)
	}
	plain := map[string]any{"components": map[string]any{"item_properties": map[string]any{"max_stack_size": int32(64)}}}
	if _, ok := parseCustomItem(plain); !ok { // stack 64 is found but changes nothing on apply
		t.Fatal("plain")
	}
	// Food encodes (the item package cannot read food back: clients never send full stacks).
	{
		c, _ := parseCustomItem(food)
		js := jitem.Stack{Count: 1, ID: javaPaper}
		c.apply(&js)
		var w wire.Writer
		js.Encode(&w)
		if len(js.Raw) != 2 || len(w.B) == 0 {
			t.Fatalf("food components: %d", len(js.Raw))
		}
	}
	// The others encode as a stack the item package can read back.
	for _, data := range []map[string]any{sword, helmet, plain} {
		c, _ := parseCustomItem(data)
		js := jitem.Stack{Count: 1, ID: javaPaper}
		c.apply(&js)
		var w wire.Writer
		js.Encode(&w)
		var back jitem.Stack
		r := wire.NewReader(w.B)
		back.Decode(r)
		if r.Err != nil || back.ID != javaPaper || back.MaxDamage != js.MaxDamage {
			t.Fatalf("round trip %v: err=%v back=%+v", c, r.Err, back)
		}
	}
}
