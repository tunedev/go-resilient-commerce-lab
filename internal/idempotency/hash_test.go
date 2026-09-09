package idempotency_test

import (
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
)

func TestHashIgnoresKeyOrderAndWhitespace(t *testing.T) {
	a, err := idempotency.Hash([]byte(`{"customer_id":"c1","payment_method_id":"pm_ok"}`))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	b, err := idempotency.Hash([]byte("{\n  \"payment_method_id\": \"pm_ok\",\n  \"customer_id\": \"c1\"\n}"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	if a != b {
		t.Errorf("reordered and reformatted bodies hashed differently:\n%s\n%s", a, b)
	}
}

func TestHashDistinguishesDifferentValues(t *testing.T) {
	a, _ := idempotency.Hash([]byte(`{"customer_id":"c1"}`))
	b, _ := idempotency.Hash([]byte(`{"customer_id":"c2"}`))

	if a == b {
		t.Error("different bodies hashed identically")
	}
}

func TestHashRejectsMalformedJSON(t *testing.T) {
	if _, err := idempotency.Hash([]byte(`{`)); err == nil {
		t.Error("Hash accepted malformed JSON")
	}
}
