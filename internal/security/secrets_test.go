package security

import (
	"encoding/base64"
	"net"
	"testing"
)

func TestVaultIntegrityAndContext(t *testing.T) {
	v, e := NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	data, e := v.Seal([]byte("private-key"), "tenant-A")
	if e != nil {
		t.Fatal(e)
	}
	plain, e := v.Open(data, "tenant-A")
	if e != nil || string(plain) != "private-key" {
		t.Fatal("roundtrip failed")
	}
	if _, e = v.Open(data, "tenant-B"); e == nil {
		t.Fatal("wrong context accepted")
	}
	data[len(data)-1] ^= 1
	if _, e = v.Open(data, "tenant-A"); e == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, e = NewVault("bad"); e == nil {
		t.Fatal("invalid key accepted")
	}
}
func TestSSRFAddressPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.1.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "64:ff9b::a00:1", "198.18.0.1", "0.0.0.0"} {
		if PublicIP(net.ParseIP(ip)) {
			t.Errorf("unsafe address %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !PublicIP(net.ParseIP(ip)) {
			t.Errorf("public address %s", ip)
		}
	}
	for _, u := range []string{"http://example.net/hook", "https://user:pass@example.net", "https://127.0.0.1/hook", "https://example.net/#secret", "file:///etc/passwd"} {
		if ValidateWebhook(u, false) == nil {
			t.Errorf("unsafe URL %s", u)
		}
	}
}
