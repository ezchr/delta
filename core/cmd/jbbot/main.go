// Command jbbot is a scripted Java Edition 26.3 test client for the bridge: it logs in
// offline, finishes configuration, then plays like a client with simple vanilla physics (walking
// friction, gravity, jumping, landing on the blocks of the chunks it received) and runs a script
// of steps. It logs what the bridge sends: teleports (absolute, and relative ones, which are
// the original bridge's corrections), chunks (with a hash of each column's blocks, for comparing two
// bridges), health, respawns, chat and disconnects.
//
//	jbbot -addr 127.0.0.1:25592 -name Bot -script 'wait 3; cmd fill -20 150 -20 20 150 20 stone; ...'
//
// Script steps (separated by ';'): wait SECONDS | cmd COMMAND | chat TEXT | walk SECONDS [sprint]
// | square SECONDS | jump N | tp X Y Z (absolute: bot only, as a cheat) | respawn | expect TEXT |
// look YAW | still SECONDS | quit. Hold-to-use steps: useitem.go.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	jchunk "github.com/ezchr/go-mcjava/chunk"
	"github.com/ezchr/go-mcjava/server"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
)

var (
	addr      = flag.String("addr", "127.0.0.1:25592", "bridge address")
	name      = flag.String("name", "Bot", "player name")
	script    = flag.String("script", "still 20", "steps")
	maxSecs   = flag.Int("secs", 120, "give up after this many seconds")
	hashFile  = flag.String("hashes", "", "write column hashes here (x z hash per line)")
	dumpCols  = flag.String("dump", "", "write every block state of these columns (x,z;x,z) to <hashes>.dump")
	blocksRep = flag.String("blocks", os.Getenv("JB_BLOCKS_REPORT"), "Java blocks report (generated/reports/blocks.json of the 26.3 server data generator)")
	velocity  = flag.String("velocity", "", "Velocity forwarding.secret file: answer the modern forwarding query as the proxy would (a bridge behind Velocity)")
)

func main() {
	flag.Parse()
	log.SetFlags(log.Lmicroseconds)
	loadBlocks(*blocksRep)
	b := &bot{chunks: map[[2]int32]*jchunk.Column{}, hashes: map[[2]int32]uint64{}, stats: map[string]int{}}
	err := b.run()
	b.summary()
	if err != nil {
		log.Printf("END: %v", err)
		os.Exit(1)
	}
}

// solidState says which Java states the bot collides with (blocks.json names, minus plants,
// liquids and the like).
var solidState []bool

func loadBlocks(file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		log.Printf("no blocks report (%v): everything but air is solid", err)
		return
	}
	var rep map[string]struct {
		States []struct {
			ID int `json:"id"`
		} `json:"states"`
	}
	if json.Unmarshal(data, &rep) != nil {
		return
	}
	solidState = make([]bool, jchunk.VanillaBlockStates)
	bedState = make([]bool, jchunk.VanillaBlockStates)
	soft := []string{"water", "lava", "short_grass", "tall_grass", "fern", "flower", "dandelion", "poppy", "orchid",
		"allium", "tulip", "daisy", "cornflower", "lily_of_the_valley", "rose_bush", "peony", "lilac", "bush", "sapling",
		"torch", "vine", "kelp", "seagrass", "sugar_cane", "sign", "button", "pressure_plate", "rail", "dead_bush",
		"sweet_berry", "wheat", "carrots", "potatoes", "beetroots", "leaf_litter", "dry_grass", "cobweb", "fire",
		"redstone_wire", "tripwire", "lever", "_mushroom", "snow", "coral_fan", "pink_petals", "wildflowers", "moss_carpet",
		"_carpet", "glow_lichen", "roots", "nether_sprouts", "spore_blossom", "structure_void", "light", "grass"}
	for n, b := range rep {
		n = strings.TrimPrefix(n, "minecraft:")
		solid := n != "air" && !strings.HasSuffix(n, "_air") // not stairs ("air" is in their name)
		for _, s := range soft {
			if strings.Contains(n, s) && n != "grass_block" && n != "snow_block" && !strings.HasSuffix(n, "mushroom_block") {
				solid = false
				break
			}
		}
		for _, s := range b.States {
			if s.ID < len(solidState) {
				solidState[s.ID] = solid
				bedState[s.ID] = strings.HasSuffix(n, "_bed")
			}
		}
	}
}

type bot struct {
	c    *wire.Conn
	wmu  sync.Mutex
	mu   sync.Mutex
	minY int

	chunks map[[2]int32]*jchunk.Column
	hashes map[[2]int32]uint64
	stats  map[string]int

	pos, vel   [3]float64
	yaw, pitch float32
	onGround   bool
	hColl      bool
	spawned    bool
	health     float32
	food       int32
	dead       bool
	keys       byte
	sprint     bool
	jumpQueued bool
	loadedSent bool
	lastSent   [3]float64
	sinceSent  int

	absTP, relTP, respawns, chunkCount int
	relOffsets                         []float64
	cmdNodes                           []jnode // commands.go
	cmdRoot                            int32
	suggestID                          int32
	chat                               []string
	kicked                             string
}

func (b *bot) write(id int32, w *wire.Writer) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if err := b.c.Send(id, w.B); err != nil {
		log.Printf("write %#x: %v", id, err)
	}
}

func (b *bot) run() error {
	nc, err := net.Dial("tcp", *addr)
	if err != nil {
		return err
	}
	b.c = wire.NewConn(nc)
	defer b.c.Close()
	host, port, _ := net.SplitHostPort(*addr)
	var w wire.Writer
	w.VarInt(server.ProtocolVersion)
	w.String(host)
	p, _ := strconv.Atoi(port)
	w.Uint16(uint16(p))
	w.VarInt(2)
	b.c.WritePacket(v777.ServerboundHandshakeIntention, w.B)
	w.Reset()
	w.String(*name)
	w.UUID(server.OfflineUUID(*name))
	if err := b.c.Send(v777.ServerboundLoginHello, w.B); err != nil {
		return err
	}
	for done := false; !done; {
		id, body, err := b.c.ReadPacket()
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
		switch id {
		case v777.ClientboundLoginLoginCompression:
			b.c.SetThreshold(int(wire.NewReader(body).VarInt()))
		case v777.ClientboundLoginLoginFinished:
			b.c.Send(v777.ServerboundLoginLoginAcknowledged, nil)
			done = true
		case v777.ClientboundLoginLoginDisconnect:
			return fmt.Errorf("kicked at login: %s", wire.NewReader(body).String(262144))
		case v777.ClientboundLoginCustomQuery:
			b.c.Send(v777.ServerboundLoginCustomQueryAnswer, velocityAnswer(body, *name, *velocity))
		case v777.ClientboundLoginCookieRequest:
			r := wire.NewReader(body)
			key := r.String(32767)
			w.Reset()
			w.String(key)
			if c := loginCookie(key); c != nil { // commands.go
				w.Bool(true)
				w.VarInt(int32(len(c)))
				w.Raw(c)
			} else {
				w.Bool(false)
			}
			b.c.Send(v777.ServerboundLoginCookieResponse, w.B)
		default:
			return fmt.Errorf("login: unexpected packet %#x", id)
		}
	}
	w.Reset()
	w.String("en_us")
	w.Int8(12)
	w.VarInt(0)
	w.Bool(true)
	w.Byte(0x7f)
	w.VarInt(1)
	w.Bool(false)
	w.Bool(true)
	w.VarInt(0)
	b.c.Send(v777.ServerboundConfigurationClientInformation, w.B)
	for done := false; !done; {
		id, body, err := b.c.ReadPacket()
		if err != nil {
			return fmt.Errorf("configuration: %w", err)
		}
		switch id {
		case v777.ClientboundConfigurationSelectKnownPacks:
			w.Reset()
			w.VarInt(1)
			w.String("minecraft")
			w.String("core")
			w.String(server.GameVersion)
			b.c.Send(v777.ServerboundConfigurationSelectKnownPacks, w.B)
		case v777.ClientboundConfigurationKeepAlive:
			b.c.Send(v777.ServerboundConfigurationKeepAlive, body)
		case v777.ClientboundConfigurationPing:
			b.c.Send(v777.ServerboundConfigurationPong, body)
		case v777.ClientboundConfigurationFinishConfiguration:
			b.c.Send(v777.ServerboundConfigurationFinishConfiguration, nil)
			done = true
		case v777.ClientboundConfigurationDisconnect:
			return fmt.Errorf("kicked in configuration")
		}
	}
	log.Printf("in play")
	errc := make(chan error, 1)
	go func() { errc <- b.readLoop() }()
	steps := strings.Split(*script, ";")
	deadline := time.Now().Add(time.Duration(*maxSecs) * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var stepEnd time.Time
	var cur []string
	square := false
	squareStart := time.Now()
	for {
		select {
		case err := <-errc:
			return err
		case <-tick.C:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout")
		}
		b.mu.Lock()
		spawned := b.spawned
		b.mu.Unlock()
		if !spawned {
			continue
		}
		// Next step?
		if time.Now().After(stepEnd) {
			b.mu.Lock()
			wasSprint := b.sprint
			b.keys, b.sprint, square = 0, false, false
			b.mu.Unlock()
			if wasSprint {
				b.playerCommand(2)
			}
			if len(steps) == 0 {
				log.Printf("script done")
				return nil
			}
			cur = strings.Fields(strings.TrimSpace(steps[0]))
			steps = steps[1:]
			stepEnd = time.Now()
			if len(cur) == 0 {
				continue
			}
			b.mu.Lock()
			log.Printf("STEP %s (at %.3f %.3f %.3f ground %v)", strings.Join(cur, " "), b.pos[0], b.pos[1], b.pos[2], b.onGround)
			b.mu.Unlock()
			arg := func(i int) float64 {
				if i < len(cur) {
					v, _ := strconv.ParseFloat(cur[i], 64)
					return v
				}
				return 0
			}
			switch cur[0] {
			case "wait", "still":
				stepEnd = time.Now().Add(time.Duration(arg(1) * float64(time.Second)))
			case "cmd":
				w := &wire.Writer{}
				w.String(strings.Join(cur[1:], " "))
				b.write(v777.ServerboundPlayChatCommand, w)
				stepEnd = time.Now().Add(1500 * time.Millisecond)
			case "chat":
				w := &wire.Writer{}
				w.String(strings.Join(cur[1:], " "))
				w.Int64(time.Now().UnixMilli())
				w.Int64(0)
				w.Bool(false)
				w.VarInt(0)
				w.Raw(make([]byte, 3)) // acknowledged: fixed bitset of 20 bits
				w.Byte(0)              // checksum
				b.write(v777.ServerboundPlayChat, w)
				stepEnd = time.Now().Add(1500 * time.Millisecond)
			case "walk":
				b.mu.Lock()
				b.keys = 1
				b.sprint = len(cur) > 2 && cur[2] == "sprint"
				b.mu.Unlock()
				if b.sprint {
					b.playerCommand(1)
				}
				stepEnd = time.Now().Add(time.Duration(arg(1) * float64(time.Second)))
			case "square":
				b.mu.Lock()
				b.keys = 1
				b.mu.Unlock()
				square, squareStart = true, time.Now()
				stepEnd = time.Now().Add(time.Duration(arg(1) * float64(time.Second)))
			case "jump": // jump once (keeps walking if a walk key is held: jump walk)
				b.mu.Lock()
				b.jumpQueued = true
				if len(cur) > 1 && cur[1] == "walk" {
					b.keys = 1
				}
				b.mu.Unlock()
				stepEnd = time.Now().Add(800 * time.Millisecond)
			case "look":
				b.mu.Lock()
				b.yaw = float32(arg(1))
				b.mu.Unlock()
			case "respawn":
				w := &wire.Writer{}
				w.VarInt(0)
				b.write(v777.ServerboundPlayClientCommand, w)
				stepEnd = time.Now().Add(2 * time.Second)
			case "expect":
				want := strings.Join(cur[1:], " ")
				b.mu.Lock()
				found := false
				for _, c := range b.chat {
					if strings.Contains(c, want) {
						found = true
					}
				}
				b.mu.Unlock()
				log.Printf("EXPECT %q: %v", want, found)
			case "block": // block X Y Z: log the Java state the bot has there
				b.mu.Lock()
				x, y, z := int(arg(1)), int(arg(2)), int(arg(3))
				st := -1
				if col := b.chunks[[2]int32{int32(x >> 4), int32(z >> 4)}]; col != nil {
					if i := (y - b.minY) >> 4; i >= 0 && i < len(col.Sections) {
						st = int(col.Sections[i].Blocks[jchunk.BlockIndex(x&15, y&15, z&15)])
					}
				}
				log.Printf("BLOCK %d %d %d = state %d", x, y, z, st)
				b.mu.Unlock()
			case "light":
				b.mu.Lock()
				sky, blk := b.lightAt(int(math.Floor(b.pos[0])), int(math.Floor(b.pos[1]))+1, int(math.Floor(b.pos[2])))
				log.Printf("LIGHT at head: sky %d block %d", sky, blk)
				b.mu.Unlock()
			case "tree": // tree NAME: the paths of a command in the commands packet (commands.go)
				b.mu.Lock()
				b.printTree(strings.Join(cur[1:], " "))
				b.mu.Unlock()
			case "complete": // complete TEXT: ask for suggestions at the end of /TEXT
				text := strings.Join(cur[1:], " ")
				text = strings.TrimSuffix(text, "_") // a trailing _ stands for the space after the last word
				b.complete(text)
				stepEnd = time.Now().Add(1500 * time.Millisecond)
			case "quit":
				return nil
			default:
				if f, ok := extraSteps[cur[0]]; ok { // inv.go
					stepEnd = time.Now().Add(f(b, cur))
				} else if d, ok := b.entityStep(cur); ok { // entities.go
					stepEnd = time.Now().Add(d)
				}
			}
		}
		b.mu.Lock()
		if square {
			b.yaw = float32(int(time.Since(squareStart).Seconds()/2)%4) * 90
		}
		b.mu.Unlock()
		for _, h := range tickHooks { // useitem.go
			h(b)
		}
		b.physicsTick()
	}
}

func (b *bot) playerCommand(action int32) {
	w := &wire.Writer{}
	w.VarInt(1)
	w.VarInt(action)
	w.VarInt(0) // data (riding jump strength)
	b.write(v777.ServerboundPlayPlayerCommand, w)
}

// solid reports whether the block at x, y, z is one the bot collides with.
func (b *bot) solid(x, y, z int) bool {
	col := b.chunks[[2]int32{int32(x >> 4), int32(z >> 4)}]
	if col == nil {
		return true // not loaded: treat as a wall (and the floor), like a client waiting
	}
	i := (y - b.minY) >> 4
	if i < 0 || i >= len(col.Sections) {
		return false
	}
	st := col.Sections[i].Blocks[jchunk.BlockIndex(x&15, y&15, z&15)]
	if solidState == nil {
		return st != 0
	}
	return int(st) < len(solidState) && solidState[st]
}

func (b *bot) collides(p [3]float64) bool {
	const hw = 0.3
	for _, dx := range []float64{-hw, hw - 1e-7} {
		for _, dz := range []float64{-hw, hw - 1e-7} {
			for _, dy := range []float64{0, 1, 1.79} {
				if b.solid(int(math.Floor(p[0]+dx)), int(math.Floor(p[1]+dy)), int(math.Floor(p[2]+dz))) {
					return true
				}
			}
		}
	}
	return false
}

// physicsTick moves the bot one tick (vanilla's travel for walking on normal blocks and falling)
// and sends what a client sends: player_input, the move, client_tick_end.
func (b *bot) physicsTick() {
	if ridingTick != nil && ridingTick(b) { // ride.go
		return
	}
	if jsimTick != nil && jsimTick(b) { // jmove.go
		return
	}
	if sleepTick != nil && sleepTick(b) { // sleep.go
		return
	}
	b.mu.Lock()
	if b.dead {
		b.mu.Unlock()
		b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
		return
	}
	cx, cz := int32(math.Floor(b.pos[0]))>>4, int32(math.Floor(b.pos[2]))>>4
	if b.chunks[[2]int32{cx, cz}] == nil {
		b.mu.Unlock()
		b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
		return
	}
	if !b.loadedSent {
		b.loadedSent = true
		b.write(v777.ServerboundPlayPlayerLoaded, &wire.Writer{})
	}
	var fwd float64
	if b.keys&1 != 0 {
		fwd = 0.98
	}
	speed := 0.1
	if b.sprint {
		speed *= 1.3
	}
	yawR := float64(b.yaw) * math.Pi / 180
	sin, cos := math.Sin(yawR), math.Cos(yawR)
	accel := 0.02
	if b.sprint {
		accel = 0.026
	}
	if b.onGround {
		accel = speed // 0.6 slipperiness: 0.216 / 0.6^3 = 1
	}
	jumped := false
	wasOnGround := b.onGround
	if b.jumpQueued && b.onGround {
		b.vel[1] = 0.42
		if b.sprint {
			b.vel[0] += -sin * 0.2
			b.vel[2] += cos * 0.2
		}
		jumped = true
	}
	b.jumpQueued = false
	b.vel[0] += -sin * fwd * accel
	b.vel[2] += cos * fwd * accel
	// Move with collisions, y first.
	np := b.pos
	np[1] += b.vel[1]
	b.onGround = false
	if b.collides(np) {
		if b.vel[1] < 0 {
			np[1] = math.Floor(np[1]) + 1
			for b.collides(np) && np[1] < b.pos[1]+1 {
				np[1]++
			}
			b.onGround = true
		} else {
			np[1] = b.pos[1]
		}
		b.vel[1] = 0
	}
	b.hColl = false
	for _, axis := range []int{0, 2} {
		t := np
		t[axis] += b.vel[axis]
		if b.collides(t) {
			b.vel[axis] = 0
			b.hColl = true
		} else {
			np = t
		}
	}
	if !b.onGround {
		// Still standing on something without moving down this tick?
		below := np
		below[1] -= 0.001
		if b.vel[1] == 0 && b.collides(below) {
			b.onGround = true
		}
	}
	b.pos = np
	b.vel[1] = (b.vel[1] - 0.08) * 0.98
	if b.onGround && b.vel[1] < 0 {
		b.vel[1] = 0 // resting
	}
	f := 0.91
	if wasOnGround {
		f = 0.6 * 0.91
	}
	b.vel[0] *= f
	b.vel[2] *= f
	keys := b.keys
	if jumped {
		keys |= 0x10
	}
	if b.sprint {
		keys |= 0x40
	}
	pos, yaw, pitch, onGround, hColl := b.pos, b.yaw, b.pitch, b.onGround, b.hColl
	moved := pos != b.lastSent
	b.sinceSent++
	if moved || b.sinceSent >= 20 {
		b.lastSent, b.sinceSent = pos, 0
	}
	b.mu.Unlock()

	w := &wire.Writer{}
	w.Byte(keys)
	b.write(v777.ServerboundPlayPlayerInput, w)
	w = &wire.Writer{}
	w.Float64(pos[0])
	w.Float64(pos[1])
	w.Float64(pos[2])
	w.Float32(yaw)
	w.Float32(pitch)
	var fl byte
	if onGround {
		fl |= 1
	}
	if hColl {
		fl |= 2
	}
	w.Byte(fl)
	b.write(v777.ServerboundPlayMovePlayerPosRot, w)
	b.write(v777.ServerboundPlayClientTickEnd, &wire.Writer{})
}

func (b *bot) readLoop() error {
	dec := jchunk.NewDecoder(jchunk.VanillaBlockStates, jchunk.VanillaBiomes)
	for {
		id, body, err := b.c.ReadPacket()
		if err != nil {
			return err
		}
		r := wire.NewReader(body)
		b.mu.Lock()
		err = b.handle(id, r, body, dec)
		b.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (b *bot) handle(id int32, r *wire.Reader, body []byte, dec *jchunk.Decoder) error {
	b.entityPacket(id, body)        // entities.go
	for _, h := range packetHooks { // useitem.go
		h(b, id, body)
	}
	switch id {
	case v777.ClientboundPlayLogin, v777.ClientboundPlayRespawn:
		if id == v777.ClientboundPlayLogin {
			r.Int32()
			r.Bool()
			for n := r.VarInt(); n > 0; n-- {
				r.String(256)
			}
			r.VarInt()
			r.VarInt()
			r.VarInt()
			r.Bool()
			r.Bool()
			r.Bool()
		}
		r.VarInt()
		dim := r.String(256)
		b.minY = 0
		if dim == "minecraft:overworld" {
			b.minY = -64
		}
		r.Int64()
		mode := r.VarInt()
		if id == v777.ClientboundPlayRespawn {
			b.respawns++
			b.dead = false
			b.loadedSent = false
			log.Printf("respawn packet: dim %s mode %d", dim, mode)
		} else {
			log.Printf("login packet: dim %s mode %d", dim, mode)
		}
	case v777.ClientboundPlayPlayerPosition:
		tp := r.VarInt()
		x, y, z := r.Float64(), r.Float64(), r.Float64()
		dx, dy, dz := r.Float64(), r.Float64(), r.Float64()
		yaw, pitch := r.Float32(), r.Float32()
		flags := r.Int32()
		if flags&7 == 7 {
			b.relTP++
			off := math.Sqrt(x*x + y*y + z*z)
			b.relOffsets = append(b.relOffsets, off)
			log.Printf("RELATIVE teleport %d by %.4f %.4f %.4f (%.4f) flags %#x", tp, x, y, z, off, flags)
			b.pos[0] += x
			b.pos[1] += y
			b.pos[2] += z
			for i, d := range [3]float64{dx, dy, dz} { // relative velocity: added, as Java does
				if flags&(32<<i) != 0 {
					b.vel[i] += d
				} else {
					b.vel[i] = d
				}
			}
		} else {
			b.absTP++
			log.Printf("ABSOLUTE teleport %d to %.3f %.3f %.3f yaw %.1f flags %#x (delta %.2f %.2f %.2f)", tp, x, y, z, yaw, flags, dx, dy, dz)
			b.pos = [3]float64{x, y, z}
			if flags&relYaw == 0 {
				b.yaw, b.pitch = yaw, pitch
			}
			if flags&(1<<5) == 0 {
				b.vel = [3]float64{}
			}
		}
		b.spawned = true
		go func() {
			w := &wire.Writer{}
			w.VarInt(tp)
			b.write(v777.ServerboundPlayAcceptTeleportation, w)
		}()
	case v777.ClientboundPlayKeepAlive:
		go b.write(v777.ServerboundPlayKeepAlive, &wire.Writer{B: append([]byte(nil), body...)})
	case v777.ClientboundPlayPing:
		go b.write(v777.ServerboundPlayPong, &wire.Writer{B: append([]byte(nil), body...)})
	case v777.ClientboundPlayLevelChunkWithLight:
		col := &jchunk.Column{}
		if err := dec.Decode(r, col); err != nil {
			return fmt.Errorf("chunk decode: %w", err)
		}
		for i := range col.SkyLight {
			col.SkyLight[i].Data = append([]byte(nil), col.SkyLight[i].Data...)
		}
		for i := range col.BlockLight {
			col.BlockLight[i].Data = append([]byte(nil), col.BlockLight[i].Data...)
		}
		for i := range col.BlockEntities { // the decoder keeps them in the packet buffer
			col.BlockEntities[i].NBT = append([]byte(nil), col.BlockEntities[i].NBT...)
		}
		key := [2]int32{col.X, col.Z}
		b.chunks[key] = col
		h := fnv.New64a()
		for _, s := range col.Sections {
			for _, v := range s.Blocks {
				h.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16)})
			}
		}
		b.hashes[key] = h.Sum64()
		b.chunkCount++
		if b.chunkCount%50 == 1 {
			log.Printf("chunk %d at %d %d (%d bytes, %d sections, %d block entities)", b.chunkCount, col.X, col.Z, len(body), len(col.Sections), len(col.BlockEntities))
		}
	case v777.ClientboundPlayForgetLevelChunk:
		v := r.Int64()
		delete(b.chunks, [2]int32{int32(uint32(v)), int32(uint32(uint64(v) >> 32))})
		b.stats["forget"]++
	case v777.ClientboundPlayChunkBatchFinished:
		go func() {
			w := &wire.Writer{}
			w.Float32(20)
			b.write(v777.ServerboundPlayChunkBatchReceived, w)
		}()
	case v777.ClientboundPlayBlockUpdate:
		x, y, z := r.Position()
		st := r.VarInt()
		b.setBlock(x, y, z, uint32(st))
		b.stats["blockUpdate"]++
	case v777.ClientboundPlaySectionBlocksUpdate:
		sp := r.Int64()
		sx, sy, sz := int(sp>>42), int(sp<<44>>44), int(sp<<22>>42)
		for n := r.VarInt(); n > 0; n-- {
			e := r.VarLong()
			st := uint32(e >> 12)
			b.setBlock(sx*16+int(e>>8&15), sy*16+int(e&15), sz*16+int(e>>4&15), st)
		}
		b.stats["sectionUpdate"]++
	case v777.ClientboundPlaySetHealth:
		h := r.Float32()
		food := r.VarInt()
		if h != b.health || food != b.food {
			log.Printf("HEALTH %.1f food %d", h, food)
		}
		b.food = food
		b.health = h
		if h <= 0 && !b.dead {
			b.dead = true
			log.Printf("DIED")
		}
	case v777.ClientboundPlayPlayerCombatKill:
		r.VarInt()
		log.Printf("death screen: %s", printable(body[1:]))
	case v777.ClientboundPlaySystemChat:
		msg := printable(body)
		b.chat = append(b.chat, msg)
		log.Printf("CHAT %s", msg)
	case v777.ClientboundPlayDisconnect:
		b.kicked = printable(body)
		return fmt.Errorf("disconnected: %s", b.kicked)
	case v777.ClientboundPlaySetTitleText, v777.ClientboundPlaySetSubtitleText, v777.ClientboundPlaySetActionBarText:
		log.Printf("TITLE %#x %s", id, printable(body))
	case v777.ClientboundPlayCustomPayload:
		ch := r.String(256)
		log.Printf("PLUGIN %s %q", ch, printable(body[min(len(body), len(ch)+1):]))
	case v777.ClientboundPlayUpdateAttributes:
		r.VarInt()
		var parts []string
		for n := r.VarInt(); n > 0 && r.Err == nil; n-- {
			aid := r.VarInt()
			v := r.Float64()
			for m := r.VarInt(); m > 0; m-- {
				r.UUID()
				r.Float64()
				r.Byte()
			}
			parts = append(parts, fmt.Sprintf("%d=%g", aid, v))
		}
		log.Printf("attributes %s", strings.Join(parts, " "))
	case v777.ClientboundPlayGameEvent:
		ev := r.Byte()
		v := r.Float32()
		if ev != 13 {
			log.Printf("game event %d %.2f", ev, v)
		}
	case v777.ClientboundPlayPlayerAbilities:
		log.Printf("abilities flags %#x fly %.3f", r.Byte(), r.Float32())
	case v777.ClientboundPlaySetEntityMotion:
		if r.VarInt() == 1 {
			x, y, z := r.LpVec3()
			b.vel = [3]float64{x, y, z}
			log.Printf("KNOCKBACK %.3f %.3f %.3f", x, y, z)
		}
	case v777.ClientboundPlayCommands:
		b.stats["commands"]++
		b.onCommands(body) // commands.go
	case v777.ClientboundPlayCommandSuggestions:
		b.onSuggestions(r)
	case v777.ClientboundPlayTransfer:
		host := r.String(32767)
		log.Printf("TRANSFER %s:%d", host, r.VarInt())
	case v777.ClientboundPlayStoreCookie:
		log.Printf("STORE COOKIE %s", r.String(32767))
	default:
		for _, h := range extraHandlers { // inv.go
			if h(b, id, r) {
				return nil
			}
		}
		b.stats[fmt.Sprintf("%#x", id)]++
	}
	return nil
}

const relYaw = 1 << 3

// tickHooks run every client tick before the movement; packetHooks see every packet (b.mu held).
var (
	tickHooks   []func(b *bot)
	packetHooks []func(b *bot, id int32, body []byte)
)

// lightAt is the sky and block light the bot got for a block (-1: not sent).
func (b *bot) lightAt(x, y, z int) (int, int) {
	col := b.chunks[[2]int32{int32(x >> 4), int32(z >> 4)}]
	if col == nil {
		return -1, -1
	}
	i := (y-b.minY)>>4 + 1
	get := func(ls []jchunk.Light) int {
		if i < 0 || i >= len(ls) || ls[i].State != jchunk.LightData {
			return -1
		}
		idx := jchunk.BlockIndex(x&15, y&15, z&15)
		return int(ls[i].Data[idx>>1]>>(4*(idx&1))) & 15
	}
	return get(col.SkyLight), get(col.BlockLight)
}

func (b *bot) setBlock(x, y, z int, st uint32) {
	col := b.chunks[[2]int32{int32(x >> 4), int32(z >> 4)}]
	if col == nil {
		return
	}
	i := (y - b.minY) >> 4
	if i >= 0 && i < len(col.Sections) {
		col.Sections[i].Blocks[jchunk.BlockIndex(x&15, y&15, z&15)] = st
	}
}

func (b *bot) summary() {
	b.mu.Lock()
	defer b.mu.Unlock()
	maxOff := 0.0
	for _, o := range b.relOffsets {
		maxOff = max(maxOff, o)
	}
	log.Printf("SUMMARY absolute teleports %d, relative teleports %d (max %.3f), respawns %d, chunks %d (distinct %d), health %.1f, pos %.3f %.3f %.3f, other %v",
		b.absTP, b.relTP, maxOff, b.respawns, b.chunkCount, len(b.hashes), b.health, b.pos[0], b.pos[1], b.pos[2], b.stats)
	b.entitySummary() // entities.go
	if *hashFile != "" {
		keys := make([][2]int32, 0, len(b.hashes))
		for k := range b.hashes {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i][0] != keys[j][0] {
				return keys[i][0] < keys[j][0]
			}
			return keys[i][1] < keys[j][1]
		})
		var sb strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&sb, "%d %d %016x\n", k[0], k[1], b.hashes[k])
		}
		_ = os.WriteFile(*hashFile, []byte(sb.String()), 0o644)
		if *dumpCols != "" {
			var db strings.Builder
			for _, c := range strings.Split(*dumpCols, ";") {
				var x, z int32
				fmt.Sscanf(c, "%d,%d", &x, &z)
				col := b.chunks[[2]int32{x, z}]
				if col == nil {
					continue
				}
				for i, s := range col.Sections {
					for j, v := range s.Blocks {
						fmt.Fprintf(&db, "%d %d %d %d\n", int(x)*16+j&15, b.minY+i*16+j>>8, int(z)*16+j>>4&15, v)
					}
				}
			}
			_ = os.WriteFile(*hashFile+".dump", []byte(db.String()), 0o644)
		}
	}
}

// printable keeps the readable runs of a packet body.
func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 32 && c < 127 {
			sb.WriteByte(c)
		} else if sb.Len() > 0 && !strings.HasSuffix(sb.String(), " ") {
			sb.WriteByte(' ')
		}
	}
	return strings.TrimSpace(sb.String())
}
