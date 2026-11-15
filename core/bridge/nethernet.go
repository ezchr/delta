package bridge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/df-mc/go-nethernet"
	"github.com/df-mc/go-nethernet/endpoint"
	"github.com/pion/webrtc/v4"
	"github.com/sandertv/gophertunnel/minecraft"
)

// NetherNet transport to this server's own BDS, for a BDS with transport=nethernet in
// server.properties. Such a BDS opens no RakNet port at all: it answers NetherNet's HTTP
// signaling on its TCP server-port (GET /v1/join, POST /v1/join/{our id} with the SDP offer)
// and then talks WebRTC (ICE + DTLS + SCTP) over UDP.
//
// Target syntax: "nethernet:host:port" (Config.Target), dialed as http://host:port.
//
// Identity: BDS refuses a NetherNet peer whose offer has no 'a=identity' attribute (signal
// error 37). It does not require that identity to come from Xbox/Minecraft's authorization
// service: a SELF-SIGNED identity (a fresh P-384 key, a JWT signed by that key with the key in
// its 'cpk' claim, and the DTLS fingerprints signed with it) is accepted, whatever online-mode
// is (verified on BDS 1.26.60.6; online-mode=true then refuses the self-signed Login itself, as
// over RakNet). This only proves "the same peer holds this
// key for this DTLS connection"; it carries no account. Who the player is still comes from the
// Bedrock Login packet (self-signed, as on RakNet) and the online-identity hook. So no Xbox
// token is needed. The identity key must be the key the Login is signed with (BDS closes the
// connection right after the Login otherwise), so gophertunnel's dialer hands it over through
// DialContextKey (a local patch in third_party/gophertunnel, see docs/THIRD_PARTY.md); one key
// per connection, so the JWT's one-minute lifetime is always fresh.
//
// Loopback only: the WebRTC side gathers only loopback host candidates and no STUN/TURN
// candidates, so the connection never leaves the machine and BDS (Endstone) sees the player's
// address as 127.0.0.1 - which the online-identity hook's loopback trust check requires.

// netherNetNetwork is the gophertunnel network id the bridge registers for this transport.
const netherNetNetwork = "javabridge-nethernet"

// netherNetPrefix marks a NetherNet target in Config.Target.
const netherNetPrefix = "nethernet:"

// netherNetIdentityDomain is the 'idp.domain' of the self-signed identity (non-empty or the
// server rejects the assertion; "self" is what BDS itself puts in its own self-signed identity).
const netherNetIdentityDomain = "self"

func init() {
	minecraft.RegisterNetwork(netherNetNetwork, func(l *slog.Logger) minecraft.Network {
		return loopbackNetherNet{log: l}
	})
}

// netherNetURL turns the part of a target after "nethernet:" into the signaling base URL.
func netherNetURL(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), "19132")
	}
	return "http://" + addr
}

// loopbackWebRTC is the WebRTC API every NetherNet dial shares: loopback host candidates only.
var loopbackWebRTC = sync.OnceValue(func() *webrtc.API {
	var s webrtc.SettingEngine
	s.SetIncludeLoopbackCandidate(true)
	s.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	return webrtc.NewAPI(webrtc.WithSettingEngine(s))
})

// signalingHTTP is the HTTP client for NetherNet signaling: one connection per request, closed
// with a reset (SO_LINGER 0) once the answer is read. BDS closes a signaling connection right
// after its response; finishing that close normally leaves BDS's server-port in TIME_WAIT for a
// minute, and BDS (which binds it without SO_REUSEADDR) then refuses to start again during that
// minute ("Port may be in use"). The reset ends the connection on both sides at once.
var signalingHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			return c, err
		},
	},
	Timeout: 15 * time.Second,
}

// loopbackNetherNet is a gophertunnel Network that dials a local BDS over NetherNet.
type loopbackNetherNet struct{ log *slog.Logger }

// DialContext dials the BDS whose signaling base URL is address with a fresh self-signed
// identity. gophertunnel's dialer calls DialContextKey instead (the login's key).
func (n loopbackNetherNet) DialContext(ctx context.Context, address string) (net.Conn, error) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("nethernet: identity key: %w", err)
	}
	return n.DialContextKey(ctx, address, key)
}

// DialContextKey dials with a self-signed identity for key, the key the Bedrock login is signed
// with: BDS closes a NetherNet connection whose identity key is not the login's key.
func (n loopbackNetherNet) DialContextKey(ctx context.Context, address string, key *ecdsa.PrivateKey) (net.Conn, error) {
	id, err := nethernet.SelfSignedIdentity(key, netherNetIdentityDomain)
	if err != nil {
		return nil, fmt.Errorf("nethernet: identity: %w", err)
	}
	log := n.log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	d := nethernet.Dialer{
		Log:      log.With("transport", "nethernet"),
		API:      loopbackWebRTC(),
		Identity: id,
		// HTTP signaling is one request/response: every candidate goes in the offer.
		DisableTrickleICE: true,
		// BDS answers with its own (self-signed) identity; one without is accepted as well.
		AllowIdentitylessServer: true,
	}
	signaling := endpoint.ClientConfig{HTTPClient: signalingHTTP, Logger: log}.New()
	conn, err := d.DialContext(ctx, address, signaling)
	if err != nil {
		return nil, netherNetError(address, err)
	}
	return conn, nil
}

// netherNetError explains the failures a server owner can act on.
func netherNetError(address string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "error code: 37"):
		return fmt.Errorf("nethernet %s refused the bridge's self-signed connection identity (this BDS build may want one issued by Xbox services): %w", address, err)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("nethernet %s: no NetherNet signaling there (is server.properties transport=nethernet and the port its server-port?): %w", address, err)
	}
	return fmt.Errorf("nethernet %s: %w", address, err)
}

// PingContext is not used: the bridge knows the server's port (no port following).
func (loopbackNetherNet) PingContext(context.Context, string) ([]byte, error) {
	return nil, errors.New("nethernet: ping not supported")
}

// Listen is not supported: the bridge only dials.
func (loopbackNetherNet) Listen(string) (minecraft.NetworkListener, error) {
	return nil, errors.New("nethernet: listen not supported")
}
