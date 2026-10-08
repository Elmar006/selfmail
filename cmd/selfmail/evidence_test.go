package main

import (
	"encoding/base64"
	"testing"
)

func TestEvidenceRejectsWrongDeploymentKeys(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	other := base64.StdEncoding.EncodeToString([]byte("different-32-byte-master-secret!!"))
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"APP_ENV=production\nMASTER_KEY=" + key + "\n", true},
		{"APP_ENV=production\nMASTER_KEY='" + key + "'\n", true},
		{"APP_ENV=production\nMASTER_KEY=" + other + "\n", false},
		{"APP_ENV=development\nMASTER_KEY=" + key + "\n", false},
		{"APP_ENV=production\n", false},
		{"APP_ENV=production\nMASTER_KEY=" + key + "\nMASTER_KEY=" + key, false},
	} {
		if e := validateEvidenceEnvironment([]byte(tc.raw), key, "production"); (e == nil) != tc.valid {
			t.Fatalf("environment validation valid=%v: %v", tc.valid, e)
		}
	}
}
