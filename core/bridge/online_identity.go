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
//
// Identity comes only from the Java player's VERIFIED Mojang profile: in online mode the Java
// listener runs the Mojang encryption handshake and the session-server hasJoined check, and the
// JavaPlayer handed here carries the UUID and name the session server returned, not the ones the
// client sent in its handshake (see third_party/.../server/{auth,server}.go). A player therefore
// cannot derive another player's BDS identity by claiming their name.
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

// DefaultNamespace is the namespace the online identity derives ids under when none is set.
// The plugin hook must be configured with the same namespace_uuid.
func DefaultNamespace() uuid.UUID { return defaultNamespace }

// NewSecret returns a fresh 32-byte trust secret as hex, for the service to pass to both the
// core (OnlineIdentity.Secret) and the plugin (its staging config).
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// derivedXUID is the well-formed 16-digit XUID derived from a Java account UUID. A real XUID is a
// 16-digit decimal in the 2533.. range; Bedrock clients can crash on a malformed XUID in a
// broadcast player-list entry, so we build "2535" + 12 hash digits rather than a raw hash. It
// stays in the real-XUID key space but the derived low digits make a real-account collision
// vanishingly unlikely.
func derivedXUID(javaUUID uuid.UUID) string {
	h := sha256.Sum256([]byte("javabridge:xuid:" + javaUUID.String()))
	low := binary.BigEndian.Uint64(h[:8]) % 1000000000000 // 12 digits
	return fmt.Sprintf("2535%012d", low)
}

// selfSignedID is the SelfSignedId the plugin hook (xuidforward) expects for a staged login:
// uuid5(namespace, xuid). The hook's require_self_signed_id_match checks exactly this, so the
// login's client data must carry it (clientdata.go copies it in for an OnlineIdentity login).
func selfSignedID(ns uuid.UUID, xuid string) string {
	return uuid.NewSHA1(ns, []byte(xuid)).String()
}

// namespaceOf returns the provider's namespace, or the default.
func (o OnlineIdentity) namespaceOf() uuid.UUID {
	if o.Namespace == uuid.Nil {
		return defaultNamespace
	}
	return o.Namespace
}

// BedrockLogin builds the self-signed login with the derived identity.
func (o OnlineIdentity) BedrockLogin(_ context.Context, j JavaPlayer) (*BedrockLogin, error) {
	ns := o.namespaceOf()
	// A stable BDS UUID and an XUID, both derived from the VERIFIED Java account UUID so the same
	// Java player is the same BDS player every session.
	bedrockUUID := uuid.NewSHA1(ns, []byte("javabridge:uuid:"+j.UUID.String()))
	xuid := derivedXUID(j.UUID)
	return &BedrockLogin{
		IdentityData: login.IdentityData{
			DisplayName: j.Name,
			Identity:    bedrockUUID.String(),
			XUID:        xuid,
		},
		// trustSecret travels in the login's client data Nonce field; selfSignedID is the value
		// the hook checks against uuid5(namespace, xuid). remote.go/clientdata.go stitch both into
		// the client data the bridge sends (nil ClientData means the bridge builds its own, with a
		// Windows client's device data and the Java player's skin).
		trustSecret:  o.Secret,
		selfSignedID: selfSignedID(ns, xuid),
	}, nil
}
