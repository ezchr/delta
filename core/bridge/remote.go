package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"javabridge/core/internal/blocks"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Remote is one connection to a Bedrock server: the gophertunnel client logged in as the
// player's Bedrock account, and the block palette its StartGame defined.
type Remote struct {
	p      *Player
	conn   *minecraft.Conn
	log    *slog.Logger
	target string
	gd     minecraft.GameData
	pal    *blocks.Palette

	done      chan struct{}
	closeOnce sync.Once
	// ended is closed when the read loop and the connection are gone (wait).
	ended  chan struct{}
	loaded bool // the loading screen end was sent (p.mu)
	stats  chunkStats
	// clocks are the server's world clock names by id (SyncWorldClocks, BDS 1.26.60+). p.mu.
	clocks map[uint64]string
	// testOut is what was written to a Remote without a connection (tests).
	testOut []packet.Packet
	// jpack is the Java pack converted from the server's resource packs (packs.go; nil: none).
	jpack *javaPack
}

// dialTimeout is how long connecting to the server may take.
const dialTimeout = 25 * time.Second

// dialRemote connects to target (host:port, tcp:host:port) and spawns. It does not touch the
// player's state and must be called without p.mu.
func (p *Player) dialRemote(target string) (*Remote, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	conn, err := p.dial(ctx, target)
	if err != nil {
		return nil, err
	}
	if p.login != nil && p.login.TokenSource != nil {
		p.rememberPlayFab(conn.IdentityData()) // clientdata.go
	}
	gd := conn.GameData()
	pal, err := blocks.NewPalette(gd.CustomBlocks, gd.UseBlockNetworkIDHashes)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("block palette: %w", err)
	}
	r := &Remote{p: p, conn: conn, log: p.log.With("target", target), target: target, gd: gd, pal: pal,
		done: make(chan struct{}), ended: make(chan struct{})}
	r.jpack = p.javaPackFor(conn, gd, pal) // packs.go: before the palette is used
	if pal.Custom > 0 || pal.Unmapped > 0 {
		r.log.Info("blocks without a Java equivalent", "custom", pal.Custom, "customShownAs", blocks.CustomStandIn,
			"unmappedStates", pal.Unmapped, "unmappedShownAs", "minecraft:stone")
	}
	r.log.Debug("connected", "dimension", gd.Dimension, "pos", gd.PlayerPosition, "gamemode", gd.PlayerGameMode,
		"states", len(pal.States), "custom", pal.Custom, "hashes", gd.UseBlockNetworkIDHashes, "items", len(gd.Items))
	return r, nil
}

// dial makes the Bedrock connection: logged in with the identity provider's login, spawned.
func (p *Player) dial(ctx context.Context, target string) (*minecraft.Conn, error) {
	d := minecraft.Dialer{
		ErrorLog: p.log.With("conn", target),
		// Resource packs are converted for the Java client (packs.go; off by default).
		DownloadResourcePack: packDownload,
		ResourcePackCache:    packCache(),
		// Vanilla BDS sends batches of more than the 16 MB default (crafting data, creative items).
		MaxDecompressedLen: 256 << 20,
	}
	lg := p.login
	if lg == nil {
		lg = &BedrockLogin{}
	}
	if lg.TokenSource != nil {
		d.TokenSource = lg.TokenSource
	} else {
		d.IdentityData = lg.IdentityData
		if d.IdentityData.DisplayName == "" {
			d.IdentityData.DisplayName = p.name
		}
	}
	if !legacyLogin {
		// A Windows 1.26.60 client's login: its client data, the player's skin (clientdata.go,
		// skin.go), unless the identity provider made the client data itself.
		if lg.ClientData != nil {
			d.ClientData = *lg.ClientData
		} else {
			d.ClientData = p.clientData(ctx, target)
		}
		// An online (xuidforward) login: the plugin hook trusts the login by its loopback
		// source AND the per-start secret in the Nonce field, and checks SelfSignedId against
		// uuid5(namespace, xuid). Set both here so the derived XUID is written into BDS; the
		// secret is never logged. Offline logins leave the client data untouched.
		if lg.selfSignedID != "" {
			d.ClientData.SelfSignedID = lg.selfSignedID
		}
		if lg.trustSecret != "" {
			d.ClientData.Nonce = lg.trustSecret
		}
		// Keep the derived XUID in the offline login: gophertunnel clears XBL identity data
		// (including XUID) before EncodeOffline unless this is set, which would drop the XUID
		// the hook needs to stage. Only for an online (hook) login.
		if lg.selfSignedID != "" {
			d.KeepXBLIdentityData = true
		}
		d.ClientLogin = true
	}
	network, addr := "raknet", target
	if a, ok := strings.CutPrefix(addr, "tcp:"); ok {
		network, addr = "tcp", a
	} else if a, ok := strings.CutPrefix(addr, netherNetPrefix); ok {
		network, addr = netherNetNetwork, netherNetURL(a) // nethernet.go
	}
	if !strings.Contains(addr, ":") {
		addr += ":19132"
	}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := conn.DoSpawnContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("spawn: %w", err)
	}
	return conn, nil
}

// write sends a packet to the server (buffered by gophertunnel, flushed every tick).
func (r *Remote) write(pk packet.Packet) {
	if r.conn == nil { // tests: a Remote without a connection keeps what was written
		r.testOut = append(r.testOut, pk)
		return
	}
	if err := r.conn.WritePacket(pk); err != nil {
		r.log.Debug("write", "packet", fmt.Sprintf("%T", pk), "err", err)
	}
}

// Write sends a packet to the player's current server, if there is one. p.mu must be held.
func (p *Player) Write(pk packet.Packet) {
	if p.rem != nil {
		p.rem.write(pk)
	}
}

func (r *Remote) closed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// close closes the connection (without blocking the caller).
func (r *Remote) close(reason string) {
	r.closeOnce.Do(func() {
		close(r.done)
		r.log.Debug("connection closed", "reason", reason)
		if n := r.pal.UnknownIDs(); n > 0 {
			r.log.Info("unknown block runtime ids were shown as air", "count", n)
		}
		goSafe("bedrock close", func() { _ = r.conn.Close() }, nil)
	})
}

// startReading starts the read loop (after the player was put on this connection).
func (r *Remote) startReading() {
	r.p.goSafe("bedrock read", r.readLoop)
}

// wait waits until the read loop ended (at most d).
func (r *Remote) wait(d time.Duration) {
	select {
	case <-r.ended:
	case <-time.After(d):
		r.log.Warn("bedrock read loop did not end in time")
	}
}

// readLoop handles the server's packets until the connection ends.
func (r *Remote) readLoop() {
	defer close(r.ended)
	p := r.p
	goSafe("chunk stats", r.logChunkStats, nil)
	p.after(3*time.Second, "loading end", func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.rem == r {
			r.finishLoading()
		}
	})
	for {
		pk, err := r.conn.ReadPacket()
		if err != nil {
			if r.closed() {
				return
			}
			p.locked("bedrock end", func() {
				if p.rem == r {
					p.remoteEnded(r.endReason(err, ""))
				}
			})
			r.close("read: " + err.Error())
			return
		}
		if captureDir != "" {
			r.capture(pk)
		}
		current := true
		if !p.locked(fmt.Sprintf("bedrock packet %T", pk), func() {
			if p.rem != r {
				current = false
				return
			}
			p.dispatchBedrock(pk)
		}) || !current {
			r.close("translator stopped")
			return
		}
	}
}

// remoteEnded ends the Java connection after the server ended the Bedrock one (kick, server
// stop): the Java player gets the server's reason. p.mu is held.
func (p *Player) remoteEnded(reason string) {
	r := p.rem
	p.rem = nil
	if r != nil {
		r.close(reason)
	}
	if p.gone {
		return
	}
	p.log.Info("server ended the connection", "reason", reason)
	p.java.Disconnect(reason)
}

// endReason is what the player reads when the connection to target ended with err (or a
// Disconnect packet's message): "Kicked by <world>: <reason>" or "Lost connection to <world>".
func (r *Remote) endReason(err error, message string) string {
	return r.leaveReason(err, message) // leave.go
}

// targetName is a target as the player reads it.
func targetName(string) string { return "the server" }

// finishLoading tells the server the client's loading screen is over: vanilla servers do not move
// the player (nor accept its interactions) before. Sent once per connection. p.mu is held.
func (r *Remote) finishLoading() {
	if r.loaded {
		return
	}
	r.loaded = true
	r.write(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeStart})
	r.write(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeEnd})
}

func init() {
	OnBedrock(packet.IDDisconnect, func(p *Player, pk *packet.Disconnect) {
		reason := p.rem.endReason(nil, pk.Message)
		if pk.Message == "" {
			reason = p.rem.endReason(errors.New("disconnected"), "disconnected")
		}
		p.remoteEnded(reason)
	})
	OnBedrock(packet.IDNetworkStackLatency, func(p *Player, pk *packet.NetworkStackLatency) {
		if pk.NeedsResponse {
			p.Write(&packet.NetworkStackLatency{Timestamp: pk.Timestamp})
		}
	})
	OnBedrock(packet.IDPacketViolationWarning, func(p *Player, pk *packet.PacketViolationWarning) {
		p.log.Warn("server says a packet we sent was bad", "type", pk.Type, "severity", pk.Severity, "packet", pk.PacketID, "context", pk.ViolationContext)
	})
	// Answered by gophertunnel or of no use to a Java client.
	for _, id := range []uint32{packet.IDPlayStatus, packet.IDChunkRadiusUpdated, packet.IDResourcePacksInfo,
		packet.IDBiomeDefinitionList, packet.IDCreativeContent, packet.IDCraftingData, packet.IDItemRegistry} {
		OnBedrock(id, func(*Player, packet.Packet) {})
	}
}

// eyeHeight is how far above the feet Bedrock puts a player's position.
const eyeHeight = 1.62

// refused reports whether a dial found nothing listening (a UDP port unreachable answer included).
func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused")
}

// captureDir, when set (option capture_dir), is where the LevelChunk and SubChunk
// packets are recorded for tests (testdata/*.bin.gz, see world_capture_test.go).
var captureDir string

// capture appends pk to <captureDir>/<player>.bin when it is a chunk packet: per packet the id
// (uint32 LE), the length (uint32 LE) and the payload as gophertunnel marshals it.
func (r *Remote) capture(pk packet.Packet) {
	switch pk.(type) {
	case *packet.LevelChunk, *packet.SubChunk:
	default:
		return
	}
	buf := new(bytes.Buffer)
	pk.Marshal(protocol.NewWriter(buf, 0))
	f, err := os.OpenFile(filepath.Join(captureDir, r.p.name+".bin"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[:4], pk.ID())
	binary.LittleEndian.PutUint32(hdr[4:], uint32(buf.Len()))
	_, _ = f.Write(hdr[:])
	_, _ = f.Write(buf.Bytes())
}
