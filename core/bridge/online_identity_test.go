package bridge

import (
	"context"
	"regexp"
	"testing"

	"github.com/google/uuid"
)

func TestOnlineIdentityStableAndWellFormed(t *testing.T) {
	ju := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	p := OnlineIdentity{Secret: "s"}
	a, err := p.BedrockLogin(context.Background(), JavaPlayer{Name: "Steve", UUID: ju})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.BedrockLogin(context.Background(), JavaPlayer{Name: "Steve", UUID: ju})
	if a.IdentityData.XUID != b.IdentityData.XUID || a.IdentityData.Identity != b.IdentityData.Identity {
		t.Fatalf("not stable: %q/%q vs %q/%q", a.IdentityData.XUID, a.IdentityData.Identity, b.IdentityData.XUID, b.IdentityData.Identity)
	}
	// A real XUID is a 16-digit decimal; a malformed one can crash Bedrock clients in the player
	// list. See LOGIN.md.
	if !regexp.MustCompile(`^2535\d{12}$`).MatchString(a.IdentityData.XUID) {
		t.Fatalf("XUID not a well-formed 16-digit id: %q", a.IdentityData.XUID)
	}
	if a.trustSecret != "s" {
		t.Fatalf("secret not carried: %q", a.trustSecret)
	}
	// A different Java account gives a different id.
	c, _ := p.BedrockLogin(context.Background(), JavaPlayer{Name: "Alex", UUID: uuid.MustParse("99999999-2222-3333-4444-555555555555")})
	if c.IdentityData.XUID == a.IdentityData.XUID {
		t.Fatal("two accounts share an XUID")
	}
}

func TestNewSecretUnique(t *testing.T) {
	a, err := NewSecret()
	if err != nil || len(a) != 64 {
		t.Fatalf("secret %q err %v", a, err)
	}
	b, _ := NewSecret()
	if a == b {
		t.Fatal("secret not random")
	}
}
