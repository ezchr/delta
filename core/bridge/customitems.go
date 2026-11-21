package bridge

import (
	"fmt"
	"math"
	"strings"

	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/wire"
)

// Custom items (an add-on's items: ComponentBased in the server's item registry) are paper on the
// Java client, with the converted pack's model and name (packs.go). Paper alone behaves like paper:
// it stacks to 64, has no durability bar, cannot be eaten or worn. A Bedrock client knows how the
// item behaves from its item components, which the server sends with the registry; this turns the
// ones the Java client acts on into the matching Java data components, so any add-on's items
// behave on Java as they do on Bedrock:
//
//	minecraft:durability     -> max_damage (+ max_stack_size 1: Java refuses damageable stacks)
//	max_stack_size           -> max_stack_size
//	minecraft:food           -> food + consumable (eat, or drink with a drink animation)
//	use_animation eat/drink  -> consumable (potions and the like without minecraft:food)
//	minecraft:wearable slot  -> equippable (helmet/chestplate/leggings/boots slot)
//
// What the item does stays the server's: BDS still deals the damage, finishes eating, wears the
// armour. The components only let the Java client show and start the same things.

// customItem is the Java side of one custom item.
type customItem struct {
	maxStack   int32 // 0: the Java default
	maxDamage  int32 // 0: none
	food       bool
	nutrition  int32
	saturation float32
	alwaysEat  bool
	useAnim    int32   // Java ItemUseAnimation: 1 eat, 2 drink; 0 not consumable
	useSeconds float32 // how long a use takes
	equipSlot  int32   // jitem.EquipSlot*; -1 not wearable
}

// Java ItemUseAnimation ids.
const (
	javaUseEat   = 1
	javaUseDrink = 2
)

// parseCustomItem reads the Java-relevant item components out of a registry entry's data. The data
// nests them ("components", "item_properties"), and the nesting has moved between versions, so each
// component is found wherever it is.
func parseCustomItem(data map[string]any) (customItem, bool) {
	c := customItem{equipSlot: -1}
	found := false
	if d := findMap(data, "minecraft:durability"); d != nil {
		if n, ok := nbtNum(d["max_durability"]); ok && n > 0 {
			c.maxDamage, found = int32(min(n, math.MaxInt32)), true
		}
	}
	if n, ok := findNum(data, "max_stack_size"); ok && n >= 1 && n <= 99 {
		c.maxStack, found = int32(n), true
	} else if m := findMap(data, "minecraft:max_stack_size"); m != nil {
		if n, ok := nbtNum(m["value"]); ok && n >= 1 && n <= 99 {
			c.maxStack, found = int32(n), true
		}
	}
	if f := findMap(data, "minecraft:food"); f != nil {
		c.food, found = true, true
		if n, ok := nbtNum(f["nutrition"]); ok {
			c.nutrition = int32(max(0, min(n, 1000)))
		}
		if s, ok := nbtFloat(f["saturation_modifier"]); ok {
			// Bedrock's modifier: saturation = nutrition * modifier * 2 (as Java's food builder).
			c.saturation = float32(float64(c.nutrition) * s * 2)
		}
		if b, ok := nbtNum(f["can_always_eat"]); ok && b != 0 {
			c.alwaysEat = true
		}
		c.useAnim = javaUseEat
	}
	switch anim := findAny(data, "use_animation").(type) {
	case string:
		switch strings.TrimPrefix(strings.ToLower(anim), "minecraft:") {
		case "eat":
			c.useAnim = javaUseEat
		case "drink":
			c.useAnim = javaUseDrink
		}
	default:
		// The network form is Bedrock's enum: 1 eat, 2 drink.
		if n, ok := nbtNum(anim); ok && (n == 1 || n == 2) {
			c.useAnim = int32(n)
		}
	}
	if c.useAnim != 0 {
		found = true
		c.useSeconds = 1.6 // Bedrock's default eating time (32 ticks)
		if m := findMap(data, "minecraft:use_modifiers"); m != nil {
			if s, ok := nbtFloat(m["use_duration"]); ok && s > 0 && s < 3600 {
				c.useSeconds = float32(s)
			}
		} else if t, ok := findNum(data, "use_duration"); ok && t > 0 && t < 72000 {
			c.useSeconds = float32(t) / 20 // the item_properties form is in ticks
		}
	}
	if w := findMap(data, "minecraft:wearable"); w != nil {
		slot, _ := w["slot"].(string)
		switch {
		case strings.HasSuffix(slot, "armor.head"):
			c.equipSlot = jitem.EquipSlotHead
		case strings.HasSuffix(slot, "armor.chest"):
			c.equipSlot = jitem.EquipSlotChest
		case strings.HasSuffix(slot, "armor.legs"):
			c.equipSlot = jitem.EquipSlotLegs
		case strings.HasSuffix(slot, "armor.feet"):
			c.equipSlot = jitem.EquipSlotFeet
		}
		if c.equipSlot >= 0 {
			found = true
		}
	}
	return c, found
}

// apply sets the components on the paper standing in for the item.
func (c *customItem) apply(js *jitem.Stack) {
	if c.maxDamage > 0 {
		js.MaxDamage, js.MaxStackSize = c.maxDamage, 1
		js.Add(jitem.CompMaxDamage)
		js.Add(jitem.CompMaxStackSize)
	} else if c.maxStack > 0 && c.maxStack != 64 {
		js.MaxStackSize = c.maxStack
		js.Add(jitem.CompMaxStackSize)
	}
	if c.food {
		var w wire.Writer
		w.VarInt(c.nutrition)
		w.Float32(c.saturation)
		w.Bool(c.alwaysEat)
		js.Raw = append(js.Raw, jitem.RawComponent{Type: jitem.CompFood, Data: w.B})
	}
	if c.useAnim != 0 {
		snd := "minecraft:entity.generic.eat"
		if c.useAnim == javaUseDrink {
			snd = "minecraft:entity.generic.drink"
		}
		var w wire.Writer
		w.Float32(c.useSeconds)
		w.VarInt(c.useAnim)
		w.VarInt(0) // the sound inline: no registry id
		w.String(snd)
		w.Bool(false)                   // no fixed range
		w.Bool(c.useAnim == javaUseEat) // particles
		w.VarInt(0)                     // no on-consume effects: the server applies them
		js.Raw = append(js.Raw, jitem.RawComponent{Type: jitem.CompConsumable, Data: w.B})
	}
	if c.equipSlot >= 0 {
		// The pack's equipment asset (packItem, before this) stays: without it the client has
		// nothing to draw the armour with on the body.
		var model string
		if js.Equippable != nil {
			model = js.Equippable.Model
		}
		js.Equippable = &jitem.Equippable{Slot: c.equipSlot, Model: model, Swappable: true, DamageOnHurt: c.maxDamage > 0}
		js.Add(jitem.CompEquippable)
	}
}

// findMap is the map under key anywhere in v (depth first), or nil.
func findMap(v any, key string) map[string]any {
	m, _ := findAny(v, key).(map[string]any)
	return m
}

// findNum is the number under key anywhere in v.
func findNum(v any, key string) (int64, bool) { return nbtNum(findAny(v, key)) }

// findAny is the value under key anywhere in v (depth first, maps and lists), or nil.
func findAny(v any, key string) any {
	return findAnyDepth(v, key, 0)
}

func findAnyDepth(v any, key string, depth int) any {
	if depth > 12 {
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		if x, ok := t[key]; ok {
			return x
		}
		for _, c := range t {
			if x := findAnyDepth(c, key, depth+1); x != nil {
				return x
			}
		}
	case []any:
		for _, c := range t {
			if x := findAnyDepth(c, key, depth+1); x != nil {
				return x
			}
		}
	}
	return nil
}

// nbtFloat is an NBT number as a float64.
func nbtFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	if i, ok := nbtNum(v); ok {
		return float64(i), true
	}
	return 0, false
}

// describeCustomItem is a short text of what was taken from an item, for debug logs.
func (c customItem) String() string {
	return fmt.Sprintf("stack=%d durability=%d food=%v(%d/%.1f always=%v) use=%d/%.2fs equip=%d",
		c.maxStack, c.maxDamage, c.food, c.nutrition, c.saturation, c.alwaysEat, c.useAnim, c.useSeconds, c.equipSlot)
}
