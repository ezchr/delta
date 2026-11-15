package blocks

// BreakInfo is what breaking a block takes (breakdata_gen.go, from tools/genbreakdata).
type BreakInfo struct {
	// Hardness is the block's hardness (< 0: it cannot be broken in survival, 0: at once).
	Hardness float64
	// Hand reports whether the block drops (is harvested) without a tool.
	Hand bool
	// Effective has a bit per tool kind (ToolPickaxe...) that mines the block faster.
	Effective uint8
	// MinTier is per tool kind the lowest harvest level that harvests it (0: that kind does not).
	MinTier [6]uint8
}

// Tool kinds of BreakInfo.
const (
	ToolPickaxe = iota
	ToolAxe
	ToolShovel
	ToolHoe
	ToolSword
	ToolShears
)

// Break is the break data of a Bedrock block name ("minecraft:stone"); false for blocks the
// data does not know (custom blocks, blocks newer than it).
func Break(name string) (BreakInfo, bool) {
	b, ok := breakData[name]
	return b, ok
}
