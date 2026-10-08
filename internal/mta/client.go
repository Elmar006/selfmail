package mta

import (
	"context"
	"errors"
	"net"
	"regexp"
	"time"

	smtp "github.com/emersion/go-smtp"
)

type Result struct {
	QueueID            string
	Unknown, Permanent bool
	Err                error
}
type Client struct {
	Address, Hostname string
	Timeout           time.Duration
}

func (c *Client) LocalHostname() string { return c.Hostname }

var queueRE = regexp.MustCompile(`(?i)queued as ([A-Za-z0-9]+)`)

func (c *Client) Submit(ctx context.Context, from, to, envid string, raw []byte) Result {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", c.Address)
	if e != nil {
		return failure(e, false)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(timeout))
	cl := smtp.NewClient(conn)
	cl.CommandTimeout = timeout
	cl.SubmissionTimeout = timeout
	defer cl.Close()
	if e = cl.Hello(c.Hostname); e != nil {
		return failure(e, false)
	}
	opts := &smtp.MailOptions{}
	if ok, _ := cl.Extension("DSN"); ok {
		opts.EnvelopeID = envid
		opts.Return = smtp.DSNReturnHeaders
	}
	if e = cl.Mail(from, opts); e != nil {
		return failure(e, false)
	}
	if e = cl.Rcpt(to, nil); e != nil {
		return failure(e, false)
	}
	w, e := cl.Data()
	if e != nil {
		return failure(e, false)
	}
	conn.SetDeadline(time.Now().Add(timeout))
	// A write without the final dot cannot be accepted. Close may write it partially:
	// only explicit SMTP rejection is definitive after that point.
	if _, e = w.Write(raw); e != nil {
		return failure(e, false)
	}
	res, e := w.CloseWithResponse()
	if e != nil {
		return failure(e, true)
	}
	r := Result{}
	if m := queueRE.FindStringSubmatch(res.StatusText); len(m) == 2 {
		r.QueueID = m[1]
	}
	return r
}
func failure(e error, afterCommit bool) Result {
	r := Result{Err: e, Unknown: afterCommit}
	var se *smtp.SMTPError
	if errors.As(e, &se) {
		r.Permanent = se.Code >= 500 && se.Code < 600
		r.Unknown = false
	}
	return r
}
