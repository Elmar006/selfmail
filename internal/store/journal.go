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
func (s *Store) recordLogs(node, file string, lines []LogLine) error {
	if s.Journal == nil {
		return nil
	}
	var records []journal.Record
	for _, line := range lines {
		if line.Parsed {
			records = append(records, journal.Record{ID: journal.ID("log", node, file, strconv.FormatInt(line.Offset, 10)), Kind: "log", Node: node, File: file, Offset: line.Offset, Next: line.Next, Queue: line.QueueID, Message: line.MessageID, Attempt: line.AttemptID, Status: line.Status, Recipient: line.Recipient, DSN: line.DSN, Diagnostic: line.Diagnostic})
		}
	}
	if len(records) == 0 {
		return nil
	}
	return s.Journal.PutMany(records)
}
func (s *Store) CheckJournal(ctx context.Context) error {
	if s.Journal == nil {
		return nil
	}
	_, e := s.Journal.Digest(ctx)
	return e
}
