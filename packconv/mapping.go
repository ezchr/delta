package packconv

import (
	"encoding/json"
	"sort"
	"strings"
)

// MappingSchema is the version of mapping.json's layout (docs/PACKS.md). It changes only when
// fields are removed or change meaning; new fields keep it.
const MappingSchema = 1

// Mapping is mapping.json: what the translator needs to draw the converted content.
type Mapping struct {
	Schema    int    `json:"schema"`
	Converter int    `json:"converter_version"`
	Namespace string `json:"namespace"`
	PackSHA1  string `json:"pack_sha1"`

	Items    map[string]*ItemMapping   `json:"items"`
	Blocks   map[string]*BlockMapping  `json:"blocks"`
	Entities map[string]*EntityMapping `json:"entities"`
	Sounds   map[string]*SoundMapping  `json:"sounds"`
	// EntitySounds are the packs' sounds.json events of custom entities: entity id -> event
	// ("ambient", "hurt", ...) -> Bedrock sound name (a key of Sounds when converted).
	EntitySounds map[string]map[string]string `json:"entity_sounds,omitempty"`
	// Overrides are the vanilla Java textures the pack replaces ("block/stone").
	Overrides []string `json:"overrides"`
	// Languages are the Java language codes the pack has lang files for.
	Languages []string `json:"languages"`
}

func newMapping(ns string) *Mapping {
	return &Mapping{Schema: MappingSchema, Converter: Version, Namespace: ns,
		Items: map[string]*ItemMapping{}, Blocks: map[string]*BlockMapping{},
		Entities: map[string]*EntityMapping{}, Sounds: map[string]*SoundMapping{},
		Overrides: []string{}, Languages: []string{}}
}

// ItemMapping is how a custom item is shown to Java clients.
type ItemMapping struct {
	// JavaBase is the vanilla Java item the item is sent as: it decides what the client predicts
	// (mining speed, equipping, eating, the durability bar, placing nothing).
	JavaBase string `json:"java_base"`
	// ItemModel is the minecraft:item_model component value ("" none: the base item's look).
	ItemModel string `json:"item_model,omitempty"`
	// Kind is the item's class: sword, axe, pickaxe, shovel, hoe, spear, helmet, chestplate,
	// leggings, boots, food, drink, throwable, bow, crossbow, shield, fishing_rod, block, item.
	Kind string `json:"kind"`
	// Model is how it looks: "flat", "handheld", "3d" (attachable geometry in hand, flat icon in
	// GUIs), "head" (3D on the head), "block" (the block's model), "none".
	Model string `json:"model"`
	// Name is the en_US name, NameKey the lang key the pack's lang files have it under.
	Name    string `json:"name,omitempty"`
	NameKey string `json:"name_key,omitempty"`
	// MaxStack and MaxDamage are the max_stack_size / max_damage components to send (0: the
	// base item's).
	MaxStack  int  `json:"max_stack,omitempty"`
	MaxDamage int  `json:"max_damage,omitempty"`
	Glint     bool `json:"glint,omitempty"`
	// Equipment is set for wearables: the equippable component's slot and asset.
	Equipment *EquipmentMapping `json:"equipment,omitempty"`
	// Food is set for edible items (the food + consumable components).
	Food *FoodMapping `json:"food,omitempty"`
	// Block is the custom block the item places ("" none).
	Block string `json:"block,omitempty"`
	// Source says where the definition came from: "runtime", "behavior_pack", "attachable",
	// "resource_pack" (a legacy RP item), or "identifier" (nothing but the id).
	Source string `json:"source"`
}

// EquipmentMapping is a wearable's equippable component.
type EquipmentMapping struct {
	Slot  string `json:"slot"`            // head, chest, legs, feet
	Asset string `json:"asset,omitempty"` // equipment asset id ("" none: the 3D model is shown on the head)
}

// FoodMapping is an edible item's food and consumable components.
type FoodMapping struct {
	Nutrition    int     `json:"nutrition"`
	Saturation   float64 `json:"saturation"`
	CanAlwaysEat bool    `json:"can_always_eat,omitempty"`
	UseSeconds   float64 `json:"use_seconds,omitempty"`
	Animation    string  `json:"animation,omitempty"` // eat, drink
}

// BlockMapping is how a custom block is shown to Java clients.
type BlockMapping struct {
	// JavaState is the Java block state of the block's default look, as
	// "minecraft:note_block[instrument=harp,note=0,powered=true]" ("" none: see Fallback).
	JavaState string `json:"java_state,omitempty"`
	// Slot is the index of that state in the conversion's Input.Slots (-1 none).
	Slot int `json:"slot"`
	// Solid reports whether the block has collision (its states are from a solid pool).
	Solid bool `json:"solid"`
	// Variants map block states to other looks: the first whose States all match wins, the
	// default look otherwise.
	Variants []BlockVariant `json:"variants,omitempty"`
	// Fallback is the vanilla Java block to show when no state is left ("" when one was given).
	Fallback  string `json:"fallback,omitempty"`
	Name      string `json:"name,omitempty"`
	NameKey   string `json:"name_key,omitempty"`
	ItemModel string `json:"item_model,omitempty"`
	// Approximate is set when the look is not exact (non-cube geometry on a full-cube state,
	// geometry clipped to Java's box, ...).
	Approximate string `json:"approximate,omitempty"`
}

// BlockVariant is a look of a block for some of its states.
type BlockVariant struct {
	States    map[string]any `json:"states"`
	JavaState string         `json:"java_state"`
	Slot      int            `json:"slot"`
}

// EntityMapping is how a custom entity is drawn (see Entity).
type EntityMapping struct {
	Model       string     `json:"model"`
	Scale       float64    `json:"scale"`
	Translation [3]float64 `json:"translation"`
	// ModelYaw is the yaw (degrees, counter-clockwise from above) to turn the parts by when the
	// item display's own half turn is undone (javasession.EntityModel.Yaw); 180 leaves the half
	// turn in place, which is what makes the model face the entity's way.
	ModelYaw    float64       `json:"model_yaw"`
	Parts       []PartMapping `json:"parts"`
	Bones       []EntityBone  `json:"bones"`
	Name        string        `json:"name,omitempty"`
	NameKey     string        `json:"name_key,omitempty"`
	Width       float64       `json:"width"`
	Height      float64       `json:"height"`
	StandIn     string        `json:"stand_in"`
	Unsupported []string      `json:"unsupported,omitempty"`
}

// PartMapping is one EntityPart in mapping.json.
type PartMapping struct {
	Bone     string     `json:"bone"`
	Parent   string     `json:"parent,omitempty"`
	Model    string     `json:"model"`
	Offset   [3]float64 `json:"offset"`
	Pivot    [3]float64 `json:"pivot"`
	Rotation [3]float64 `json:"rotation"`
	Scale    float64    `json:"scale"`
	// Display is the ItemDisplayContext to show the part with ("fixed": its model's fixed
	// transform scales a shrunk part back).
	Display string `json:"display"`
}

// SoundMapping is a custom Bedrock sound event as a Java sound event.
type SoundMapping struct {
	JavaEvent string `json:"java_event"`
	Category  string `json:"category,omitempty"`
	Files     int    `json:"files"`
	Skipped   int    `json:"skipped,omitempty"` // files in formats Java cannot play
}

// MarshalIndent is mapping.json's bytes (keys sorted, stable).
func (m *Mapping) MarshalIndent() []byte {
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// StateString formats a Java block state as "minecraft:block[a=1,b=2]".
func (s Slot) StateString() string {
	if len(s.State) == 0 {
		return s.Block
	}
	keys := make([]string, 0, len(s.State))
	for k := range s.State {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + s.State[k]
	}
	return s.Block + "[" + strings.Join(parts, ",") + "]"
}
