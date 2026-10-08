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

func TestDestinationPolicyIsSharedAcrossTenantsAndWorkers(t *testing.T) {
	if os.Getenv("REDIS_URL") == "" {
		t.Skip("REDIS_URL not set")
	}
	a, e := Open(os.Getenv("REDIS_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Client.Close()
	b, e := Open(os.Getenv("REDIS_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer b.Client.Close()
	a.DestinationRate, b.DestinationRate = 1, 1
	ctx := context.Background()
	destination := domain.ID() + ".example.test"
	if wait, e := a.Take(ctx, domain.ID(), destination, 1000); e != nil || wait != 0 {
		t.Fatal("initial destination token rejected", wait, e)
	}
	if wait, e := b.Take(ctx, domain.ID(), destination, 1000); e != nil || wait <= 0 {
		t.Fatal("second tenant/worker bypassed shared destination limit", wait, e)
	}
	if wait, e := b.Take(ctx, domain.ID(), domain.ID()+".example.test", 1000); e != nil || wait != 0 {
		t.Fatal("different destination shared the exhausted bucket", wait, e)
	}
}
