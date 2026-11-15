package bridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Login client data. The Bedrock login is a Windows 1.26.60 client's, as measured on real players
// (packet logs of real clients): the multiplayer token only, without the legacy
// Mojang chain (gophertunnel's ClientLogin, so no Android Xbox title shows), DeviceOS Win32 with a
// "<model> <manufacturer> (<chassis>)" device model, mouse input, the classic UI, the account's
// real PlayFab id and the Java player's own skin (skin.go). The multiplayer token's Minecraft
// services session already says Windows10 (gophertunnel's default device), so everything a server
// can see agrees; only the Microsoft token (auth.AndroidConfig) is Android, and servers never see
// it. What a real client keeps across sessions (device id, self-signed id, client random id, the
// device model) is kept per account in <token file without .json>.device.json.
// JB_LOGIN=legacy goes back to gophertunnel's login (the chain and its Android fill-in data).

// legacyLogin: JB_LOGIN=legacy.
var legacyLogin = strings.EqualFold(os.Getenv("JB_LOGIN"), "legacy")

// device is what a real client keeps between sessions.
type device struct {
	DeviceID       string `json:"device_id"`
	DeviceModel    string `json:"device_model"`
	MemoryTier     int    `json:"memory_tier"`
	ClientRandomID int64  `json:"client_random_id"`
	SelfSignedID   string `json:"self_signed_id"`
	// PlayFabID is the account's PlayFab id (its master player account, as the multiplayer token's
	// mid claim names it after gophertunnel's PlayFab LoginWithXbox), in lower case as clients send
	// it; XUID is the account it belongs to. Empty until the first online login.
	PlayFabID string `json:"playfab_id,omitempty"`
	XUID      string `json:"xuid,omitempty"`
}

// windowsModels are device models as Windows 1.26.60 clients report them (WMI model, maker,
// chassis), with a memory tier that fits the machine. A new account gets one at random.
var windowsModels = []struct {
	model string
	tier  int
}{
	{"System Product Name ASUS (Unknown)", 4},
	{"B550M DS3H Gigabyte Technology Co., Ltd. (Unknown)", 4},
	{"MS-7C91 Micro-Star International Co., Ltd. (Unknown)", 4},
	{"HP Laptop 15-fd0xxx HP (Notebook)", 3},
	{"Inspiron 15 3520 Dell Inc. (Notebook)", 3},
	{"Nitro AN515-58 Acer (Notebook)", 4},
	{"82XV Lenovo (Notebook)", 3},
}

var deviceMu sync.Mutex

// deviceFile is where the player's device is kept ("" without a data directory).
func (p *Player) deviceFile() string {
	if cfg.DataDir == "" {
		return ""
	}
	dir := filepath.Join(cfg.DataDir, "devices")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, strings.ToLower(p.name)+".json")
}

// device returns the account's device, making (and saving) one the first time.
func (p *Player) device() device {
	deviceMu.Lock()
	defer deviceMu.Unlock()
	file := p.deviceFile()
	var d device
	if file == "" {
		return newDevice()
	}
	if b, err := os.ReadFile(file); err == nil && json.Unmarshal(b, &d) == nil && d.DeviceID != "" {
		return d
	}
	d = newDevice()
	if err := writeJSON(file, d); err != nil {
		p.log.Warn("device file not saved: the next login gets another device", "file", file, "err", err)
	}
	return d
}

// newDevice makes a device the way a fresh Windows install looks: a 32-character lower-hex device
// id, a version 3 self-signed id, a random client id.
func newDevice() device {
	var b [24]byte
	_, _ = rand.Read(b[:])
	m := windowsModels[int(b[16])%len(windowsModels)]
	return device{
		DeviceID:       hex.EncodeToString(b[:16]),
		DeviceModel:    m.model,
		MemoryTier:     m.tier,
		ClientRandomID: int64(binary.LittleEndian.Uint64(b[16:])),
		SelfSignedID:   uuid.NewMD5(uuid.NameSpaceOID, b[:16]).String(),
	}
}

// rememberPlayFab saves the account's PlayFab id from a login's identity.
func (p *Player) rememberPlayFab(id login.IdentityData) {
	pf := strings.ToLower(id.PlayFabID)
	if pf == "" {
		return
	}
	deviceMu.Lock()
	defer deviceMu.Unlock()
	file := p.deviceFile()
	if file == "" {
		return
	}
	var d device
	if b, err := os.ReadFile(file); err != nil || json.Unmarshal(b, &d) != nil || d.DeviceID == "" {
		return
	}
	if d.PlayFabID == pf && d.XUID == id.XUID {
		return
	}
	if d.PlayFabID != "" {
		p.log.Warn("the account's PlayFab id changed (another account in the token file?)", "was", d.PlayFabID, "now", pf, "xuid", id.XUID)
	}
	d.PlayFabID, d.XUID = pf, id.XUID
	if err := writeJSON(file, d); err != nil {
		p.log.Warn("device file not saved", "file", file, "err", err)
	}
}

// writeJSON writes v to file through a temporary file.
func writeJSON(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// clientData is the login client data for a connection to target, as a Windows client sends it.
func (p *Player) clientData(ctx context.Context, target string) login.ClientData {
	dev := p.device()
	cd := login.ClientData{
		DeviceOS:          protocol.DeviceWin32,
		DeviceModel:       dev.DeviceModel,
		DeviceID:          login.DeviceID(dev.DeviceID),
		ClientRandomID:    dev.ClientRandomID,
		SelfSignedID:      dev.SelfSignedID,
		MemoryTier:        dev.MemoryTier,
		CurrentInputMode:  packet.InputModeMouse,
		DefaultInputMode:  packet.InputModeMouse,
		UIProfile:         0, // classic
		GUIScale:          -1,
		GameVersion:       protocol.CurrentVersion,
		LanguageCode:      bedrockLanguage(p.info.Locale),
		MaxViewDistance:   255,
		PlatformType:      0,
		GraphicsMode:      1,
		ServerAddress:     loginServerAddress(target),
		AnimatedImageData: []login.SkinAnimation{},
		PersonaPieces:     []login.PersonaPiece{},
		PieceTintColours:  []login.PersonaPieceTintColour{},
		// CompatibleWithClientSideChunkGen, FilterProfanity, ClientIsEditorCapable, the platform
		// ids, the party and ProfileHash: false/empty, as Windows sends them.
	}
	if p.login != nil && p.login.TokenSource != nil && dev.PlayFabID != "" {
		cd.PlayFabID = dev.PlayFabID // otherwise gophertunnel takes the token's
	}
	s := p.bedrockSkin(ctx)
	s.apply(&cd)
	p.log.Debug("login client data", "device", cd.DeviceModel, "language", cd.LanguageCode, "skin", cd.SkinID,
		"arms", cd.ArmSize, "cape", s.cape != nil, "playfab", cd.PlayFabID)
	return cd
}

// bedrockLanguage is a Java locale ("en_us") as a Bedrock language code ("en_US").
func bedrockLanguage(locale string) string {
	lang, region, ok := strings.Cut(locale, "_")
	if !ok || len(lang) != 2 || len(region) != 2 {
		return "en_US"
	}
	return strings.ToLower(lang) + "_" + strings.ToUpper(region)
}

// loginServerAddress is the address a client puts in its login for target (host:port).
func loginServerAddress(target string) string {
	addr := strings.TrimPrefix(target, "tcp:")
	if !strings.Contains(addr, ":") {
		addr += ":19132"
	}
	return addr
}
