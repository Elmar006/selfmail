package sink

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	smtp "github.com/emersion/go-smtp"
)

type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	Raw       string    `json:"raw"`
	CreatedAt time.Time `json:"created_at"`
}
type Sink struct {
	mu       sync.Mutex
	messages []Message
	webhooks []json.RawMessage
	receipts []http.Header
	alerts   []json.RawMessage
	bytes    int
	total    int64
	counts   map[string]int
}
type session struct {
	s    *Sink
	from string
	to   []string
}

func (s *Sink) NewSession(*smtp.Conn) (smtp.Session, error)    { return &session{s: s}, nil }
func (s *session) Reset()                                      { s.from = ""; s.to = nil }
func (s *session) Logout() error                               { return nil }
func (s *session) Mail(from string, _ *smtp.MailOptions) error { s.from = from; return nil }
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if strings.HasPrefix(to, "reject@") {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "test mailbox does not exist"}
	}
	if strings.HasPrefix(to, "defer@") {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 2, 0}, Message: "test temporary error"}
	}
	s.to = append(s.to, to)
	return nil
}
func (s *session) Data(r io.Reader) error {
	raw, e := io.ReadAll(io.LimitReader(r, 10*1024*1024+1))
	if e != nil {
		return e
	}
	s.s.mu.Lock()
	if len(raw) > 10*1024*1024 {
		s.s.mu.Unlock()
		return &smtp.SMTPError{Code: 552, Message: "sink wire size exceeded"}
	}
	defer s.s.mu.Unlock()
	for len(s.s.messages) > 0 && (len(s.s.messages) >= 500 || s.s.bytes+len(raw) > 16*1024*1024) {
		s.s.bytes -= len(s.s.messages[0].Raw)
		s.s.messages[0] = Message{}
		s.s.messages = s.s.messages[1:]
	}
	s.s.bytes += len(raw)
	s.s.total++
	rawText := string(raw)
	if parsed, e := mail.ReadMessage(strings.NewReader(rawText)); e == nil {
		id := strings.SplitN(strings.Trim(parsed.Header.Get("Message-ID"), "<>"), ".", 2)[0]
		if s.s.counts == nil {
			s.s.counts = map[string]int{}
		}
		if _, ok := s.s.counts[id]; ok || len(s.s.counts) < 10000 {
			s.s.counts[id]++
		}
	}
	s.s.messages = append(s.s.messages, Message{domain.ID(), s.from, append([]string(nil), s.to...), rawText, time.Now().UTC()})
	return nil
}
func (s *Sink) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("GET /counts", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"total": s.total, "by_message": s.counts, "retained_bytes": s.bytes})
	})
	m.HandleFunc("POST /alerts", func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
		if e != nil || len(b) > 65536 || !json.Valid(b) {
			w.WriteHeader(400)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.alerts) >= 100 {
			s.alerts = s.alerts[1:]
		}
		s.alerts = append(s.alerts, b)
		w.WriteHeader(204)
	})
	m.HandleFunc("GET /alerts", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		json.NewEncoder(w).Encode(s.alerts)
	})
	m.HandleFunc("GET /messages", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.messages)
	})
	m.HandleFunc("POST /webhooks", func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if e != nil || !json.Valid(raw) {
			w.WriteHeader(400)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.webhooks) >= 2000 {
			s.webhooks = s.webhooks[1:]
			s.receipts = s.receipts[1:]
		}
		s.webhooks = append(s.webhooks, json.RawMessage(raw))
		s.receipts = append(s.receipts, r.Header.Clone())
		w.WriteHeader(204)
	})
	m.HandleFunc("GET /webhooks", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		raw := make([][]byte, len(s.webhooks))
		for i, b := range s.webhooks {
			raw[i] = []byte(b)
		}
		json.NewEncoder(w).Encode(map[string]any{"payloads": s.webhooks, "raw_payloads": raw, "headers": s.receipts})
	})
	return m
}
func Run(ctx context.Context) error {
	sink := new(Sink)
	server := smtp.NewServer(sink)
	server.Addr = ":1025"
	server.Domain = "sink.example.test"
	server.MaxMessageBytes = 10 * 1024 * 1024
	server.ReadTimeout = 15 * time.Second
	server.WriteTimeout = 15 * time.Second
	httpServer := &http.Server{Addr: ":8025", Handler: sink.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errch := make(chan error, 2)
	go func() { errch <- server.ListenAndServe() }()
	go func() { errch <- httpServer.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case e := <-errch:
		return e
	}
	stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpServer.Shutdown(stop)
	server.Shutdown(stop)
	return nil
}
