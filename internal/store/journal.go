package store

import (
	"context"
	"strconv"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
)

func (s *Store) recordIntent(m domain.Message) error {
	if s.Journal == nil {
		return nil
	}
	return s.Journal.Put(journal.Record{ID: journal.ID("intent", m.AttemptID), Kind: "intent", Tenant: m.TenantID, Message: m.ID, Attempt: m.AttemptID, Batch: m.BatchID, KeyDigest: m.KeyDigest, Fingerprint: m.Fingerprint, From: m.From, Recipient: m.Recipient, Priority: m.Priority, Node: m.NodeID, Created: m.CreatedAt})
}
func (s *Store) recordOutcome(m domain.Message, status, queue, diagnostic string) error {
	if s.Journal == nil {
		return nil
	}
	return s.Journal.Put(journal.Record{ID: journal.ID("outcome", m.AttemptID, status, queue, diagnostic), Kind: "outcome", Tenant: m.TenantID, Message: m.ID, Attempt: m.AttemptID, Node: m.NodeID, Queue: queue, Status: status, Diagnostic: diagnostic})
}
func (s *Store) recordLog(node, file string, offset, next int64, queue, message, attempt, status, recipient, dsn, diagnostic string) error {
	if s.Journal == nil {
		return nil
	}
	return s.Journal.Put(journal.Record{ID: journal.ID("log", node, file, strconv.FormatInt(offset, 10)), Kind: "log", Node: node, File: file, Offset: offset, Next: next, Queue: queue, Message: message, Attempt: attempt, Status: status, Recipient: recipient, DSN: dsn, Diagnostic: diagnostic})
}
func (s *Store) CheckJournal(ctx context.Context) error {
	if s.Journal == nil {
		return nil
	}
	_, e := s.Journal.Digest(ctx)
	return e
}
