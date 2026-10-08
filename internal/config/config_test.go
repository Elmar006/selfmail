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

func TestDestinationRateIsBoundedAndKeepsDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("APP_ENV", "development")
	t.Setenv("DESTINATION_RATE_PER_SECOND", "")
	c, e := Load()
	if e != nil || c.DestinationRate != 20 {
		t.Fatal("default destination policy changed", c.DestinationRate, e)
	}
	for _, rate := range []string{"0", "-1", "1001", "invalid"} {
		t.Setenv("DESTINATION_RATE_PER_SECOND", rate)
		if _, e := Load(); e == nil {
			t.Fatal("unsafe/unbounded destination rate accepted", rate)
		}
	}
	t.Setenv("DESTINATION_RATE_PER_SECOND", "50")
	c, e = Load()
	if e != nil || c.DestinationRate != 50 {
		t.Fatal(c.DestinationRate, e)
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
