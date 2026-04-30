// Package webhook verifies confish webhook signatures.
//
// Always pass the raw, unparsed request body to Verify — re-serializing parsed JSON
// alters byte order and breaks signature comparison.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"time"
)

// DefaultTolerance rejects signatures with timestamps older than this. Pass
// a different value to Verify via Options.Tolerance, or 0 to disable.
const DefaultTolerance = 5 * time.Minute

// Payload is the canonical webhook event shape. Use json.Unmarshal on the raw body
// after a successful Verify call to populate it.
type Payload struct {
	Event       string         `json:"event"`
	Timestamp   string         `json:"timestamp"`
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
// outside the tolerance window, or doesn't match the body.
var ErrInvalidSignature = errors.New("confish: invalid webhook signature")

// Verify checks that the provided signature matches the body using the given secret.
// Returns nil on success or ErrInvalidSignature on any failure.
func Verify(body []byte, signature, secret string, opts Options) error {
	if signature == "" || secret == "" {
		return ErrInvalidSignature
	}

	match := sigRe.FindStringSubmatch(signature)
	if len(match) != 3 {
		return ErrInvalidSignature
	}

	ts, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return ErrInvalidSignature
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
			return ErrInvalidSignature
		}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(match[1]))
	mac.Write([]byte(":"))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(match[2]), []byte(expected)) {
		return ErrInvalidSignature
	}
	return nil
}
