package limiter

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestRedisAtomicBurst(t *testing.T) {
	raw := os.Getenv("REDIS_URL")
	if raw == "" {
		t.Skip("REDIS_URL not set")
	}
	l, e := Open(raw)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Client.Close()
	tenant := domain.ID()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait, e := l.Ingress(context.Background(), tenant, 5)
			if e != nil {
				t.Error(e)
			}
			if wait == 0 {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 5 {
		t.Fatalf("allowed %d, want 5", allowed.Load())
	}
}
