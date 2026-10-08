package client

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func TestURLQueryAndFragmentAreRejected(t *testing.T) {
	for _, s := range []string{"https://example.test/?a=b", "https://example.test/#x", "https://example.test/?"} {
		if _, e := New(s, "key"); e == nil {
			t.Fatal(s)
		}
	}
}
func TestWebhookSignatureAndFreshness(t *testing.T) {
	secret := []byte("a-long-test-secret")
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"event":"delivered"}`)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	signature := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if e := VerifyWebhook(secret, ts, signature, body, now, 5*time.Minute); e != nil {
		t.Fatal(e)
	}
	if VerifyWebhook(secret, ts, signature, []byte("changed"), now, 5*time.Minute) == nil {
		t.Fatal("forged payload")
	}
	if VerifyWebhook(secret, ts, signature, body, now.Add(time.Hour), 5*time.Minute) == nil {
		t.Fatal("stale payload")
	}
}
