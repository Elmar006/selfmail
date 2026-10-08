// Package recovery fences new work during a managed database restore. Its state
// lives outside PostgreSQL and is never restored from the database backup.
package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/security"
)

var ErrHeld = errors.New("recovery hold: acceptance and handoff disabled")

type State struct {
	Instance   string    `json:"instance"`
	Generation uint64    `json:"generation"`
	Held       bool      `json:"held"`
	Reason     string    `json:"reason"`
	Reconciled uint64    `json:"reconciled_generation"`
	Report     string    `json:"report_sha256,omitempty"`
	Updated    time.Time `json:"updated_at"`
}
type Controller struct {
	root  string
	vault *security.Vault
}
type Permit struct {
	file       *os.File
	Generation uint64
}

func (p *Permit) Close() {
	if p != nil && p.file != nil {
		unlock(p.file)
		p.file.Close()
		p.file = nil
	}
}
func New(root string, vault *security.Vault) (*Controller, error) {
	if root == "" || vault == nil {
		return nil, fmt.Errorf("control directory and vault required")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	return &Controller{root: root, vault: vault}, nil
}
func (c *Controller) lock(ctx context.Context, exclusive bool) (*Permit, error) {
	return LockFile(ctx, filepath.Join(c.root, "permit.lock"), exclusive)
}

// LockFile provides a cancellable OS lock shared by all service processes.
func LockFile(ctx context.Context, path string, exclusive bool) (*Permit, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	for {
		ok, e := tryLock(f, exclusive)
		if e != nil {
			f.Close()
			return nil, e
		}
		if ok {
			return &Permit{file: f}, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (c *Controller) read() (s State, e error) {
	b, e := os.ReadFile(filepath.Join(c.root, "state.enc"))
	if errors.Is(e, os.ErrNotExist) {
		return State{Held: true}, nil
	}
	if e != nil {
		return s, e
	}
	b, e = c.vault.Open(b, "selfmail:recovery-control:v1")
	if e != nil {
		return s, fmt.Errorf("control integrity/key validation: %w", e)
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	if s.Instance == "" || s.Generation == 0 {
		return s, fmt.Errorf("invalid recovery control state")
	}
	return
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (c *Controller) write(s State) error {
	s.Updated = time.Now().UTC()
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	b, e = c.vault.Seal(b, "selfmail:recovery-control:v1")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(c.root, ".state-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(f.Name(), filepath.Join(c.root, "state.enc")); e != nil {
		return e
	}
	return syncDir(c.root)
}

// Initialize is called only after the administrator verifies a fresh, empty DB
// and journal. Missing state elsewhere always fails closed.
func (c *Controller) Initialize(ctx context.Context) error {
	p, e := c.lock(ctx, true)
	if e != nil {
		return e
	}
	defer p.Close()
	s, e := c.read()
	if e != nil {
		return e
	}
	if s.Instance != "" {
		return nil
	}
	return c.write(State{Instance: domain.ID(), Generation: 1, Reconciled: 1, Report: "fresh-instance"})
}
func (c *Controller) Acquire(ctx context.Context) (*Permit, error) {
	p, e := c.lock(ctx, false)
	if e != nil {
		return nil, e
	}
	if _, e = os.Stat(filepath.Join(c.root, "hold.pending")); e == nil {
		p.Close()
		return nil, ErrHeld
	} else if !errors.Is(e, os.ErrNotExist) {
		p.Close()
		return nil, e
	}
	s, e := c.read()
	if e != nil {
		p.Close()
		return nil, e
	}
	if s.Held || s.Instance == "" {
		p.Close()
		return nil, ErrHeld
	}
	p.Generation = s.Generation
	return p, nil
}
func (c *Controller) Status(ctx context.Context) (State, error) {
	p, e := c.lock(ctx, false)
	if e != nil {
		return State{}, e
	}
	defer p.Close()
	s, e := c.read()
	if e != nil {
		return s, e
	}
	if _, e = os.Stat(filepath.Join(c.root, "hold.pending")); e == nil {
		s.Held = true
		s.Reason = "hold requested; draining existing permits"
	} else if !errors.Is(e, os.ErrNotExist) {
		return s, e
	}
	return s, nil
}
func (c *Controller) Hold(ctx context.Context, reason string) error {
	if len(reason) < 10 || len(reason) > 512 {
		return fmt.Errorf("recovery reason requires 10..512 bytes")
	}
	// Block new readers before draining old permits, preventing writer starvation.
	marker := filepath.Join(c.root, "hold.pending")
	f, e := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e == nil {
		_, e = f.Write([]byte("hold requested"))
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return e
		}
		if e = syncDir(c.root); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrExist) {
		return e
	}
	p, e := c.lock(ctx, true)
	if e != nil {
		return e
	}
	defer p.Close()
	s, e := c.read()
	if e != nil {
		return e
	}
	if s.Instance == "" {
		s.Instance = domain.ID()
	}
	s.Generation++
	s.Held = true
	s.Reason = reason
	s.Reconciled = 0
	s.Report = ""
	if e = c.write(s); e != nil {
		return e
	}
	if e = os.Remove(marker); e != nil {
		return e
	}
	return syncDir(c.root)
}
func (c *Controller) Reconciled(ctx context.Context, generation uint64, report string) error {
	p, e := c.lock(ctx, true)
	if e != nil {
		return e
	}
	defer p.Close()
	s, e := c.read()
	if e != nil {
		return e
	}
	if !s.Held || s.Generation != generation || len(report) != 64 {
		return fmt.Errorf("invalid reconciliation generation/report")
	}
	s.Reconciled = generation
	s.Report = report
	return c.write(s)
}

// ImportHeld preserves an authenticated journal's original identity on a new
// host. The caller must validate the restored journal against source.Instance
// first. Import never resumes dispatch or replaces another deployment.
func (c *Controller) ImportHeld(ctx context.Context, source State, reason string) error {
	if !domain.ValidID(source.Instance) || source.Generation == ^uint64(0) || len(reason) < 10 || len(reason) > 512 {
		return fmt.Errorf("invalid recovery import identity/reason")
	}
	p, e := c.lock(ctx, true)
	if e != nil {
		return e
	}
	defer p.Close()
	current, e := c.read()
	if e != nil {
		return e
	}
	if current.Instance != "" && (current.Instance != source.Instance || !current.Held) {
		return fmt.Errorf("cannot replace an existing or running deployment identity")
	}
	generation := max(current.Generation, source.Generation)
	if generation == ^uint64(0) {
		return fmt.Errorf("recovery generation overflow")
	}
	return c.write(State{Instance: source.Instance, Generation: generation + 1, Held: true, Reason: reason})
}
func (c *Controller) Release(ctx context.Context, report string) error {
	p, e := c.lock(ctx, true)
	if e != nil {
		return e
	}
	defer p.Close()
	s, e := c.read()
	if e != nil {
		return e
	}
	if !s.Held || s.Reconciled != s.Generation || len(report) != 64 || report != s.Report {
		return fmt.Errorf("reconciliation proof required before release")
	}
	s.Held = false
	return c.write(s)
}
