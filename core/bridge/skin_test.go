package bridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"

	jserver "github.com/df-mc/dragonfly/server/java/protocol/server"
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

func TestSteveClientData(t *testing.T) {
	var cd login.ClientData
	steveSkin().apply(&cd)
	data, err := base64.StdEncoding.DecodeString(cd.SkinData)
	if err != nil || len(data) != 64*64*4 || cd.SkinImageWidth != 64 || cd.SkinImageHeight != 64 {
		t.Fatalf("skin data %d bytes, %dx%d", len(data), cd.SkinImageWidth, cd.SkinImageHeight)
	}
	geo, _ := base64.StdEncoding.DecodeString(cd.SkinGeometry)
	var g struct {
		Geometry []struct {
			Description struct {
				Identifier string `json:"identifier"`
			} `json:"description"`
		} `json:"minecraft:geometry"`
	}
	if err := json.Unmarshal(geo, &g); err != nil || len(g.Geometry) != 1 || g.Geometry[0].Description.Identifier != "geometry.humanoid.custom" {
		t.Errorf("geometry %v %s", err, geo[:min(80, len(geo))])
	}
	patch, _ := base64.StdEncoding.DecodeString(cd.SkinResourcePatch)
	if string(patch) != `{"geometry":{"default":"geometry.humanoid.custom"}}` {
		t.Errorf("resource patch %s", patch)
	}
	if cd.ArmSize != "wide" || cd.SkinID != standardSkinPack+".Steve" {
		t.Errorf("arm %s id %s", cd.ArmSize, cd.SkinID)
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
