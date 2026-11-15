package bridge

import (
	"context"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"golang.org/x/oauth2"
)

// The identity a Java player has on the Bedrock server is made in one place: an
// IdentityProvider turns the Java player (after the Java login) into a Bedrock login. The
// default, OfflineIdentity, logs in without Xbox Live as the Java name, which a server with
// online-mode=false accepts. Other providers (signed logins, linked accounts) replace it via
// Config.Identity.

// JavaPlayer is what is known about a Java player when their Bedrock login is made.
type JavaPlayer struct {
	Name       string
	UUID       uuid.UUID
	Properties []jserver.Property // the login profile's properties (textures...)
	Locale     string             // client locale, e.g. "en_us"
	Address    string             // remote address of the Java connection
}

// BedrockLogin is the login the bridge's Bedrock client uses for one player.
type BedrockLogin struct {
	// TokenSource, when set, logs in with Xbox Live (an online login); IdentityData is then
	// ignored.
	TokenSource oauth2.TokenSource
	// IdentityData is used for offline logins: DisplayName (defaults to the Java name),
	// Identity (UUID string), XUID (normally empty offline).
	IdentityData login.IdentityData
	// ClientData, when set, is sent as is; nil = the bridge's own (a Windows client's device
	// data and the Java player's skin, clientdata.go/skin.go).
	ClientData *login.ClientData
}

// IdentityProvider makes the Bedrock login for a Java player. It runs once per Java login, on
// that player's goroutine, without locks; an error refuses the player with its text.
type IdentityProvider interface {
	BedrockLogin(ctx context.Context, j JavaPlayer) (*BedrockLogin, error)
}

// OfflineIdentity is the default provider: an offline login with the Java name and a UUID
// derived from the Java UUID, so the same Java player is the same Bedrock player every time.
type OfflineIdentity struct{}

// BedrockLogin ...
func (OfflineIdentity) BedrockLogin(_ context.Context, j JavaPlayer) (*BedrockLogin, error) {
	return &BedrockLogin{IdentityData: login.IdentityData{
		DisplayName: j.Name,
		Identity:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("javabridge:"+j.UUID.String())).String(),
	}}, nil
}
