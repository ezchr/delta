package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// OnlineIdentity is the provider for a BDS with online-mode=true (or whenever a stable XUID is
// wanted). It builds a self-signed login that carries a digit XUID derived from the Java account,
// so the javabridge plugin's hook (xuidforward) can trust it (the per-start Secret) and write the
// XUID into BDS's login path. The same Java player is then the same BDS player across sessions.
//
// On its own this login would be refused by online-mode BDS; it only works with the plugin hook
// installed. The plugin stages the login when its Secret matches and its source is loopback.
type OnlineIdentity struct {
	// Secret is the per-start trust secret, set by the service from jb_start. The plugin is told
	// the same secret and stages only logins carrying it. Empty Secret means the hook will not
	// trust the login (safe: it is then treated as a normal, refused, offline login).
	Secret string
	// Namespace keys the derived XUID and UUID; it must match the plugin's namespace_uuid so the
	// SelfSignedID consistency check lines up. Zero uses defaultNamespace.
	Namespace uuid.UUID
}

// defaultNamespace is the namespace for derived ids when none is set. It must equal the plugin's
// configured namespace_uuid.
var defaultNamespace = uuid.MustParse("a3e78ee7-823a-4cb5-9fe0-532b54ccc20d")

// NewSecret returns a fresh 32-byte trust secret as hex, for the service to pass to both the
// core (OnlineIdentity.Secret) and the plugin (its staging config).
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// BedrockLogin builds the self-signed login with the derived identity.
func (o OnlineIdentity) BedrockLogin(_ context.Context, j JavaPlayer) (*BedrockLogin, error) {
	ns := o.Namespace
	if ns == uuid.Nil {
		ns = defaultNamespace
	}
	// A stable BDS UUID and an XUID, both derived from the Java account UUID so the same Java
	// player is the same BDS player every session.
	//
	// The XUID must look like a real Xbox id, not an arbitrary number: a real XUID is a 16-digit
	// decimal in the 2533.. range (the high nibble of the 64-bit id is 2). Bedrock clients can
	// crash on a malformed XUID in a broadcast player-list entry (owner, seen in play), so we
	// build a well-formed one - 2535 followed by 12 hash digits - instead of a raw hash. It stays
	// in the real-XUID key space but the derived low digits make a real-account collision
	// vanishingly unlikely.
	bedrockUUID := uuid.NewSHA1(ns, []byte("javabridge:uuid:"+j.UUID.String()))
	h := sha256.Sum256([]byte("javabridge:xuid:" + j.UUID.String()))
	low := binary.BigEndian.Uint64(h[:8]) % 1000000000000 // 12 digits
	xuid := fmt.Sprintf("2535%012d", low)
	return &BedrockLogin{
		IdentityData: login.IdentityData{
			DisplayName: j.Name,
			Identity:    bedrockUUID.String(),
			XUID:        xuid,
		},
		// trustSecret travels in the login's client data (clientdata.go reads nil ClientData and
		// builds its own; the service stitches the secret into a field the plugin reads). The
		// field name is shared with the plugin in LOGIN.md.
		trustSecret: o.Secret,
	}, nil
}
