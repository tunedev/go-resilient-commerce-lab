// Package idempotency makes a retried request safe to repeat.
package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Hash returns a canonical digest of a JSON request body. Decoding and
// re-encoding normalises key order and whitespace, so a client that
// reserialises an identical request does not trip a false conflict.
func Hash(body []byte) (string, error) {
	var canonical any
	if err := json.Unmarshal(body, &canonical); err != nil {
		return "", fmt.Errorf("idempotency: canonicalise request: %w", err)
	}

	normalised, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("idempotency: re-encode request: %w", err)
	}

	sum := sha256.Sum256(normalised)
	return hex.EncodeToString(sum[:]), nil
}
