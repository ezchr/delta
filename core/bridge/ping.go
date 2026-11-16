package bridge

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// /ping, like a proxy's: the player's round trip to the Bedrock server they are on,
// the median of the last pingSamples measurements (one a second). A measurement is the
// connection's RakNet (or NetherNet) round trip, or, when the server answers NetworkStackLatency
// requests, the time to that answer, which includes the server's packet handling as a Bedrock
// client's own ping would.
//
//	/ping              your ping
//	/ping @s | @p      your ping
//	/ping @a           everyone's on this bridge and server
//	/ping @r           a random one of those
//	/ping <player>     a player's ping: known for bridge players; Bedrock servers do not tell
//	                   anyone else's (a server with its own /ping gets the command instead)
//
// The Java client's tab list shows the same number for the player.

// pingSamples is how many measurements the median is of.
const pingSamples = 5

// pingState is a player's ping (p.mu).
type pingState struct {
	samples []time.Duration // the last pingSamples, oldest first
	ticks   int
	// nsl is the NetworkStackLatency request waiting for its answer (0: none) and when it went.
	nsl     int64
	nslSent time.Time
	nslOK   bool // the server answers them
	nslMiss int  // requests sent without an answer
	tabMS   int64
	// java is the Java client's own keep-alive round trip to the bridge.
	java time.Duration
	// names are the players the server listed (PlayerList), lowercase -> as shown.
	names map[string]string
}

func (s *pingState) add(d time.Duration) {
	if d <= 0 {
		return
	}
	s.samples = append(s.samples, d)
	if len(s.samples) > pingSamples {
		s.samples = s.samples[len(s.samples)-pingSamples:]
	}
}

// median is the ping, false before the first measurement.
func (s *pingState) median() (time.Duration, bool) {
	if len(s.samples) == 0 {
		return 0, false
	}
	v := slices.Clone(s.samples)
	slices.Sort(v)
	return v[len(v)/2], true
}

func (s *pingState) reset() {
	*s = pingState{java: s.java, tabMS: s.tabMS}
}

// connRTT is a connection's transport round trip (0 if it has none to give).
func connRTT(c *minecraft.Conn) (d time.Duration) {
	defer func() {
		if recover() != nil {
			d = 0 // a network without Latency (gophertunnel panics)
		}
	}()
	return 2 * c.Latency()
}

func init() {
	OnTick(func(p *Player) {
		s := &p.ping
		if p.rem == nil {
			return
		}
		if s.ticks++; s.ticks%20 != 0 {
			return
		}
		if s.nsl != 0 && time.Since(s.nslSent) > 5*time.Second {
			s.nsl = 0
			if s.nslMiss++; s.nslMiss >= 3 && !s.nslOK {
				s.nslMiss = -1 << 30 // the server does not answer: transport round trips only
				p.log.Debug("ping: the server does not answer NetworkStackLatency; using the transport round trip")
			}
		}
		if !s.nslOK {
			s.add(connRTT(p.rem.conn))
		}
		if s.nsl == 0 && (s.nslOK || s.nslMiss >= 0) {
			s.nsl, s.nslSent = time.Now().UnixMilli()*1000+rand.Int64N(1000), time.Now()
			p.rem.write(&packet.NetworkStackLatency{Timestamp: s.nsl, NeedsResponse: true})
		}
		p.updateTabPing()
	})
	OnBedrock(packet.IDNetworkStackLatency, func(p *Player, pk *packet.NetworkStackLatency) {
		s := &p.ping
		if pk.NeedsResponse || s.nsl == 0 {
			return
		}
		if t := pk.Timestamp; t == s.nsl || t == s.nsl*1000 || t == s.nsl/1000 {
			if !s.nslOK {
				p.log.Debug("ping: the server answers NetworkStackLatency", "rtt", time.Since(s.nslSent))
			}
			s.nsl, s.nslOK = 0, true
			s.add(time.Since(s.nslSent))
		}
	})
	OnBedrock(packet.IDPlayerList, func(p *Player, pk *packet.PlayerList) {
		s := &p.ping
		if s.names == nil {
			s.names = map[string]string{}
		}
		for _, e := range pk.Entries {
			if e.ActionType == protocol.PlayerListActionAdd {
				s.names[strings.ToLower(e.Username)] = e.Username
				s.names[e.UUID.String()] = e.Username
			} else if n, ok := s.names[e.UUID.String()]; ok {
				delete(s.names, strings.ToLower(n))
				delete(s.names, e.UUID.String())
			}
		}
	})
	OnReset(func(p *Player, newConn bool) {
		if newConn {
			p.ping.reset()
		}
	})
}

// updateTabPing shows the ping in the player's own tab list entry. p.mu is held.
func (p *Player) updateTabPing() {
	d, ok := p.ping.median()
	if !ok {
		return
	}
	ms := (d + p.ping.java).Milliseconds()
	if diff := ms - p.ping.tabMS; diff > -5 && diff < 5 {
		return
	}
	p.ping.tabMS = ms
	w := p.pk()
	w.Byte(0x10) // update latency
	w.VarInt(1)
	w.UUID(p.profile.UUID)
	w.VarInt(int32(ms))
	p.send(v777.ClientboundPlayPlayerInfoUpdate, w)
}

// pingText is "Your ping: 42 ms" or "<name>'s ping: 42 ms".
func pingText(name string, d time.Duration, ok bool) string {
	who := "Your ping"
	if name != "" {
		who = name + "'s ping"
	}
	if !ok {
		return who + ": not measured yet"
	}
	return fmt.Sprintf("%s: §a%d ms", who, d.Milliseconds())
}

// pingOf is a bridge player's ping to their server (false: not on one or not measured yet).
func pingOf(o *Player) (string, string, time.Duration, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.rem == nil {
		return o.name, "", 0, false
	}
	d, ok := o.ping.median()
	return o.name, o.rem.target, d, ok
}

// pingCommand is /ping [player | @s | @p | @a | @r]. Called without p.mu.
func (p *Player) pingCommand(args []string) {
	arg := strings.Trim(strings.TrimSpace(strings.Join(args, " ")), `"`)
	p.mu.Lock()
	r := p.rem
	d, ok := p.ping.median()
	java := p.ping.java
	var target, shown string
	var serverPing, listed bool
	if r != nil {
		target = r.target
		if p.cmds.server != nil {
			serverPing = slices.ContainsFunc(p.cmds.server.Commands, func(c protocol.Command) bool { return strings.EqualFold(c.Name, "ping") })
		}
		shown, listed = p.ping.names[strings.ToLower(arg)]
	}
	p.mu.Unlock()
	if r == nil {
		p.message("Not connected to a Bedrock server, so there is no ping to show.")
		return
	}
	self := func() {
		line := pingText("", d, ok)
		if ok && java > 0 {
			line += fmt.Sprintf("§r §7(to %s; your connection to "+ProductName+": %d ms)", targetName(target), java.Milliseconds())
		} else if ok {
			line += fmt.Sprintf("§r §7(to %s)", targetName(target))
		}
		p.message("%s", line)
	}
	// The bridge players on the same server.
	var others []*Player
	players.Range(func(_, v any) bool {
		o := v.(*Player)
		if o != p {
			if _, t, _, _ := pingOf(o); t == target {
				others = append(others, o)
			}
		}
		return true
	})
	slices.SortFunc(others, func(a, b *Player) int { return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)) })
	if strings.HasPrefix(arg, "@") {
		sel := strings.ToLower(arg)
		if i := strings.IndexByte(sel, '['); i >= 0 {
			sel = sel[:i] // selector arguments are not supported: @a[r=5] is @a
		}
		switch sel {
		case "@s", "@p":
			self()
		case "@a", "@e":
			self()
			for _, o := range others {
				n, _, od, ook := pingOf(o)
				p.message("%s", pingText(n, od, ook))
			}
		case "@r":
			if len(others) == 0 {
				self()
				return
			}
			n, _, od, ook := pingOf(others[rand.IntN(len(others))])
			p.message("%s", pingText(n, od, ook))
		default:
			p.message("§cUse @s, @p, @a or @r, or a player's name.")
		}
		return
	}
	if arg == "" || strings.EqualFold(arg, p.name) || strings.EqualFold(arg, r.conn.IdentityData().DisplayName) {
		self()
		return
	}
	for _, o := range others {
		if strings.EqualFold(o.name, arg) {
			n, _, od, ook := pingOf(o)
			p.message("%s", pingText(n, od, ook))
			return
		}
	}
	if serverPing {
		p.mu.Lock()
		p.runServerCommand("/ping " + arg)
		p.mu.Unlock()
		return
	}
	if listed {
		p.message("§e%s§r's ping is not available: Bedrock servers do not tell other players' ping.", shown)
		return
	}
	p.message("§cNo player named §e%s§c is on %s.", arg, targetName(target))
}

// pingSuggestions are the players on the server (PlayerList) and the selectors. p.mu is held.
func pingSuggestions(p *Player) []string {
	out := []string{"@s", "@a", "@r"}
	for k, v := range p.ping.names {
		if _, err := uuid.Parse(k); err != nil {
			out = append(out, v)
		}
	}
	return out
}
