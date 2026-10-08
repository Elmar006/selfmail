package mta

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func fakeSMTP(t *testing.T, mode string) string {
	t.Helper()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "220 fake ESMTP\r\n")
		r := bufio.NewReader(conn)
		for {
			line, e := r.ReadString('\n')
			if e != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				fmt.Fprint(conn, "250-fake\r\n250 DSN\r\n")
			case strings.HasPrefix(line, "MAIL"):
				fmt.Fprint(conn, "250 sender ok\r\n")
			case strings.HasPrefix(line, "RCPT"):
				if mode == "reject" {
					fmt.Fprint(conn, "550 5.1.1 no mailbox\r\n")
				} else {
					fmt.Fprint(conn, "250 recipient ok\r\n")
				}
			case strings.HasPrefix(line, "DATA"):
				fmt.Fprint(conn, "354 go ahead\r\n")
				for {
					l, e := r.ReadString('\n')
					if e != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
				}
				switch mode {
				case "drop":
					return
				case "temporary":
					fmt.Fprint(conn, "451 4.3.0 retry\r\n")
				default:
					fmt.Fprint(conn, "250 2.0.0 Ok: queued as ABC123\r\n")
				}
			default:
				fmt.Fprint(conn, "250 ok\r\n")
			}
		}
	}()
	return listener.Addr().String()
}
func TestSMTPCommitBoundary(t *testing.T) {
	for _, mode := range []string{"ok", "reject", "drop", "temporary"} {
		t.Run(mode, func(t *testing.T) {
			r := (&Client{Address: fakeSMTP(t, mode), Hostname: "mail.example.test", Timeout: time.Second}).Submit(context.Background(), "from@example.test", "to@example.net", "id", []byte("Subject: test\r\n\r\nbody\r\n"))
			switch mode {
			case "ok":
				if r.Err != nil || r.QueueID != "ABC123" {
					t.Fatalf("%+v", r)
				}
			case "reject":
				if !r.Permanent || r.Unknown || r.Err == nil {
					t.Fatalf("%+v", r)
				}
			case "drop":
				if !r.Unknown || r.Err == nil {
					t.Fatalf("%+v", r)
				}
			case "temporary":
				if r.Permanent || r.Unknown || r.Err == nil {
					t.Fatalf("%+v", r)
				}
			}
		})
	}
}
func TestBounceToken(t *testing.T) {
	id := "7f019929-5e91-4f56-89ac-9e7b0b04037a"
	addr := BounceAddress(id, "bounce.example.test", []byte("secret"))
	parsed, ok := VerifyBounce(addr, "bounce.example.test", []byte("secret"))
	if !ok || parsed != id {
		t.Fatal("token roundtrip")
	}
	if _, ok = VerifyBounce(addr, "bounce.example.test", []byte("other")); ok {
		t.Fatal("forged token accepted")
	}
}
func TestPostfixLogParsing(t *testing.T) {
	id := "7f019929-5e91-4f56-89ac-9e7b0b04037a"
	line := "Oct 7 postfix/cleanup[42]: ABC: message-id=<" + id + "." + id + "@mail.example.test>"
	ev, ok := ParseLog(line)
	if !ok || ev.AttemptID != id {
		t.Fatal("cleanup not parsed")
	}
	ev, ok = ParseLog("Oct 7 postfix/smtp[42]: ABC: to=<a@example.net>, relay=mx[1.1.1.1]:25, delay=1, delays=0/0/0/1, dsn=5.1.1, status=bounced (550 bad recipient)")
	if !ok || ev.Status != "bounced" || ev.DSN != "5.1.1" {
		t.Fatal("delivery not parsed")
	}
	if _, ok = ParseLog("postfix/smtp[1]: no data"); ok {
		t.Fatal("garbage parsed")
	}
}
func TestDSN(t *testing.T) {
	raw := "From: postmaster@example.net\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=dsn\r\n\r\n--dsn\r\nContent-Type: text/plain\r\n\r\nDelivery failed\r\n--dsn\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.example.net\r\n\r\nFinal-Recipient: rfc822; a@example.net\r\nAction: failed\r\nStatus: 5.1.1\r\n\r\n--dsn--\r\n"
	result, e := ParseDSN([]byte(raw))
	if e != nil || len(result) != 1 || result[0].Recipient != "a@example.net" {
		t.Fatalf("%v %v", result, e)
	}
	if _, e = ParseDSN([]byte("Subject: random\r\n\r\nbody")); e == nil {
		t.Fatal("non DSN accepted")
	}
	_ = io.EOF
}
