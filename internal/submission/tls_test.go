package submission

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
)

type authRepo struct{ application.Repository }

func (authRepo) Authenticate(ctx context.Context, key string) (domain.Principal, error) {
	if key != "valid-secret" {
		return domain.Principal{}, domain.ErrForbidden
	}
	return domain.Principal{TenantID: "7f019929-5e91-4f56-89ac-9e7b0b04037a", Scopes: []string{"messages:write"}}, nil
}
func TestTLSRequiredForSMTPAuthentication(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	server, e := New(&application.Service{Repo: authRepo{}}, "", "localhost", certPath, keyPath, false)
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go server.Serve(listener)
	defer server.Close()
	plain, e := smtp.Dial(listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	if e = plain.Auth(sasl.NewPlainClient("", "apikey", "valid-secret")); e == nil {
		t.Fatal("AUTH accepted without TLS")
	}
	plain.Close()
	roots := x509.NewCertPool()
	cert, _ := x509.ParseCertificate(der)
	roots.AddCert(cert)
	secure, e := smtp.DialStartTLS(listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12})
	if e != nil {
		t.Fatal(e)
	}
	defer secure.Close()
	if e = secure.Auth(sasl.NewPlainClient("", "apikey", "valid-secret")); e != nil {
		t.Fatal(e)
	}
}
func TestSubmissionRequiresCertificate(t *testing.T) {
	if _, e := New(&application.Service{}, ":1587", "mail.example.test", "", "", false); e == nil {
		t.Fatal("missing TLS config accepted")
	}
}
