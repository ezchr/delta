package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jserver "github.com/ezchr/go-mcjava/server"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

func TestJavaSkinLegacy(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	red := color.NRGBA{R: 255, A: 255}
	src.SetNRGBA(44, 20, red)                        // right arm, front, top left
	src.SetNRGBA(8, 8, color.NRGBA{G: 200, A: 0})    // face pixel without alpha
	src.SetNRGBA(40, 8, color.NRGBA{B: 255, A: 255}) // hat layer, fully opaque: cleared
	for y := 0; y < 32; y++ {
		for x := 32; x < 64; x++ {
			if src.NRGBAAt(x, y).A == 0 {
				src.SetNRGBA(x, y, color.NRGBA{B: 9, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	pix, err := javaSkinRGBA(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(pix) != 64*64*4 {
		t.Fatalf("got %d bytes", len(pix))
	}
	at := func(x, y int) color.NRGBA {
		o := (y*64 + x) * 4
		return color.NRGBA{pix[o], pix[o+1], pix[o+2], pix[o+3]}
	}
	// copyRect(44, 20, -8, 32, 4, 12, mirrored): (44, 20) lands on (36+3, 52).
	if c := at(39, 52); c != red {
		t.Errorf("left arm pixel %v, want the right arm's mirrored", c)
	}
	if c := at(8, 8); c.A != 255 || c.G != 200 {
		t.Errorf("face pixel %v, want opaque", c)
	}
	if c := at(40, 8); c.A != 0 {
		t.Errorf("solid legacy hat layer kept: %v", c)
	}
	// copyRect(4, 20, 16, 32, 4, 12, mirrored): the right leg's front column 4 lands on the left
	// leg's front column 23.
	if c, want := at(23, 52), at(4, 20); c != want || c.A != 255 {
		t.Errorf("left leg pixel %v, want the right leg's %v", c, want)
	}
	// The second layers a legacy skin has no room for (jacket, sleeves, trousers) are transparent.
	for _, xy := range [][2]int{{20, 36}, {44, 36}, {4, 36}, {52, 52}, {4, 52}} {
		if c := at(xy[0], xy[1]); c.A != 0 {
			t.Errorf("second layer at %v not transparent: %v", xy, c)
		}
	}
}

func TestJavaTexturesProperty(t *testing.T) {
	v, _ := json.Marshal(map[string]any{"textures": map[string]any{
		"SKIN": map[string]any{"url": "http://textures.minecraft.net/texture/abcdef0123456789", "metadata": map[string]any{"model": "slim"}},
		"CAPE": map[string]any{"url": "http://textures.minecraft.net/texture/0123456789abcdef"},
	}})
	p := &Player{name: "x", profile: jserver.Profile{Properties: []jserver.Property{{Name: "textures", Value: base64.StdEncoding.EncodeToString(v)}}}}
	tex, err := p.javaTextures(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if tex.Textures.Skin.Metadata.Model != "slim" || tex.Textures.Cape.URL == "" {
		t.Errorf("got %+v", tex)
	}
	if textureHash.FindStringSubmatch(tex.Textures.Skin.URL) == nil {
		t.Errorf("texture URL not accepted")
	}
	if textureHash.MatchString("http://example.com/texture/abcdef0123456789") {
		t.Errorf("other hosts must be refused")
	}
}

// skinPNG encodes img as a PNG.
func skinPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pixelAt reads a pixel of 64-wide RGBA bytes.
func pixelAt(pix []byte, x, y int) color.NRGBA {
	o := (y*64 + x) * 4
	return color.NRGBA{pix[o], pix[o+1], pix[o+2], pix[o+3]}
}

func TestJavaSkinClassic(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	face := color.NRGBA{R: 10, G: 20, B: 30, A: 255}
	src.SetNRGBA(8, 8, face)                            // head front
	src.SetNRGBA(20, 20, color.NRGBA{R: 1, G: 2, B: 3}) // body, transparent in the file: made opaque
	src.SetNRGBA(40, 8, color.NRGBA{R: 50, A: 128})     // hat layer, half transparent: kept
	src.SetNRGBA(36, 52, color.NRGBA{G: 77, A: 255})    // left arm (64x64 only): kept, not mirrored
	src.SetNRGBA(20, 36, color.NRGBA{B: 99, A: 0})      // jacket layer, transparent: kept
	pix, err := javaSkinRGBA(skinPNG(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if len(pix) != 64*64*4 {
		t.Fatalf("got %d bytes", len(pix))
	}
	for _, c := range []struct {
		x, y int
		want color.NRGBA
	}{
		{8, 8, face},
		{20, 20, color.NRGBA{R: 1, G: 2, B: 3, A: 255}},
		{40, 8, color.NRGBA{R: 50, A: 128}},
		{36, 52, color.NRGBA{G: 77, A: 255}},
		{20, 36, color.NRGBA{}},
	} {
		got := pixelAt(pix, c.x, c.y)
		if c.want.A == 0 {
			got.R, got.G, got.B = 0, 0, 0 // the colour of a transparent pixel does not matter
		}
		if got != c.want {
			t.Errorf("(%d, %d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestJavaSkinBadSize(t *testing.T) {
	for _, r := range []image.Rectangle{image.Rect(0, 0, 128, 128), image.Rect(0, 0, 64, 48), image.Rect(0, 0, 32, 32)} {
		if _, err := javaSkinRGBA(skinPNG(t, image.NewNRGBA(r))); err == nil {
			t.Errorf("%v skin accepted", r.Size())
		}
	}
	if _, err := javaSkinRGBA([]byte("not a png")); err == nil {
		t.Errorf("garbage accepted")
	}
}

// testClientData is client data with the fields gophertunnel's Validate needs besides the skin.
func testClientData() login.ClientData {
	return login.ClientData{DeviceOS: protocol.DeviceWin32, GameVersion: protocol.CurrentVersion, LanguageCode: "en_US",
		ServerAddress: "127.0.0.1:19132", DeviceID: "0123456789abcdef0123456789abcdef"}
}

// checkTrustedSkin checks the skin client data, and what BDS needs to trust the skin: premium,
// no geometry data (skin.go).
func checkTrustedSkin(t *testing.T, cd login.ClientData, geo, arms string) {
	t.Helper()
	if err := cd.Validate(); err != nil {
		t.Errorf("client data not valid: %v", err)
	}
	if !cd.PremiumSkin || cd.PersonaSkin || cd.SkinGeometry != "" {
		t.Errorf("premium %v persona %v geometry %q: BDS would not trust the skin", cd.PremiumSkin, cd.PersonaSkin, cd.SkinGeometry)
	}
	patch, _ := base64.StdEncoding.DecodeString(cd.SkinResourcePatch)
	if want := `{"geometry":{"default":"` + geo + `"}}`; string(patch) != want {
		t.Errorf("resource patch %s, want %s", patch, want)
	}
	if v, _ := base64.StdEncoding.DecodeString(cd.SkinGeometryVersion); string(v) != protocol.CurrentVersion {
		t.Errorf("geometry engine version %q", v)
	}
	data, err := base64.StdEncoding.DecodeString(cd.SkinData)
	if err != nil || len(data) != 64*64*4 || cd.SkinImageWidth != 64 || cd.SkinImageHeight != 64 {
		t.Errorf("skin data %d bytes, %dx%d", len(data), cd.SkinImageWidth, cd.SkinImageHeight)
	}
	if cd.ArmSize != arms {
		t.Errorf("arm size %q, want %q", cd.ArmSize, arms)
	}
}

func TestSteveClientData(t *testing.T) {
	cd := testClientData()
	steveSkin().apply(&cd)
	checkTrustedSkin(t, cd, "geometry.humanoid.custom", "wide")
	if cd.SkinID != standardSkinPack+".Steve" || cd.CapeData != "" {
		t.Errorf("id %s cape %d", cd.SkinID, len(cd.CapeData))
	}
}

// testTextures is a textures property value with a skin (model "slim" or "") and maybe a cape.
func testTextures(skinHash, model, capeHash string) *texturesValue {
	t := new(texturesValue)
	t.Textures.Skin.URL = "http://textures.minecraft.net/texture/" + skinHash
	t.Textures.Skin.Metadata.Model = model
	if capeHash != "" {
		t.Textures.Cape.URL = "http://textures.minecraft.net/texture/" + capeHash
	}
	return t
}

func TestJavaSkinClientData(t *testing.T) {
	dir := t.TempDir()
	skin := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	skin.SetNRGBA(8, 8, color.NRGBA{R: 200, A: 255})
	cape := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	cape.SetNRGBA(1, 1, color.NRGBA{B: 200, A: 255})
	legacy := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	legacy.SetNRGBA(8, 8, color.NRGBA{G: 200, A: 255})
	// The texture cache is read first, so nothing is downloaded.
	for hash, img := range map[string]image.Image{"aaaa0000aaaa0000": skin, "cccc0000cccc0000": cape, "bbbb0000bbbb0000": legacy} {
		if err := os.WriteFile(filepath.Join(dir, hash+".png"), skinPNG(t, img), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, hash, model, cape, geo, arms string
		face                               color.NRGBA
	}{
		{"classic", "aaaa0000aaaa0000", "", "", "geometry.humanoid.custom", "wide", color.NRGBA{R: 200, A: 255}},
		{"slim with cape", "aaaa0000aaaa0000", "slim", "cccc0000cccc0000", "geometry.humanoid.customSlim", "slim", color.NRGBA{R: 200, A: 255}},
		{"legacy 64x32", "bbbb0000bbbb0000", "", "", "geometry.humanoid.custom", "wide", color.NRGBA{G: 200, A: 255}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := skinFromTextures(context.Background(), dir, testTextures(c.hash, c.model, c.cape))
			if err != nil {
				t.Fatal(err)
			}
			cd := testClientData()
			s.apply(&cd)
			checkTrustedSkin(t, cd, c.geo, c.arms)
			data, _ := base64.StdEncoding.DecodeString(cd.SkinData)
			if got := pixelAt(data, 8, 8); got != c.face {
				t.Errorf("face pixel %v, want %v", got, c.face)
			}
			if !strings.HasPrefix(cd.SkinID, customSkinPack+".") {
				t.Errorf("skin id %s", cd.SkinID)
			}
			if c.cape == "" {
				if cd.CapeData != "" || cd.CapeID != "" {
					t.Errorf("cape without one: %q", cd.CapeID)
				}
				return
			}
			capeData, _ := base64.StdEncoding.DecodeString(cd.CapeData)
			if len(capeData) != 64*32*4 || cd.CapeImageWidth != 64 || cd.CapeImageHeight != 32 || cd.CapeID == "" {
				t.Fatalf("cape %d bytes %dx%d id %q", len(capeData), cd.CapeImageWidth, cd.CapeImageHeight, cd.CapeID)
			}
			if o := (1*64 + 1) * 4; capeData[o+2] != 200 || capeData[o+3] != 255 {
				t.Errorf("cape pixel %v", capeData[o:o+4])
			}
		})
	}
}

func TestClientDataHelpers(t *testing.T) {
	for in, want := range map[string]string{"en_us": "en_US", "es_mx": "es_MX", "lol_us": "en_US", "": "en_US"} {
		if got := bedrockLanguage(in); got != want {
			t.Errorf("bedrockLanguage(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"play.example.com": "play.example.com:19132", "tcp:127.0.0.1:19139": "127.0.0.1:19139",
		"1.2.3.4:19133": "1.2.3.4:19133"} {
		if got := loginServerAddress(in); got != want {
			t.Errorf("loginServerAddress(%q) = %q, want %q", in, got, want)
		}
	}
	d := newDevice()
	if len(d.DeviceID) != 32 || login.DeviceID(d.DeviceID).Format() != login.DeviceIDFormatLowerHexString || d.DeviceModel == "" {
		t.Errorf("device %+v", d)
	}
}

func TestBridgedSkin(t *testing.T) {
	id := uuid.New()
	signed := jserver.Property{Name: "textures", Value: "dGV4", Signature: "c2ln"}
	p := &Player{login: &BedrockLogin{IdentityData: login.IdentityData{Identity: id.String()}},
		profile: jserver.Profile{Properties: []jserver.Property{signed}}}
	stop := p.shareTextures()
	if got := bridgedSkin(id); len(got) != 1 || got[0] != signed {
		t.Fatalf("bridged skin %v", got)
	}
	stop()
	if got := bridgedSkin(id); got != nil {
		t.Errorf("still shared after leaving: %v", got)
	}
	// Unsigned textures (an offline login) are not shared.
	p.profile.Properties[0].Signature = ""
	defer p.shareTextures()()
	if got := bridgedSkin(id); got != nil {
		t.Errorf("unsigned textures shared: %v", got)
	}
}
