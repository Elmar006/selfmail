package mta

import (
	"testing"
)

func FuzzLogParser(f *testing.F) {
	f.Add("postfix/smtp[1]: ABC: to=<a@example.net>, dsn=2.0.0, status=sent (ok)")
	f.Add("garbage")
	f.Fuzz(func(t *testing.T, s string) { ParseLog(s) })
}
func FuzzDSNParser(f *testing.F) {
	f.Add([]byte("From: a@example.net\r\n\r\nbody"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1024*1024 {
			return
		}
		ParseDSN(b)
	})
}
