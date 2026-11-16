package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// The Java player's own skin as their Bedrock skin: 64x64 RGBA, the vanilla geometry
// geometry.humanoid.custom (or customSlim for the slim/Alex model) named in the resource patch,
// the cape when there is one. The skin comes from the profile's textures property (online-mode
// logins and proxies forward it), else from Mojang by name (offline and cracked hybrid logins of a
// premium name). Without a skin: the vanilla Steve. Textures are cached on disk in
// <data dir>/skins, by their content hash (the texture URL's last part); name lookups for
// skinLookupTTL. A lookup never holds a login up for longer than skinTimeout.
//
// Trust: BDS decides itself whether a login's skin is trusted, and Bedrock clients that only show
// trusted skins (a client setting) draw an untrusted one as a default skin picked by the player's
// UUID (Steve, Alex, Ari...). BDS (ServerNetworkHandler, 1.21.3) sets
//
//	trusted = PersonaSkin || (PremiumSkin && isValidSkinGeometry(geometry data))
//
// where isValidSkinGeometry passes empty (null) geometry data or geometry whose hash is on the
// Marketplace list. So the skin is sent as Geyser sends Java skins to Bedrock clients: premium,
// no geometry data (the client takes the named vanilla geometry from its own resources), the
// game version as the geometry engine version. ClientData.TrustedSkin is ignored by BDS.

//go:embed skinassets
var skinAssets embed.FS

const (
	// customSkinPack is the skin id prefix of an imported custom skin (the Standard pack's uuid
	// with its first part aaaaaaaa), followed by "." and a hash of the skin.
	customSkinPack = "aaaaaaaa-7b21-4637-9b63-8ad63622ef01"
	// standardSkinPack is the vanilla Standard skin pack (Steve, Alex...).
	standardSkinPack = "c18e65aa-7b21-4637-9b63-8ad63622ef01"
	// skinLookupTTL is how long a Mojang name lookup is kept.
	skinLookupTTL = 6 * time.Hour
	// skinTimeout bounds the whole skin lookup of a login (the login waits for it).
	skinTimeout = 4 * time.Second
)

var mojangHTTP = &http.Client{Timeout: 3 * time.Second}

// bedrockSkin is a skin ready for the login client data.
type bedrockSkin struct {
	rgba   []byte // 64x64
	slim   bool
	id     string
	colour string
	cape   []byte // 64x32, nil: none
	capeID string
}

// apply puts the skin in the client data.
func (s *bedrockSkin) apply(cd *login.ClientData) {
	geo := "geometry.humanoid.custom"
	cd.ArmSize = "wide"
	if s.slim {
		geo = "geometry.humanoid.customSlim"
		cd.ArmSize = "slim"
	}
	cd.SkinID = s.id
	cd.SkinData = base64.StdEncoding.EncodeToString(s.rgba)
	cd.SkinImageWidth, cd.SkinImageHeight = 64, 64
	cd.SkinResourcePatch = base64.StdEncoding.EncodeToString([]byte(`{"geometry":{"default":"` + geo + `"}}`))
	cd.SkinGeometry = "" // no geometry data: BDS trusts the skin (see above)
	cd.SkinGeometryVersion = base64.StdEncoding.EncodeToString([]byte(protocol.CurrentVersion))
	cd.SkinColour = s.colour
	cd.TrustedSkin = true
	cd.PremiumSkin, cd.PersonaSkin, cd.OverrideSkin = true, false, false
	cd.CapeData, cd.CapeImageWidth, cd.CapeImageHeight, cd.CapeID = "", 0, 0, ""
	if s.cape != nil {
		cd.CapeData = base64.StdEncoding.EncodeToString(s.cape)
		cd.CapeImageWidth, cd.CapeImageHeight = 64, 32
		cd.CapeID = s.capeID
	}
}

// bedrockSkin returns the Java player's skin (the vanilla Steve when there is none).
func (p *Player) bedrockSkin(ctx context.Context) *bedrockSkin {
	ctx, cancel := context.WithTimeout(ctx, skinTimeout)
	defer cancel()
	dir := filepath.Join(os.TempDir(), ProductName+"-skins")
	if cfg.DataDir != "" {
		dir = filepath.Join(cfg.DataDir, "skins")
	}
	tex, err := p.javaTextures(ctx, dir)
	if err != nil {
		p.log.Debug("skin: no Java textures, sending Steve", "err", err)
		return steveSkin()
	}
	s, err := skinFromTextures(ctx, dir, tex)
	if err != nil {
		p.log.Info("skin: Java skin not usable, sending Steve", "url", tex.Textures.Skin.URL, "err", err)
		return steveSkin()
	}
	p.log.Debug("skin: Java skin", "slim", s.slim, "cape", s.cape != nil, "id", s.id)
	return s
}

// texturesValue is the decoded value of a textures profile property.
type texturesValue struct {
	ProfileName string `json:"profileName"`
	Textures    struct {
		Skin struct {
			URL      string `json:"url"`
			Metadata struct {
				Model string `json:"model"`
			} `json:"metadata"`
		} `json:"SKIN"`
		Cape struct {
			URL string `json:"url"`
		} `json:"CAPE"`
	} `json:"textures"`
}

// javaTextures are the player's textures: the profile's property, else Mojang's by name.
func (p *Player) javaTextures(ctx context.Context, dir string) (*texturesValue, error) {
	value := profileProperty(p.profile, "textures")
	if value == "" {
		var err error
		if value, err = lookupTextures(ctx, dir, p.name); err != nil {
			return nil, err
		}
	}
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("textures property: %w", err)
	}
	t := new(texturesValue)
	if err := json.Unmarshal(b, t); err != nil {
		return nil, fmt.Errorf("textures property: %w", err)
	}
	if t.Textures.Skin.URL == "" {
		return nil, errors.New("the profile has no skin (a default skin)")
	}
	return t, nil
}

// validName is a Java name Mojang can have.
var validName = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

// lookupTextures asks Mojang for the textures property of the premium player with this name
// (cached in dir for skinLookupTTL; a stale cache entry is used when Mojang cannot be reached).
func lookupTextures(ctx context.Context, dir, name string) (string, error) {
	if !validName.MatchString(name) {
		return "", errors.New("not a Java name")
	}
	file := filepath.Join(dir, "profile-"+strings.ToLower(name)+".json")
	var cached struct {
		Fetched  int64  `json:"fetched"`
		Textures string `json:"textures"` // "" : no such premium player
	}
	haveCache := false
	if b, err := os.ReadFile(file); err == nil && json.Unmarshal(b, &cached) == nil {
		haveCache = true
		if time.Since(time.Unix(cached.Fetched, 0)) < skinLookupTTL {
			if cached.Textures == "" {
				return "", errors.New("no premium player with this name (cached)")
			}
			return cached.Textures, nil
		}
	}
	textures, err := fetchTextures(ctx, name)
	if err != nil {
		if haveCache && cached.Textures != "" {
			return cached.Textures, nil
		}
		return "", err
	}
	cached.Fetched, cached.Textures = time.Now().Unix(), textures
	_ = os.MkdirAll(dir, 0o755)
	_ = writeJSON(file, cached)
	if textures == "" {
		return "", errors.New("no premium player with this name")
	}
	return textures, nil
}

// fetchTextures gets the textures property of a name from Mojang ("" when there is no such
// premium player).
func fetchTextures(ctx context.Context, name string) (string, error) {
	var id struct {
		ID string `json:"id"`
	}
	status, err := getJSON(ctx, "https://api.mojang.com/users/profiles/minecraft/"+url.PathEscape(name), &id)
	if err != nil {
		return "", err
	}
	if status == http.StatusNoContent || status == http.StatusNotFound || id.ID == "" {
		return "", nil
	}
	var prof struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	}
	if _, err := getJSON(ctx, "https://sessionserver.mojang.com/session/minecraft/profile/"+url.PathEscape(id.ID), &prof); err != nil {
		return "", err
	}
	for _, pr := range prof.Properties {
		if pr.Name == "textures" {
			return pr.Value, nil
		}
	}
	return "", nil
}

// getJSON GETs u and decodes a 200 answer into v. Other 2xx/404 answers return their status.
func getJSON(ctx context.Context, u string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", ProductName)
	resp, err := mojangHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(v)
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound:
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("%s: %s", u, resp.Status)
}

// textureHash is the content hash at the end of a textures.minecraft.net URL.
var textureHash = regexp.MustCompile(`^https?://textures\.minecraft\.net/texture/([0-9a-f]{16,128})$`)

// texturePNG returns a texture image (from the cache in dir, else downloaded and cached).
func texturePNG(ctx context.Context, dir, u string) ([]byte, string, error) {
	m := textureHash.FindStringSubmatch(u)
	if m == nil {
		return nil, "", fmt.Errorf("not a Mojang texture URL: %q", u)
	}
	hash := m[1]
	file := filepath.Join(dir, hash+".png")
	if b, err := os.ReadFile(file); err == nil {
		return b, hash, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://textures.minecraft.net/texture/"+hash, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", ProductName)
	resp, err := mojangHTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("texture: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o755); err == nil {
		tmp := file + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, file)
		}
	}
	return b, hash, nil
}

// skinFromTextures builds the Bedrock skin (and cape) of Java textures.
func skinFromTextures(ctx context.Context, dir string, t *texturesValue) (*bedrockSkin, error) {
	data, _, err := texturePNG(ctx, dir, t.Textures.Skin.URL)
	if err != nil {
		return nil, err
	}
	rgba, err := javaSkinRGBA(data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(rgba)
	s := &bedrockSkin{
		rgba:   rgba,
		slim:   t.Textures.Skin.Metadata.Model == "slim",
		id:     customSkinPack + "." + hex.EncodeToString(sum[:]),
		colour: "#b37b62", // what Windows sends with a custom skin
	}
	if t.Textures.Cape.URL != "" {
		if data, hash, err := texturePNG(ctx, dir, t.Textures.Cape.URL); err == nil {
			if cape, err := capeRGBA(data); err == nil {
				s.cape = cape
				s.capeID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("textures.minecraft.net/"+hash)).String()
			}
		}
	}
	return s, nil
}

// steveSkin is the vanilla Steve of the Standard skin pack, as a client sends it.
func steveSkin() *bedrockSkin {
	data, _ := skinAssets.ReadFile("skinassets/steve.png")
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		panic(ProductName + ": embedded steve.png: " + err.Error())
	}
	return &bedrockSkin{rgba: toNRGBA(img, 64, 64).Pix, id: standardSkinPack + ".Steve", colour: "#0"}
}

// toNRGBA draws img into a new w x h non-premultiplied RGBA image (Bedrock skin bytes).
func toNRGBA(img image.Image, w, h int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

// javaSkinRGBA turns a Java skin PNG into the 64x64 image the Java client draws: a legacy 64x32
// skin gets its left arm and leg from the right ones, and the base layer is opaque
// (SkinTextureDownloader.processLegacySkin).
func javaSkinRGBA(data []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("skin png: %w", err)
	}
	b := img.Bounds()
	if b.Dx() != 64 || (b.Dy() != 64 && b.Dy() != 32) {
		return nil, fmt.Errorf("skin is %dx%d, not 64x64 or 64x32", b.Dx(), b.Dy())
	}
	legacy := b.Dy() == 32
	im := toNRGBA(img, 64, 64)
	if legacy {
		for y := 32; y < 64; y++ {
			for x := 0; x < 64; x++ {
				im.SetNRGBA(x, y, color.NRGBA{})
			}
		}
		for _, c := range [][6]int{
			{4, 16, 16, 32, 4, 4}, {8, 16, 16, 32, 4, 4}, {0, 20, 24, 32, 4, 12}, {4, 20, 16, 32, 4, 12},
			{8, 20, 8, 32, 4, 12}, {12, 20, 16, 32, 4, 12}, {44, 16, -8, 32, 4, 4}, {48, 16, -8, 32, 4, 4},
			{40, 20, 0, 32, 4, 12}, {44, 20, -8, 32, 4, 12}, {48, 20, -16, 32, 4, 12}, {52, 20, -8, 32, 4, 12},
		} {
			copyRectMirrored(im, c[0], c[1], c[2], c[3], c[4], c[5])
		}
	}
	setNoAlpha(im, 0, 0, 32, 16)
	if legacy {
		notchTransparency(im, 32, 0, 64, 32)
	}
	setNoAlpha(im, 0, 16, 64, 32)
	setNoAlpha(im, 16, 48, 48, 64)
	return im.Pix, nil
}

// capeRGBA is a Java cape PNG as a 64x32 Bedrock cape.
func capeRGBA(data []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		return nil, fmt.Errorf("cape is %dx%d", b.Dx(), b.Dy())
	}
	return toNRGBA(img, 64, 32).Pix, nil
}

// copyRectMirrored copies the w x h region at (x, y) to (x+dx, y+dy), mirrored horizontally
// (NativeImage.copyRect with mirrorX).
func copyRectMirrored(im *image.NRGBA, x, y, dx, dy, w, h int) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			im.SetNRGBA(x+dx+w-1-i, y+dy+j, im.NRGBAAt(x+i, y+j))
		}
	}
}

// setNoAlpha makes the region opaque.
func setNoAlpha(im *image.NRGBA, x0, y0, x1, y1 int) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			im.Pix[im.PixOffset(x, y)+3] = 0xff
		}
	}
}

// notchTransparency clears a legacy skin's hat layer when it has no transparent pixel at all
// (old skins filled it with a solid colour).
func notchTransparency(im *image.NRGBA, x0, y0, x1, y1 int) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if im.Pix[im.PixOffset(x, y)+3] < 128 {
				return
			}
		}
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			im.Pix[im.PixOffset(x, y)+3] = 0
		}
	}
}
