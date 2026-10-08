package config

import (
	"testing"
)

func TestProductionRejectsDevelopmentBypasses(t *testing.T) {
	for _, key := range []string{"ALLOW_PLAIN_SMTP", "ALLOW_UNVERIFIED_DOMAINS", "ALLOW_PRIVATE_WEBHOOKS"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://example")
			t.Setenv("APP_ENV", "production")
			t.Setenv("PUBLIC_URL", "https://mail.example.net")
			for _, k := range []string{"ALLOW_PLAIN_SMTP", "ALLOW_UNVERIFIED_DOMAINS", "ALLOW_PRIVATE_WEBHOOKS"} {
				t.Setenv(k, "false")
			}
			t.Setenv(key, "true")
			if _, e := Load(); e == nil {
				t.Fatal("unsafe production configuration accepted")
			}
		})
	}
}
func TestProductionRequiresHTTPS(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("APP_ENV", "production")
	t.Setenv("PUBLIC_URL", "http://mail.example.net")
	if _, e := Load(); e == nil {
		t.Fatal("plaintext production API accepted")
	}
}
