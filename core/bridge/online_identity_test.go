package bridge

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// a fixed verified Java profile UUID, as it would come from the session server.
var testJavaUUID = uuid.MustParse("11111111-2222-3333-4444-555555555555")

func TestOnlineIdentityStableAndWellFormed(t *testing.T) {
	o := OnlineIdentity{Secret: "s3cr3t"}
	j := JavaPlayer{Name: "Alice", UUID: testJavaUUID}
	a, err := o.BedrockLogin(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := o.BedrockLogin(context.Background(), j)
	if a.IdentityData.XUID != b.IdentityData.XUID || a.IdentityData.Identity != b.IdentityData.Identity {
		t.Fatalf("identity not stable: %+v vs %+v", a.IdentityData, b.IdentityData)
	}
	// well-formed XUID: 16 digits, "2535" prefix (real-XUID key space), all decimal.
	x := a.IdentityData.XUID
	if len(x) != 16 || x[:4] != "2535" {
		t.Fatalf("XUID not well-formed: %q", x)
	}
	for _, c := range x {
		if c < '0' || c > '9' {
			t.Fatalf("XUID has a non-digit: %q", x)
		}
	}
	// SelfSignedID must equal uuid5(namespace, xuid): what xuidforward checks.
	want := uuid.NewSHA1(defaultNamespace, []byte(x)).String()
	if a.selfSignedID != want {
		t.Fatalf("selfSignedID = %q, want uuid5(ns,xuid)=%q", a.selfSignedID, want)
	}
	if a.trustSecret != "s3cr3t" {
		t.Fatalf("trustSecret not carried: %q", a.trustSecret)
	}
	// Identity (BDS UUID) is derived, not the Java UUID itself.
	if a.IdentityData.Identity == testJavaUUID.String() {
		t.Fatal("BDS UUID must be derived, not the raw Java UUID")
	}
}

func TestOnlineIdentityDiffersPerAccount(t *testing.T) {
	o := OnlineIdentity{}
	a, _ := o.BedrockLogin(context.Background(), JavaPlayer{Name: "Alice", UUID: testJavaUUID})
	other := uuid.MustParse("99999999-8888-7777-6666-555555555555")
	b, _ := o.BedrockLogin(context.Background(), JavaPlayer{Name: "Alice", UUID: other})
	if a.IdentityData.XUID == b.IdentityData.XUID {
		t.Fatal("two different Java accounts must get different XUIDs even with the same name")
	}
}

func TestOfflineIdentityUnchanged(t *testing.T) {
	a, err := OfflineIdentity{}.BedrockLogin(context.Background(), JavaPlayer{Name: "Bob", UUID: testJavaUUID})
	if err != nil {
		t.Fatal(err)
	}
	if a.IdentityData.XUID != "" {
		t.Fatalf("offline login must have an empty XUID, got %q", a.IdentityData.XUID)
	}
	if a.trustSecret != "" || a.selfSignedID != "" {
		t.Fatal("offline login must carry no trust secret or forced SelfSignedID")
	}
	want := uuid.NewSHA1(uuid.NameSpaceOID, []byte("javabridge:"+testJavaUUID.String())).String()
	if a.IdentityData.Identity != want {
		t.Fatalf("offline UUID derivation changed: %q want %q", a.IdentityData.Identity, want)
	}
}
