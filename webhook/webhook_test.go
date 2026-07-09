package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

func sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d:", ts)))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyReturnsParsedPayload(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"event":"environment.updated","environment":{"name":"Production","env_id":"env_1"},"changes":["site_name"]}`)
	ts := int64(1_700_000_000)
	header := fmt.Sprintf("ts=%d;sig=%s", ts, sign(secret, ts, body))

	payload, err := Verify(body, header, secret, Options{
		Now: func() time.Time { return time.Unix(ts, 0) },
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if payload.Event != "environment.updated" {
		t.Fatalf("event: %q", payload.Event)
	}
	if payload.Environment.EnvID != "env_1" {
		t.Fatalf("env_id: %q", payload.Environment.EnvID)
	}
	if len(payload.Changes) != 1 || payload.Changes[0] != "site_name" {
		t.Fatalf("changes: %+v", payload.Changes)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	body := []byte(`{}`)
	ts := int64(1_700_000_000)
	header := fmt.Sprintf("ts=%d;sig=%s", ts, sign("other", ts, body))

	_, err := Verify(body, header, "whsec_test", Options{
		Now: func() time.Time { return time.Unix(ts, 0) },
	})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	secret := "whsec_test"
	original := []byte(`{"a":1}`)
	tampered := []byte(`{"a":2}`)
	ts := int64(1_700_000_000)
	header := fmt.Sprintf("ts=%d;sig=%s", ts, sign(secret, ts, original))

	_, err := Verify(tampered, header, secret, Options{
		Now: func() time.Time { return time.Unix(ts, 0) },
	})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestVerifyRejectsStaleTimestamp(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{}`)
	ts := int64(1_700_000_000)
	header := fmt.Sprintf("ts=%d;sig=%s", ts, sign(secret, ts, body))

	_, err := Verify(body, header, secret, Options{
		Tolerance: 5 * time.Minute,
		Now:       func() time.Time { return time.Unix(ts+600, 0) },
	})
	if !errors.Is(err, ErrTimestampOutsideTolerance) {
		t.Fatalf("expected ErrTimestampOutsideTolerance, got %v", err)
	}
	if errors.Is(err, ErrInvalidSignature) {
		t.Fatal("stale timestamp should be distinguishable from an invalid signature")
	}
}

func TestVerifyAcceptsStaleTimestampWhenToleranceDisabled(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"event":"environment.updated"}`)
	ts := int64(1_700_000_000)
	header := fmt.Sprintf("ts=%d;sig=%s", ts, sign(secret, ts, body))

	payload, err := Verify(body, header, secret, Options{
		Tolerance: -1,
		Now:       func() time.Time { return time.Unix(ts+99_999, 0) },
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if payload.Event != "environment.updated" {
		t.Fatalf("event: %q", payload.Event)
	}
}

func TestVerifyRejectsMalformedHeaders(t *testing.T) {
	cases := []string{"", "garbage", "ts=abc;sig=def", "ts=1;sig="}
	for _, h := range cases {
		if _, err := Verify([]byte(`{}`), h, "secret", Options{}); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("header %q: expected ErrInvalidSignature, got %v", h, err)
		}
	}
}
