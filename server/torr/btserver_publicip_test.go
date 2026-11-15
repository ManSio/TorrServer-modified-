package torr

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// resolvePublicIP must run the lookup under a deadline: Connect used to pass
// context.TODO(), so an unreachable lookup host held startup and every settings
// save. The test fails fast through the watchdog, not through go test's timeout.
func TestResolvePublicIPGivesUpOnTimeout(t *testing.T) {
	prevTimeout, prevLookup := publicIPTimeout, publicIPLookup
	t.Cleanup(func() { publicIPTimeout, publicIPLookup = prevTimeout, prevLookup })

	publicIPTimeout = 100 * time.Millisecond
	deadline := make(chan error, 1)
	publicIPLookup = func(ctx context.Context, v6 bool) (net.IP, error) {
		<-ctx.Done()
		deadline <- ctx.Err()
		return nil, ctx.Err()
	}

	type result struct {
		ip  net.IP
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := publicIPContext()
	defer cancel()
	go func() {
		ip, err := resolvePublicIP(ctx, false)
		done <- result{ip, err}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("lookup that never answers returned no error")
		}
		if r.ip != nil {
			t.Errorf("ip = %v, want nil on timeout", r.ip)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("resolvePublicIP ignored the %v deadline", publicIPTimeout)
	}

	select {
	case err := <-deadline:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("context error = %v, want context.DeadlineExceeded", err)
		}
	default:
		t.Fatal("lookup never observed the context deadline")
	}
}

func TestResolvePublicIPReturnsLookupResult(t *testing.T) {
	prevLookup := publicIPLookup
	t.Cleanup(func() { publicIPLookup = prevLookup })

	want := net.ParseIP("203.0.113.7")
	publicIPLookup = func(ctx context.Context, v6 bool) (net.IP, error) {
		if v6 {
			return nil, errors.New("unexpected v6 lookup")
		}
		return want, nil
	}

	ctx, cancel := publicIPContext()
	defer cancel()
	got, err := resolvePublicIP(ctx, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("ip = %v, want %v", got, want)
	}
}

// Both lookups share one deadline, so a box whose IPv4 and IPv6 lookups both
// hang waits the bound once, not twice.
func TestPublicIPContextIsBounded(t *testing.T) {
	prevTimeout := publicIPTimeout
	t.Cleanup(func() { publicIPTimeout = prevTimeout })

	if publicIPTimeout <= 0 || publicIPTimeout > 10*time.Second {
		t.Errorf("publicIPTimeout = %v, want a small positive bound", publicIPTimeout)
	}

	publicIPTimeout = 50 * time.Millisecond
	ctx, cancel := publicIPContext()
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("publicIPContext returned a context without a deadline")
	}
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("ctx.Err() = %v, want context.DeadlineExceeded", ctx.Err())
	}
}
