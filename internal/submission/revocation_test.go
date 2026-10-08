package submission

import (
	"context"
	"net"
	stdsmtp "net/smtp"
	"os"
	"testing"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
)

func TestLiveSessionRejectsRevokedKey(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("integration database required")
	}
	ctx := context.Background()
	root, e := store.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = root.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	app, e := store.Open(ctx, os.Getenv("TEST_APP_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer app.Close()
	id, key, e := root.CreateTenant(ctx, "revoke-"+domain.ID(), 100, 100)
	if e != nil {
		t.Fatal(e)
	}
	name := id + ".example.test"
	e = app.AddDomain(ctx, id, domain.Domain{ID: domain.ID(), Name: name, Selector: "test", Token: "test", PublicKey: "test", EncryptedKey: []byte("test")})
	if e != nil {
		t.Fatal(e)
	}
	srv, e := New(&application.Service{Repo: app, AllowUnverified: true}, "127.0.0.1:0", "localhost", "", "", true)
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", srv.Addr)
	if e != nil {
		t.Fatal(e)
	}
	defer srv.Close()
	go srv.Serve(l)
	// PlainAuth is restricted to localhost or TLS by the standard client.
	conn, e := net.Dial("tcp", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	c2, e := stdsmtp.NewClient(conn, "localhost")
	if e != nil {
		t.Fatal(e)
	}
	defer c2.Close()
	if e = c2.Auth(stdsmtp.PlainAuth("", "apikey", key, "localhost")); e != nil {
		t.Fatal(e)
	}
	if e = root.RevokeKey(ctx, key); e != nil {
		t.Fatal(e)
	}
	if e = c2.Mail("a@" + name); e != nil {
		t.Fatal(e)
	}
	if e = c2.Rcpt("b@example.net"); e != nil {
		t.Fatal(e)
	}
	w, e := c2.Data()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.Write([]byte("From: a@" + name + "\r\nSubject: test\r\n\r\nhello")); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(); e == nil {
		t.Fatal("revoked key accepted in existing SMTP session")
	}
	var n int
	if e = root.Pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE tenant_id=$1", id).Scan(&n); e != nil || n != 0 {
		t.Fatalf("accepted=%d error=%v", n, e)
	}
}
