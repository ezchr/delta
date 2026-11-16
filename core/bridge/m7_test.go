package bridge

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jserver "github.com/ezchr/go-mcjava/server"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// Milestone 7's tests: skins (Geyser lookup, default skin UUIDs), name tag teams, actor
// properties, effect particles, scale.

// sampleGeyserSkin is a response of GeyserMC's skin API (GET /v2/skin/<xuid>) for a player who
// joined a Floodgate server: textures uploaded to a Mojang account, Mojang-signed.
const sampleGeyserSkin = `{"value": "ewogICJ0aW1lc3RhbXAiIDogMTYyMTk3ODExNzA0MywKICAicHJvZmlsZUlkIiA6ICJkYmNlZjMyZjI5ZDc0Y2UzOTUzOWMwYjBhMTE1YjZiZCIsCiAgInByb2ZpbGVOYW1lIiA6ICJyYW1waXJlIiwKICAic2lnbmF0dXJlUmVxdWlyZWQiIDogdHJ1ZSwKICAidGV4dHVyZXMiIDogewogICAgIlNLSU4iIDogewogICAgICAidXJsIiA6ICJodHRwOi8vdGV4dHVyZXMubWluZWNyYWZ0Lm5ldC90ZXh0dXJlLzZiYWYzZTdmM2MzYTUwZTJlNDdlZTAyNGI0ZTI3MTE5MTBjYmY5NjQzZjY1Njg2ZTU4ZTNmYjFlOGI3ZmY1NGEiCiAgICB9CiAgfQp9", "signature": "A6j7n8lSt+XdrEP3Zq82RXAcsKIW0SnFEkv+Y0sejKoBj17nIj99cSWG5jxUq00EsYVokFO84odTiQ6LuOvHPemmCd286czLKdq9dnbwqIFbvCBeojcDzoPVQQ7JfByHF7MF0TChdkXAzDrP7Vcjy/8jzshaxNwJ4fITIJ4Hyin7HCyl+DyhXgdzlQSer07dSF81jmwndnuZQJdWu3OSYv4I5iAmlQoboWxybK1csz7j0gdGOQ/EvSVz+rtqzgnPkNuzNm0ZVehuZA+jOKCFPTjSPBOSvgE3+05zi2Rr94bFYHf1/CncZmPv9ByoGC0S3ycD306FIwJh3Nfc5k99yD8nSicOc05w1GyBQ59Xtsm0c9PduE6MZAND/G6iVp/2PKdGy7+QnWazBpSCIXY0QH2y2Jbx6ppAzaq/viR45N4aPc0KTtgIAK6phvlLwuakPBtaACEeUmvonhhQKzbErhZzBSpQ60v4IPfsaSJ5iVACnoMjNSQk48/MCGtMOcqJnjD9uzu3hJbyhMET0t7AJkNc2uXXdnzc7bO9Mw4lLQ+w2/jZFDqmliBK8HkYyByp8YIdxUjwi7/k3BOWQvBfc33N0yYal834iUMeBiVWgoguxD7QyGBj6F/cyQPh/4zWkfaHaYXI70BxRg3SWLLFtF3Q4e+hkUQW72eLuOiQ5ho=", "hash": "ea281fa927e12705b269e05ded20ed34b8fcbd1a526fbccfad9c3448310003ec", "is_steve": true, "texture_id": "6baf3e7f3c3a50e2e47ee024b4e2711910cbf9643f65686e58e3fb1e8b7ff54a", "last_update": 1790821342880}`

// mojangPropertyKeys are Mojang's profile property keys (api.minecraftservices.com/publickeys,
// profilePropertyKeys): what the Java client checks textures signatures with.
var mojangPropertyKeys = []string{
	"MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEAylB4B6m5lz7jwrcFz6Fd/fnfUhcvlxsTSn5kIK/2aGG1C3kMy4VjhwlxF6BFUSnfxhNswPjh3ZitkBxEAFY25uzkJFRwHwVA9mdwjashXILtR6OqdLXXFVyUPIURLOSWqGNBtb08EN5fMnG8iFLgEJIBMxs9BvF3s3/FhuHyPKiVTZmXY0WY4ZyYqvoKR+XjaTRPPvBsDa4WI2u1zxXMeHlodT3lnCzVvyOYBLXL6CJgByuOxccJ8hnXfF9yY4F0aeL080Jz/3+EBNG8RO4ByhtBf4Ny8NQ6stWsjfeUIvH7bU/4zCYcYOq4WrInXHqS8qruDmIl7P5XXGcabuzQstPf/h2CRAUpP/PlHXcMlvewjmGU6MfDK+lifScNYwjPxRo4nKTGFZf/0aqHCh/EAsQyLKrOIYRE0lDG3bzBh8ogIMLAugsAfBb6M3mqCqKaTMAf/VAjh5FFJnjS+7bE+bZEV0qwax1CEoPPJL1fIQjOS8zj086gjpGRCtSy9+bTPTfTR/SJ+VUB5G2IeCItkNHpJX2ygojFZ9n5Fnj7R9ZnOM+L8nyIjPu3aePvtcrXlyLhH/hvOfIOjPxOlqW+O5QwSFP4OEcyLAUgDdUgyW36Z5mB285uKW/ighzZsOTevVUG2QwDItObIV6i8RCxFbN2oDHyPaO5j1tTaBNyVt8CAwEAAQ==",
	"MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEAra4Y2wu3rWEW7cDTDRRd4IvUD140Y12SaG3k4V3UwT/pDnnX5itOcYiZA0qf4VCpJDp2PifOL+Pr/ph/G9/6ZoIxkBeGENo+S7i9BqizJy9cmZocpyx+RkZaw9+frCGNLuYLrxziNWiXFACJSg2mHACR7+6NkGN8d/16/3PxMnvGSyLT7JKGUgqj1Q3oW7k+NLXR9sw6oRELOcnUvZVa2bcglv8vlcyPqqnBhydLfHI85Z5WnIYZviZ3Bb4dv5Fme726BGOtEY7kz40RfiwjT3xYKYKPJUS3/crPX6eugmWyrWdddKaePrW88bp17Z5NIStlJ5KJJk4coha8O+P7onDqmbHwLqPTeR51njkgZ+DJWT6fz8ku9OWQn6I/FxqN14iYIghDJijmKvEwsI7FJ5X2ttPXEvBYLmpj2j0lQQcUIqH7hkiZ+mCW0GYawJgbAeNAraM9sP+76MyAGITtAsXv1IQmah+7OeDJOToG2Kb1Dl0Va+HiP9MPpcnO7kbn6dqAyhNvRNmHnsUOiEcLhW9Rk7xz87IBV/cGKbUDgxu8cYY0P512DWt5+Jmr8W10FDFdLmkJt1taWxNxApM2CiFPCimk02koyLZDW9nqpWNw6qS/TOYPdz438qEuamtYUJ+u6WhBjK8xAJEAt5k3gDKX+nlTiG3N6se09D62fS8CAwEAAQ==",
}

// TestGeyserSkin looks a skin up the way a tab entry does (skinOf by XUID against a stand-in for
// the API) and checks the profile property the Java client gets: name, value, Mojang signature,
// a texture on textures.minecraft.net, written into the player_info_update.
func TestGeyserSkin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/skin/2535400000000001":
			w.Write([]byte(sampleGeyserSkin))
		case "/v2/skin/2535400000000003":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Write([]byte("{}")) // the API's answer for a player without an uploaded skin
		}
	}))
	defer srv.Close()
	old := geyserSkinURL
	geyserSkinURL = srv.URL + "/v2/skin/"
	defer func() { geyserSkinURL = old }()

	wait := func(xuid string) []jserver.Property {
		sk := skinOf(xuid)
		select {
		case <-sk.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("lookup of %s did not finish", xuid)
		}
		if again := skinOf(xuid); again != sk {
			t.Errorf("lookup of %s not cached", xuid)
		}
		return sk.result()
	}
	props := wait("2535400000000001")
	if len(props) != 1 || props[0].Name != "textures" || props[0].Signature == "" {
		t.Fatalf("props = %+v", props)
	}
	var sample struct{ Value, Signature string }
	_ = json.Unmarshal([]byte(sampleGeyserSkin), &sample)
	if props[0].Value != sample.Value || props[0].Signature != sample.Signature {
		t.Fatal("property is not the API's value and signature")
	}
	// Signed by Mojang (the Java client draws other players' skins only if so).
	sig, err := base64.StdEncoding.DecodeString(props[0].Signature)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(props[0].Value))
	verified := false
	for _, k := range mojangPropertyKeys {
		der, _ := base64.StdEncoding.DecodeString(k)
		pub, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			t.Fatal(err)
		}
		if rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), crypto.SHA1, sum[:], sig) == nil {
			verified = true
		}
	}
	if !verified {
		t.Error("textures signature does not verify with Mojang's keys")
	}
	raw, _ := base64.StdEncoding.DecodeString(props[0].Value)
	var payload struct {
		Textures struct {
			Skin struct{ URL string } `json:"SKIN"`
		} `json:"textures"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || !strings.HasPrefix(payload.Textures.Skin.URL, "http://textures.minecraft.net/texture/") {
		t.Errorf("texture url %q (%v)", payload.Textures.Skin.URL, err)
	}

	// The tab entry carries the property.
	te := &tabEntry{id: uuid.MustParse("84370b75-ad0a-4f26-9e2e-ba10c2fdc08c"), name: "Someone", props: props, listed: true}
	var w wire.Writer
	writeTabAdd(&w, te)
	r := wire.NewReader(w.B)
	if a := r.Byte(); a != tabAdd|tabMode|tabListed|tabLatency|tabDisplay {
		t.Fatalf("actions %#x", a)
	}
	if n := r.VarInt(); n != 1 {
		t.Fatalf("entries %d", n)
	}
	if id := r.UUID(); id != te.id {
		t.Fatalf("uuid %v", id)
	}
	if name := r.String(16); name != "Someone" {
		t.Fatalf("name %q", name)
	}
	if n := r.VarInt(); n != 1 {
		t.Fatalf("properties %d", n)
	}
	if name, value := r.String(64), r.String(32767); name != "textures" || value != sample.Value {
		t.Fatalf("property %q", name)
	}
	if !r.Bool() || r.String(1024) != sample.Signature {
		t.Fatal("signature not written")
	}
	if r.Err != nil {
		t.Fatal(r.Err)
	}

	// No skin uploaded, and a failing API: no property (the default skin).
	for _, x := range []string{"2535400000000002", "2535400000000003"} {
		if props := wait(x); props != nil {
			t.Errorf("%s: props %+v", x, props)
		}
	}
}

// TestDefaultSkinUUID checks the UUIDs that make the Java client draw Steve or Alex.
func TestDefaultSkinUUID(t *testing.T) {
	// java.util.UUID.hashCode of these (jshell).
	for s, want := range map[string]int32{"84370b75-ad0a-4f26-9e2e-ba10c2fdc08c": 1978547919, "f611bc35-8a35-4838-a703-d7d493b9700e": 1218335703} {
		if h := javaUUIDHash(uuid.MustParse(s)); h != want {
			t.Errorf("hash of %s = %d, want %d", s, h, want)
		}
	}
	for i := 0; i < 200; i++ {
		id := uuid.New()
		for _, slim := range []bool{false, true} {
			j := defaultSkinUUID(id, slim)
			want := defaultSkinSteve
			if slim {
				want = defaultSkinAlex
			}
			if got := defaultSkinOf(j); got != want {
				t.Fatalf("%v slim %v: default skin %d", id, slim, got)
			}
			if string(j[:15]) != string(id[:15]) || j.Version() != id.Version() {
				t.Fatalf("%v changed beyond its last byte: %v", id, j)
			}
		}
	}
	slim := protocol.Skin{ArmSize: protocol.ArmSizeSlim, SkinData: []byte{1}}
	wide := protocol.Skin{ArmSize: protocol.ArmSizeWide, SkinData: []byte{1}}
	persona := protocol.Skin{ArmSize: protocol.ArmSizeWide, SkinResourcePatch: []byte(`{"geometry":{"default":"geometry.humanoid.customSlim"}}`)}
	if !skinSlim(&slim) || skinSlim(&wide) || !skinSlim(&persona) {
		t.Error("skinSlim")
	}
}

func TestTeamSplit(t *testing.T) {
	for _, c := range []struct {
		tag, name, prefix, suffix string
		colour                    int32
		ok                        bool
	}{
		{"§c[Owner] §fBot2 §7*", "Bot2", "§c[Owner] §f", "§f §7*", 15, true},
		{"§aBot2", "Bot2", "§a", "", 10, true},
		{"Bot2", "Bot2", "", "", -1, true},
		{"§l§6VIP §r§eBot2", "Bot2", "§l§6VIP §r§e", "", 14, true},
		{"§6[A] §lBot2§r!", "Bot2", "§6[A] §l", "§6§l§r!", 6, true},
		{"§gGold Bot2", "Bot2", "§gGold ", "", 6, true},
		{"AVeryLongBedrockName", "AVeryLongBedrock", "", "Name", -1, true},
		{"§6Line one\n§fBot2", "Bot2", "", "", -1, false},
		{"§e§lBot§r2", "Bot2", "", "", -1, false},
		{"Someone Else", "Bot2", "", "", -1, false},
	} {
		prefix, suffix, colour, ok := teamSplit(c.tag, c.name)
		if ok != c.ok || ok && (prefix != c.prefix || suffix != c.suffix || colour != c.colour) {
			t.Errorf("teamSplit(%q, %q) = %q %q %d %v, want %q %q %d %v", c.tag, c.name, prefix, suffix, colour, ok, c.prefix, c.suffix, c.colour, c.ok)
		}
	}
}

func TestVisibleEffects(t *testing.T) {
	// BDS: invisibility (14) and speed (1) as one value; poison (19) from a beacon (ambient).
	ids, amb := visibleEffects(284)
	if len(ids) != 2 || ids[0] != 14 || ids[1] != 1 || amb[0] || amb[1] {
		t.Errorf("284: %v %v", ids, amb)
	}
	ids, amb = visibleEffects(19<<1 | 1)
	if len(ids) != 1 || ids[0] != 19 || !amb[0] {
		t.Errorf("ambient poison: %v %v", ids, amb)
	}
	if ids, _ := visibleEffects(0); len(ids) != 0 {
		t.Errorf("0: %v", ids)
	}
}

func TestJavaScale(t *testing.T) {
	baby := int64(1) << protocol.EntityDataFlagBaby
	for _, c := range []struct {
		typ   string
		scale float32
		flags int64
		want  float64
	}{
		{"minecraft:cow", 1, 0, 1},
		{"minecraft:cow", 2, 0, 2},
		{"minecraft:cow", 0.5, baby, 1},
		{"minecraft:cow", 1, baby, 2},
		{"minecraft:cat", 0.8, 0, 1},
		{"minecraft:cat", 0.4, baby, 1},
		{"minecraft:turtle", 0.16, baby, 1},
		{"minecraft:wither_skeleton", 1.2, 0, 1},
		{"minecraft:zombie", 0.5, baby, 1},
		{"minecraft:slime", 3, 0, 1},
		{"minecraft:player", 1.5, 0, 1.5},
	} {
		e := &entity{bedrock: c.typ, t: entityTypes[c.typ], flags: c.flags, meta: protocol.EntityMetadata{protocol.EntityDataKeyScale: c.scale}}
		if got := e.javaScale(); got < c.want-1e-6 || got > c.want+1e-6 {
			t.Errorf("%s scale %v flags %#x: %v, want %v", c.typ, c.scale, c.flags, got, c.want)
		}
	}
}

func TestApplyProps(t *testing.T) {
	p := &Player{}
	p.ents = newEntities(p)
	p.ents.props = actorProps{types: map[string][]propDef{"minecraft:cow": {
		{name: "minecraft:climate_variant", kind: propEnum, enum: []string{"temperate", "warm", "cold"}},
		{name: "minecraft:sound_variant", kind: propEnum, enum: []string{"default", "moody"}},
	}, "minecraft:bee": {{name: "minecraft:has_nectar", kind: propBool}}}}
	e := &entity{bedrock: "minecraft:cow"}
	if !p.applyProps(e, protocol.EntityProperties{IntegerProperties: []protocol.IntegerEntityProperty{{Index: 0, Value: 2}, {Index: 1, Value: 1}}}) {
		t.Fatal("no change")
	}
	if e.propEnum("minecraft:climate_variant") != "cold" || soundVariant(e.propEnum("minecraft:sound_variant")) != "moody" {
		t.Errorf("props %v", e.props)
	}
	if p.applyProps(e, protocol.EntityProperties{IntegerProperties: []protocol.IntegerEntityProperty{{Index: 0, Value: 2}}}) {
		t.Error("unchanged value reported as a change")
	}
	b := &entity{bedrock: "minecraft:bee"}
	p.applyProps(b, protocol.EntityProperties{IntegerProperties: []protocol.IntegerEntityProperty{{Index: 0, Value: 1}}})
	if v, ok := b.propBool("minecraft:has_nectar"); !v || !ok {
		t.Errorf("bee %v", b.props)
	}
	if soundVariant("default") != "classic" || soundVariant("mad") != "angry" {
		t.Error("soundVariant")
	}
}
