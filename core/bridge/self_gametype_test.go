package bridge

import "testing"

// A game type from the server sets what flight goes with it: BDS sends no abilities then.
func TestGameTypeResetsFlight(t *testing.T) {
	p, _ := movePlayer(t)
	p.setGameType(1) // creative
	if !p.self.mayFly || !p.self.canFly() {
		t.Fatal("creative may not fly")
	}
	p.self.flying = true
	p.setGameType(1) // the same again: nothing changes
	if !p.self.flying {
		t.Error("the same game type again ended the flight")
	}
	p.setGameType(0) // survival
	if p.self.mayFly || p.self.flying || p.self.canFly() {
		t.Errorf("survival after creative: mayFly %v flying %v", p.self.mayFly, p.self.flying)
	}
	p.self.mayFly = true // abilities gave it (a server's /ability mayfly)
	p.setGameType(0)
	if !p.self.mayFly {
		t.Error("the same game type again took the abilities' may-fly")
	}
	p.setGameType(6) // spectator
	if !p.self.mayFly || !p.self.flying {
		t.Errorf("spectator: mayFly %v flying %v", p.self.mayFly, p.self.flying)
	}
}
