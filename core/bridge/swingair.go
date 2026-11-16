package bridge

import (
	v777 "github.com/ezchr/go-mcjava/v777"
)

// A Java 26.3 client sends punch for every arm swing: at the air, but also when it starts breaking
// a block, uses an item or hits an entity. Only a swing with none of those in the same tick is a
// swing at the air (the missed-swing input flag, which makes BDS play its "hit the air" sound).
func init() {
	notAir := func(p *Player, r *wireReader) error {
		p.mv.swingDidSomething = true
		return nil
	}
	OnJava(v777.ServerboundPlayPlayerAction, notAir)
	OnJava(v777.ServerboundPlayUseItemOn, notAir)
	OnJava(v777.ServerboundPlayUseItem, notAir)
	OnJava(v777.ServerboundPlayInteract, notAir)
}
