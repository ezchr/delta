package bridge

import (
	"strings"

	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
)

// The player's own item use (eating, drinking, bows, tridents, crossbows...).
//
// The vanilla Java client starts using an item itself when it sends use_item, but never finishes
// on its own timing (useItemRemaining reaches 0 only on a server): it stops when the server says
// so, with entity event 9 or its living entity flags (data 8, bit 0 off), or when it releases the
// key. While the use key is held and it is not using anything, it sends use_item again every 4
// ticks (rightClickDelay). The Bedrock server keeps its own using-item flag (SetActorData) and
// finishes eating and drinking itself (BDS: CompletedUsingItem, the food/saturation attributes,
// the held slot, then the flag off). That flag goes to the Java client as its living entity flags,
// so a finished item ends the client's use and, with the key still held, the client sends the
// next use_item after the server finished: item after item, as on a Java server. Bedrock clients
// do the same (a new UseItem ClickAir per item once the previous one is done).

// Foods a Bedrock client eats only when hungry (vanilla items; golden apples, chorus fruit,
// suspicious stew and honey bottles can always be eaten, and potions and milk are drinks).
var hungerFoods = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`apple baked_potato beef beetroot beetroot_soup bread carrot chicken
		cod cooked_beef cooked_chicken cooked_cod cooked_mutton cooked_porkchop cooked_rabbit
		cooked_salmon cookie dried_kelp glow_berries golden_carrot melon_slice mushroom_stew mutton
		poisonous_potato porkchop potato pufferfish pumpkin_pie rabbit rabbit_stew rotten_flesh
		salmon spider_eye sweet_berries tropical_fish`) {
		m["minecraft:"+n] = true
	}
	return m
}()

func init() {
	// A new server, a dimension change (Java respawn) and a death start the client without an
	// item in use.
	OnReset(func(p *Player, newConn bool) { p.self.usingItem = false })
	respawnHooks = append(respawnHooks, func(p *Player) { p.self.usingItem = false })
}

// cannotEat reports whether the player cannot start eating item now: a food that needs hunger,
// a full hunger bar, and a game mode that eats.
func (p *Player) cannotEat(item string) bool {
	s := &p.self
	return hungerFoods[item] && s.food >= 20 && (s.mode == GameSurvival || s.mode == GameAdventure)
}

// selfUsing applies the server's using-item flag of the player: a change goes to the Java client.
func (p *Player) selfUsing(using bool) {
	s := &p.self
	if using == s.usingItem {
		return
	}
	s.usingItem = using
	held := p.itemName(p.HeldItem().Stack.NetworkID)
	p.log.Debug("own item use", "using", using, "held", held)
	if !using && useOnRelease[held] {
		return
	}
	p.sendUsingFlags(using)
}

// useOnRelease are items the Java client keeps using until the key is released (CrossbowItem: a
// full charge loads it on release; a Java server never ends the use). BDS loads a crossbow at
// full charge and clears the flag then: passed on, it would end the client's use, and with the
// key still held the client's next use_item would shoot the crossbow just loaded. The client's
// release goes to the server as usual (nothing to it once loaded); a server that stops a crossbow
// for another reason changes the held item, which ends the client's use by itself.
var useOnRelease = map[string]bool{"minecraft:crossbow": true}

// sendUsingFlags sends the player's living entity flags: using an item (in the main hand: Bedrock
// players use items in the main hand only) or not. Not using makes a client that uses an item
// stop; it is nothing to one that does not.
func (p *Player) sendUsingFlags(using bool) {
	var v byte
	if using {
		v = 0x01
	}
	wr := p.pk()
	wr.VarInt(SelfEntityID)
	wr.Byte(8)
	wr.VarInt(serByte)
	wr.Byte(v)
	wr.Byte(0xff)
	p.send(v777.ClientboundPlaySetEntityData, wr)
}
