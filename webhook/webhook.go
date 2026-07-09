// Package webhook verifies confish webhook signatures.
//
// Always pass the raw, unparsed request body to Verify — re-serializing parsed JSON
// alters byte order and breaks signature comparison.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// DefaultTolerance rejects signatures with timestamps older than this. Pass
// a different value to Verify via Options.Tolerance, or 0 to disable.
const DefaultTolerance = 5 * time.Minute

// Payload is the canonical webhook event shape, returned by Verify on success.
type Payload struct {
	Event       string `json:"event"`
	Timestamp   string `json:"timestamp"`
	Application struct {
		Name string `json:"name"`
	} `json:"application"`
	Environment struct {
		Name  string `json:"name"`
		EnvID string `json:"env_id"`
		URL   string `json:"url"`
	} `json:"environment"`
	Changes []string       `json:"changes,omitempty"`
	Values  map[string]any `json:"values,omitempty"`
}

// Options configure Verify.
type Options struct {
	// Tolerance is the maximum allowed clock skew between sender and receiver.
	// Default: DefaultTolerance. Pass -1 to disable timestamp checking.
	Tolerance time.Duration
	// Now overrides the current time. Useful for testing.
	Now func() time.Time
}

var sigRe = regexp.MustCompile(`^ts=(\d+);sig=([a-fA-F0-9]+)$`)

// ErrInvalidSignature is returned when the signature header is missing, malformed,
// or doesn't match the body.
var ErrInvalidSignature = errors.New("confish: invalid webhook signature")

// ErrTimestampOutsideTolerance is returned when the signature's timestamp falls
// outside the tolerance window — the request may be a replayed capture.
var ErrTimestampOutsideTolerance = errors.New("confish: webhook timestamp outside tolerance")

// Verify checks that the provided signature matches the body using the given secret
// and, on success, returns the parsed payload. On failure it returns a zero Payload
// and either ErrInvalidSignature or ErrTimestampOutsideTolerance.
func Verify(body []byte, signature, secret string, opts Options) (Payload, error) {
	if signature == "" || secret == "" {
		return Payload{}, ErrInvalidSignature
	}

	match := sigRe.FindStringSubmatch(signature)
	if len(match) != 3 {
		return Payload{}, ErrInvalidSignature
	}

	ts, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return Payload{}, ErrInvalidSignature
	}

	// HMAC before tolerance, so ErrTimestampOutsideTolerance always means
	// "authentic but stale" - a forged payload must never report a
	// timestamp problem.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(match[1]))
	mac.Write([]byte(":"))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(match[2]), []byte(expected)) {
		return Payload{}, ErrInvalidSignature
	}

	tolerance := opts.Tolerance
	if tolerance == 0 {
		tolerance = DefaultTolerance
	}
	if tolerance > 0 {
		now := time.Now
		if opts.Now != nil {
			now = opts.Now
		}
		drift := now().Unix() - ts
		if drift < 0 {
			drift = -drift
		}
		if time.Duration(drift)*time.Second > tolerance {
			return Payload{}, ErrTimestampOutsideTolerance
		}
	}

	var payload Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Payload{}, fmt.Errorf("confish: decode webhook payload: %w", err)
	}
	return payload, nil
}
