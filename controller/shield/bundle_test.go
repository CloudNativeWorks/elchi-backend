package shield

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestCheckBundle_RejectsPathOwnedByAnotherPolicy(t *testing.T) {
	r := req(file("api.yaml", []byte("x"), "", ""))
	others := []ShieldPolicy{policy("other", file("api.yaml", []byte("y"), "", ""))}
	err := checkBundle(&r, others, "")
	if err == nil {
		t.Fatal("a path already owned by another policy must be rejected")
	}
	if !errors.Is(err, ErrPolicyPathConflict) {
		t.Fatalf("a path collision must carry ErrPolicyPathConflict (→ 409), got: %v", err)
	}
}

func TestCheckBundle_AllowsWhenUnique(t *testing.T) {
	r := req(file("api.yaml", []byte("x"), "", ""))
	others := []ShieldPolicy{policy("other", file("feeds/x.json", []byte("y"), "", ""))}
	if err := checkBundle(&r, others, ""); err != nil {
		t.Fatalf("unique paths under size cap must pass: %v", err)
	}
}

func TestCheckBundle_RejectsOverInlineSizeCap(t *testing.T) {
	// req is just under cap on its own, but the other policy's inline content
	// tips the merged total over maxInlineBundleBytes.
	big := make([]byte, maxInlineBundleBytes-10)
	r := req(file("api.yaml", big, "", ""))
	others := []ShieldPolicy{policy("other", file("feeds/x.json", make([]byte, 100), "", ""))}
	err := checkBundle(&r, others, "")
	if err == nil {
		t.Fatal("merged inline content over the cap must be rejected")
	}
	if !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("an over-size bundle must carry ErrPolicyInvalid (→ 400), got: %v", err)
	}
}

func TestCheckBundle_DownloadRefsDoNotCountTowardSizeCap(t *testing.T) {
	good64 := "0000000000000000000000000000000000000000000000000000000000000000"
	// A huge download-ref carries no inline bytes, so it never trips the cap.
	r := req(file("geo/big.mmdb", nil, "https://example.com/big.mmdb", good64))
	others := []ShieldPolicy{policy("other", file("api.yaml", make([]byte, 10), "", ""))}
	if err := checkBundle(&r, others, ""); err != nil {
		t.Fatalf("download refs must not count toward the inline cap: %v", err)
	}
}

func TestCheckBundle_ExcludeIDSkipsSelf(t *testing.T) {
	// An update re-submits its own paths; sizing/collision must skip the policy
	// being updated (matched by excludeID) so it doesn't collide with itself.
	self := policy("self", file("api.yaml", []byte("x"), "", ""))
	self.ID = primitive.NewObjectID()
	r := req(file("api.yaml", []byte("x-updated"), "", ""))
	if err := checkBundle(&r, []ShieldPolicy{self}, self.ID.Hex()); err != nil {
		t.Fatalf("update must not collide with the policy it replaces: %v", err)
	}
}
