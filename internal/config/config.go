package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	JournalMaxBytes                                                                                 uint64
	Env, DatabaseURL, AdminDatabaseURL, RabbitURL, RedisURL, MasterKey                              string
	HTTPAddr, SMTPAddr, SMTPHostname, CertFile, KeyFile, PostfixAddr, PostfixLog, NodeID, PublicURL string
	Concurrency                                                                                     int
	AllowUnverified, AllowPlainSMTP, AllowPrivateWebhooks                                           bool
	SMTPTimeout                                                                                     time.Duration
	ControlDir, JournalDir                                                                          string
	WireLimit                                                                                       int
}

func val(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func Load() (Config, error) {
	c := Config{Env: val("APP_ENV", "development"), DatabaseURL: os.Getenv("DATABASE_URL"), AdminDatabaseURL: os.Getenv("ADMIN_DATABASE_URL"), RabbitURL: os.Getenv("RABBITMQ_URL"), RedisURL: os.Getenv("REDIS_URL"), MasterKey: os.Getenv("MASTER_KEY"), HTTPAddr: val("HTTP_ADDR", ":8080"), SMTPAddr: val("SMTP_ADDR", ":1587"), SMTPHostname: val("SMTP_HOSTNAME", "mail.example.test"), CertFile: os.Getenv("SMTP_TLS_CERT"), KeyFile: os.Getenv("SMTP_TLS_KEY"), PostfixAddr: val("POSTFIX_ADDR", "postfix:25"), PostfixLog: val("POSTFIX_LOG", "/var/log/postfix/mail.log"), NodeID: val("MTA_NODE_ID", "mta-1"), PublicURL: val("PUBLIC_URL", "http://localhost:18080"), Concurrency: 4, AllowUnverified: os.Getenv("ALLOW_UNVERIFIED_DOMAINS") == "true", AllowPlainSMTP: os.Getenv("ALLOW_PLAIN_SMTP") == "true", AllowPrivateWebhooks: os.Getenv("ALLOW_PRIVATE_WEBHOOKS") == "true", SMTPTimeout: 30 * time.Second}
	if n := os.Getenv("WORKER_CONCURRENCY"); n != "" {
		var e error
		c.Concurrency, e = strconv.Atoi(n)
		if e != nil || c.Concurrency < 1 || c.Concurrency > 16 {
			return c, fmt.Errorf("invalid WORKER_CONCURRENCY")
		}
	}
	c.ControlDir = os.Getenv("CONTROL_DIR")
	c.JournalDir = os.Getenv("JOURNAL_DIR")
	c.JournalMaxBytes = 10 * 1024 * 1024 * 1024
	if raw := os.Getenv("JOURNAL_MAX_BYTES"); raw != "" {
		n, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || n < 64*1024*1024 || n > 1024*1024*1024*1024 {
			return c, fmt.Errorf("JOURNAL_MAX_BYTES must be 64 MiB..1 TiB")
		}
		c.JournalMaxBytes = n
	}
	c.WireLimit = 10 * 1024 * 1024
	if n := os.Getenv("MAX_WIRE_BYTES"); n != "" {
		wire, err := strconv.Atoi(n)
		if err != nil || wire < 64*1024 || wire > 10*1024*1024 {
			return c, fmt.Errorf("MAX_WIRE_BYTES must be 64 KiB..10 MiB")
		}
		c.WireLimit = wire
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if c.NodeID == "" {
		return c, fmt.Errorf("MTA_NODE_ID is required")
	}
	if c.Env != "development" && c.Env != "test" && c.Env != "production" {
		return c, fmt.Errorf("invalid APP_ENV")
	}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return c, fmt.Errorf("invalid PUBLIC_URL")
	}
	if c.Env == "production" {
		if c.ControlDir == "" || c.JournalDir == "" {
			return c, fmt.Errorf("production requires CONTROL_DIR and JOURNAL_DIR")
		}
		if c.AllowUnverified || c.AllowPlainSMTP || c.AllowPrivateWebhooks {
			return c, fmt.Errorf("development bypasses are prohibited in production")
		}
		if u.Scheme != "https" {
			return c, fmt.Errorf("production PUBLIC_URL requires https")
		}
	}
	return c, nil
}
