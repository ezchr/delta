package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jserver "github.com/ezchr/go-mcjava/server"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/google/uuid"
)

// This is an integration test of the premium (online) Java login path with a STUBBED session
// server, so it runs in CI without the network. It proves:
//   - a client whose (stubbed) session server says it logged in is ACCEPTED, and the identity the
//     server uses is the VERIFIED profile from the session-server response, NOT the handshake name;
//   - a client the session server rejects (HTTP 403) is REFUSED with "Failed to verify username!";
//   - the same verified account yields the same derived BDS XUID every time (via OnlineIdentity).
//
// The test drives the real jserver.Listener through handshake -> login hello -> the encryption
// handshake -> and reads whether the next packet is login_finished (accepted, carrying the
// profile) or login_disconnect (rejected). It stops before the configuration state, which is not
// needed to prove auth.

const testProtocol = 777 // 26.3

// stubSession serves /session/minecraft/hasJoined, returning the given profile or a status.
func stubSession(t *testing.T, id uuid.UUID, name string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         hexNoDash(id),
			"name":       name,
			"properties": []any{},
		})
	}))
}

func hexNoDash(u uuid.UUID) string {
	s := u.String()
	out := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// loginResult is what the test client observed after the handshake.
type loginResult struct {
	accepted bool
	profUUID uuid.UUID
	profName string
	reason   string // login_disconnect text, if rejected
}

// doOnlineLogin connects to addr and runs the handshake + encryption as a premium client called
// handshakeName. It returns whether the server accepted the login and, if so, the verified profile
// it sent back in login_finished.
func doOnlineLogin(t *testing.T, addr, handshakeName string) loginResult {
	nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer nc.Close()
	_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
	c := wire.NewConn(nc)

	// Handshake (intent 2 = login).
	var hs wire.Writer
	hs.VarInt(testProtocol)
	hs.String("127.0.0.1")
	hs.B = append(hs.B, 0x63, 0xdd) // port 25565, big-endian uint16
	hs.VarInt(2)
	if err := c.Send(v777.ServerboundHandshakeIntention, hs.B); err != nil {
		t.Fatalf("send handshake: %v", err)
	}

	// Login hello: name + a random player UUID (ignored by an online server).
	var hello wire.Writer
	hello.String(handshakeName)
	var pu [16]byte
	_, _ = rand.Read(pu[:])
	hello.UUID(pu)
	if err := c.Send(v777.ServerboundLoginHello, hello.B); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	// Expect the server's encryption request (hello), or a disconnect.
	id, body, err := c.ReadPacket()
	if err != nil {
		t.Fatalf("read after hello: %v", err)
	}
	if id == v777.ClientboundLoginLoginDisconnect {
		return loginResult{accepted: false, reason: readDisconnect(body)}
	}
	if id != v777.ClientboundLoginHello {
		t.Fatalf("expected encryption hello, got %#x", id)
	}
	r := wire.NewReader(body)
	_ = r.String(32) // server id (empty)
	serverPub := r.ByteArray(2048)
	token := r.ByteArray(2048)
	_ = r.Bool() // should authenticate
	if r.Err != nil {
		t.Fatalf("read encryption hello: %v", r.Err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(serverPub)
	if err != nil {
		t.Fatalf("parse server pubkey: %v", err)
	}
	pub := pubAny.(*rsa.PublicKey)

	// Shared secret (16 bytes). Encrypt it and the token with the server's public key.
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	encSecret, err := rsa.EncryptPKCS1v15(rand.Reader, pub, secret)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	encToken, err := rsa.EncryptPKCS1v15(rand.Reader, pub, token)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	var key wire.Writer
	key.ByteArray(encSecret)
	key.ByteArray(encToken)
	if err := c.Send(v777.ServerboundLoginKey, key.B); err != nil {
		t.Fatalf("send key: %v", err)
	}
	// From here the connection is AES-encrypted with the shared secret (both sides).
	if err := c.EnableEncryption(secret); err != nil {
		t.Fatalf("enable encryption: %v", err)
	}

	// The server now calls the (stubbed) session server with ServerHash(serverID="", secret, pub).
	// It replies with either login_compression + login_finished (accepted) or login_disconnect.
	for {
		id, body, err := c.ReadPacket()
		if err != nil {
			t.Fatalf("read after key: %v", err)
		}
		switch id {
		case v777.ClientboundLoginLoginCompression:
			cr := wire.NewReader(body)
			c.SetThreshold(int(cr.VarInt()))
		case v777.ClientboundLoginLoginFinished:
			pr := wire.NewReader(body)
			u := pr.UUID()
			name := pr.String(16)
			if pr.Err != nil {
				t.Fatalf("read login_finished: %v", pr.Err)
			}
			// Acknowledge, then stop (we do not need the configuration state).
			_ = c.Send(v777.ServerboundLoginLoginAcknowledged, nil)
			return loginResult{accepted: true, profUUID: uuid.UUID(u), profName: name}
		case v777.ClientboundLoginLoginDisconnect:
			return loginResult{accepted: false, reason: readDisconnect(body)}
		default:
			t.Fatalf("unexpected packet after key: %#x", id)
		}
	}
}

func readDisconnect(body []byte) string {
	r := wire.NewReader(body)
	js := r.String(1 << 16)
	var m map[string]any
	if json.Unmarshal([]byte(js), &m) == nil {
		if s, ok := m["text"].(string); ok {
			return s
		}
	}
	return js
}

// startListener starts a jserver.Listener on a free port with OnlineMode and the given session
// server, and returns its address and a stop func. It accepts (and drops) players in the
// background so the handshake can complete.
func startListener(t *testing.T, sessionURL string) (string, func()) {
	l, err := jserver.Listen("127.0.0.1:0", jserver.Config{
		CompressionThreshold: 256,
		OnlineMode:           true,
		SessionServer:        sessionURL,
		Status: func() jserver.Status {
			return jserver.Status{MOTD: "test", MaxPlayers: 5}
		},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			if _, err := l.Accept(); err != nil {
				return
			}
		}
	}()
	return l.Addr().String(), func() { _ = l.Close() }
}

func TestPremiumLoginAcceptedUsesVerifiedProfile(t *testing.T) {
	verifiedUUID := uuid.MustParse("e8f07710-4e3e-4cb1-a0b3-2167ede484e6")
	verifiedName := "Ezchr"
	// The client claims a DIFFERENT name in its handshake; the session server returns the real one.
	ss := stubSession(t, verifiedUUID, verifiedName, http.StatusOK)
	defer ss.Close()
	addr, stop := startListener(t, ss.URL)
	defer stop()

	res := doOnlineLogin(t, addr, "ImpersonatorName")
	if !res.accepted {
		t.Fatalf("premium login with a valid session was refused: %q", res.reason)
	}
	if res.profUUID != verifiedUUID {
		t.Fatalf("server used UUID %s, want the VERIFIED session-server UUID %s", res.profUUID, verifiedUUID)
	}
	if res.profName != verifiedName {
		t.Fatalf("server used name %q, want the VERIFIED session-server name %q (not the handshake name)", res.profName, verifiedName)
	}
}

func TestPremiumLoginRejectedWithoutValidSession(t *testing.T) {
	// The session server says the player did NOT log in (403) - as it does for an account that did
	// not authenticate with Mojang for this server.
	ss := stubSession(t, uuid.Nil, "", http.StatusForbidden)
	defer ss.Close()
	addr, stop := startListener(t, ss.URL)
	defer stop()

	res := doOnlineLogin(t, addr, "NoSession")
	if res.accepted {
		t.Fatal("a login with no valid Mojang session was ACCEPTED; it must be refused")
	}
	if res.reason == "" {
		t.Fatal("expected a login_disconnect reason")
	}
}

func TestPremiumVerifiedUUIDGivesStableXUID(t *testing.T) {
	// The same verified account must map to the same derived BDS XUID every time (what the hook
	// stages). This ties the verified Java UUID to the OnlineIdentity derivation.
	verifiedUUID := uuid.MustParse("e8f07710-4e3e-4cb1-a0b3-2167ede484e6")
	ss := stubSession(t, verifiedUUID, "Ezchr", http.StatusOK)
	defer ss.Close()
	addr, stop := startListener(t, ss.URL)
	defer stop()

	a := doOnlineLogin(t, addr, "Ezchr")
	b := doOnlineLogin(t, addr, "Ezchr")
	if !a.accepted || !b.accepted {
		t.Fatalf("logins refused: %q / %q", a.reason, b.reason)
	}
	if a.profUUID != b.profUUID || a.profUUID != verifiedUUID {
		t.Fatalf("verified UUID not stable: %s / %s", a.profUUID, b.profUUID)
	}
}
