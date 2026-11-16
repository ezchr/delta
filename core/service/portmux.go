package service

import (
	"bufio"
	"net"
	"net/http"
	"sync"
	"time"
)

// The Java port also answers HTTP, so Java clients can download the converted resource pack from
// the address they joined with: no second port to open or to configure. A connection whose first
// bytes are "GET " or "HEAD" goes to the HTTP handler, every other one to the Minecraft listener
// (a handshake starts with its length and packet id 0, a legacy ping with 0xFE).

// sniffTimeout is how long a connection may take to send its first bytes.
const sniffTimeout = 5 * time.Second

// maxSniffing caps the connections whose first bytes are being waited for.
const maxSniffing = 512

type portMux struct {
	ln   net.Listener
	mc   chan net.Conn
	web  chan net.Conn
	done chan struct{}
	once sync.Once
	sem  chan struct{}
}

// newPortMux splits ln: the returned listener gets the Minecraft connections, handler the HTTP
// requests.
func newPortMux(ln net.Listener, handler http.Handler) net.Listener {
	m := &portMux{ln: ln, mc: make(chan net.Conn), web: make(chan net.Conn), done: make(chan struct{}),
		sem: make(chan struct{}, maxSniffing)}
	go m.accept()
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 10 * time.Minute,
		IdleTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(webListener{m}) }()
	go func() { <-m.done; _ = srv.Close() }()
	return m
}

func (m *portMux) accept() {
	for {
		c, err := m.ln.Accept()
		if err != nil {
			m.close()
			return
		}
		select {
		case m.sem <- struct{}{}:
			go m.sniff(c)
		default:
			_ = c.Close()
		}
	}
}

func (m *portMux) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReaderSize(c, 512)
	out := m.mc
	b, err := br.Peek(1)
	if err != nil {
		<-m.sem
		_ = c.Close()
		return
	}
	if b[0] == 'G' || b[0] == 'H' {
		if b, err := br.Peek(4); err == nil && (string(b) == "GET " || string(b) == "HEAD") {
			out = m.web
		}
	}
	<-m.sem
	_ = c.SetReadDeadline(time.Time{})
	select {
	case out <- &sniffedConn{Conn: c, r: br}:
	case <-m.done:
		_ = c.Close()
	}
}

func (m *portMux) close() { m.once.Do(func() { close(m.done) }) }

// Accept returns the next Minecraft connection.
func (m *portMux) Accept() (net.Conn, error) {
	select {
	case c := <-m.mc:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *portMux) Close() error {
	m.close()
	return m.ln.Close()
}

func (m *portMux) Addr() net.Addr { return m.ln.Addr() }

// webListener is the HTTP half of a portMux.
type webListener struct{ m *portMux }

func (w webListener) Accept() (net.Conn, error) {
	select {
	case c := <-w.m.web:
		return c, nil
	case <-w.m.done:
		return nil, net.ErrClosed
	}
}
func (w webListener) Close() error   { return nil }
func (w webListener) Addr() net.Addr { return w.m.ln.Addr() }

// sniffedConn reads the bytes looked at first, then the connection.
type sniffedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *sniffedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
