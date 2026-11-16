package bridge

import (
	"log/slog"
	"sync"
	"time"

	jserver "github.com/ezchr/go-mcjava/server"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
)

// JavaConn is the Java client's connection in the play state. Packets are queued (never blocking
// the caller, which may hold the player lock) and written by one goroutine in bursts, as
// javasession does. Every packet is written with its 26.3 (v777) id; the writer remaps it for the
// client's version.
type JavaConn struct {
	log  *slog.Logger
	conn *wire.Conn
	ver  *version.Version

	outMu  sync.Mutex
	out    []outPacket
	wake   chan struct{}
	closed chan struct{}
	once   sync.Once

	writers sync.Pool
}

type outPacket struct {
	id int32
	w  *wire.Writer
}

// flushDelay batches the packets queued in a burst into one write.
const flushDelay = 2 * time.Millisecond

// maxQueued is how many packets may wait before a client that stopped reading is dropped.
const maxQueued = 1 << 16

func newJavaConn(jp *jserver.Player, log *slog.Logger) *JavaConn {
	c := &JavaConn{log: log, conn: jp.Conn, ver: jp.Version, wake: make(chan struct{}, 1), closed: make(chan struct{})}
	if c.ver == nil {
		c.ver = version.Newest
	}
	c.writers.New = func() any { return &wire.Writer{B: make([]byte, 0, 256)} }
	go c.writeLoop()
	return c
}

// Packet returns a writer for a packet body; pass it to Queue.
func (c *JavaConn) Packet() *wire.Writer {
	w := c.writers.Get().(*wire.Writer)
	w.Reset()
	return w
}

// Queue sends a packet built with Packet (id is the v777 id).
func (c *JavaConn) Queue(id int32, w *wire.Writer) {
	select {
	case <-c.closed:
		return
	default:
	}
	c.outMu.Lock()
	c.out = append(c.out, outPacket{id, w})
	n := len(c.out)
	c.outMu.Unlock()
	if n > maxQueued {
		c.log.Info("java send queue full", "packets", n)
		c.Close()
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Closed is closed once the connection is closing.
func (c *JavaConn) Closed() <-chan struct{} { return c.closed }

// Close closes the connection after writing what is queued (at most a second).
func (c *JavaConn) Close() {
	c.once.Do(func() {
		_ = c.conn.NetConn().SetWriteDeadline(time.Now().Add(time.Second))
		close(c.closed)
	})
}

func (c *JavaConn) writeLoop() {
	defer c.conn.Close()
	var batch []outPacket
	for {
		closing := false
		select {
		case <-c.wake:
			time.Sleep(flushDelay)
		case <-c.closed:
			closing = true
		}
		c.outMu.Lock()
		batch, c.out = c.out, batch[:0]
		c.outMu.Unlock()
		for _, p := range batch {
			var err error
			if id := c.ver.ClientboundPlay(p.id); id >= 0 { // -1: the client's version has no such packet
				err = c.conn.WritePacket(id, p.w.B)
			}
			if cap(p.w.B) <= 1<<16 {
				c.writers.Put(p.w)
			}
			if err != nil {
				c.Close()
				return
			}
		}
		if err := c.conn.Flush(); err != nil || closing {
			c.Close()
			return
		}
	}
}

// javaPacket is one packet read from the client, with its v777 id (-1: a packet of an older
// version that 26.3 does not have; legacyID is then its own id).
type javaPacket struct {
	id, legacyID int32
	body         []byte
	err          error
}

// readLoop reads the client's packets into ch until the connection fails.
func (c *JavaConn) readLoop(ch chan<- javaPacket, done <-chan struct{}) {
	for {
		id, body, err := c.conn.ReadPacket()
		pk := javaPacket{id: -1, legacyID: id, err: err}
		if err == nil {
			pk.id = c.ver.ServerboundPlay(id)
			pk.body = append([]byte(nil), body...) // the connection reuses its read buffer
		}
		select {
		case ch <- pk:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

// Disconnect sends a disconnect reason and closes the connection.
func (c *JavaConn) Disconnect(reason string) {
	w := c.Packet()
	writeText(w, reason)
	c.Queue(v777.ClientboundPlayDisconnect, w)
	c.Close()
}
