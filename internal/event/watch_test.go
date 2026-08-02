package event

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests define the store watcher's contract: the poller turns PRAGMA
// data_version into the signal that another process wrote to the database.
// Fakes keep the tests independent from Wails and SQLite.

// flipVersioner returns version 1 for the first reads and 2 afterward,
// simulating another process writing between watcher ticks.
type flipVersioner struct {
	reads atomic.Int64
}

var _ DataVersioner = (*flipVersioner)(nil)

func (f *flipVersioner) DataVersion(ctx context.Context) (int64, error) {
	if f.reads.Add(1) <= 2 {
		return 1, nil
	}
	return 2, nil
}

// A data version change between ticks triggers onChange.
func TestStartStoreWatch_NotifiesOnVersionChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changed := make(chan struct{}, 1)
	StartStoreWatch(ctx, &flipVersioner{}, time.Millisecond, func() {
		select {
		case changed <- struct{}{}:
		default: // One pending notification is enough for this test.
		}
	})

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("store watcher did not call onChange after data_version changed")
	}
}

// countingVersioner always returns the same version and signals after at least
// three reads, enough ticks to establish that no change means no notification.
type countingVersioner struct {
	reads  atomic.Int64
	once   sync.Once
	enough chan struct{}
}

var _ DataVersioner = (*countingVersioner)(nil)

func (c *countingVersioner) DataVersion(ctx context.Context) (int64, error) {
	if c.reads.Add(1) >= 3 {
		c.once.Do(func() { close(c.enough) })
	}
	return 7, nil
}

// A stable version does not trigger onChange, including after several ticks.
func TestStartStoreWatch_DoesNotNotifyWithoutChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &countingVersioner{enough: make(chan struct{})}
	var calls atomic.Int64
	StartStoreWatch(ctx, fake, time.Millisecond, func() { calls.Add(1) })

	select {
	case <-fake.enough:
	case <-time.After(2 * time.Second):
		t.Fatal("store watcher did not make three DataVersion reads")
	}
	cancel()
	time.Sleep(5 * time.Millisecond)

	if n := calls.Load(); n != 0 {
		t.Fatalf("onChange called %d times without a version change, want 0", n)
	}
}

// steppedVersioner advances through 1,1,2,2,3,3,3..., simulating two external
// writes separated by watcher ticks. It signals after eight reads, enough time
// on the stable final version to detect extra notifications.
type steppedVersioner struct {
	reads  atomic.Int64
	once   sync.Once
	enough chan struct{}
}

var _ DataVersioner = (*steppedVersioner)(nil)

func (s *steppedVersioner) DataVersion(ctx context.Context) (int64, error) {
	n := s.reads.Add(1)
	if n >= 8 {
		s.once.Do(func() { close(s.enough) })
	}
	if v := (n + 1) / 2; v < 3 {
		return v, nil
	}
	return 3, nil
}

// Every version change produces its own notification, while the stable final
// version produces no extras. This catches watchers that stop after one change
// or fail to update the baseline after notifying.
func TestStartStoreWatch_NotifiesOnEachChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &steppedVersioner{enough: make(chan struct{})}
	changed := make(chan struct{}, 16)
	var calls atomic.Int64
	StartStoreWatch(ctx, fake, time.Millisecond, func() {
		calls.Add(1)
		changed <- struct{}{}
	})

	for i := 1; i <= 2; i++ {
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatalf("notification #%d did not arrive: watcher must notify on every version change, not only the first", i)
		}
	}

	// Let the watcher poll the stable version, then wait for it before reading
	// calls to avoid a race.
	select {
	case <-fake.enough:
	case <-time.After(2 * time.Second):
		t.Fatal("store watcher did not make eight DataVersion reads")
	}
	cancel()
	time.Sleep(5 * time.Millisecond)

	if n := calls.Load(); n != 2 {
		t.Fatalf("onChange called %d times, want exactly 2 (one per version change; the baseline must update after notification)", n)
	}
}

type recoveringVersioner struct {
	reads atomic.Int64
}

func (v *recoveringVersioner) DataVersion(context.Context) (int64, error) {
	if v.reads.Add(1) == 1 {
		return 0, errors.New("temporary read failure")
	}
	return 9, nil
}

// When initialization fails, the first valid read must notify before becoming
// the baseline so a write in the unobserved interval cannot be lost.
func TestStartStoreWatch_InitialErrorNotifiesOnFirstValidRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changed := make(chan struct{}, 1)
	fake := &recoveringVersioner{}
	StartStoreWatch(ctx, fake, time.Millisecond, func() { changed <- struct{}{} })

	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("first valid DataVersion read after initialization failure did not notify")
	}
	if reads := fake.reads.Load(); reads < 2 {
		t.Fatalf("DataVersion reads = %d, want at least 2", reads)
	}
}

// A context canceled before startup causes no reads, polling, or callbacks.
func TestStartStoreWatch_CanceledContextDoesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fake := &countingVersioner{enough: make(chan struct{})}
	var calls atomic.Int64
	StartStoreWatch(ctx, fake, time.Millisecond, func() { calls.Add(1) })

	if reads := fake.reads.Load(); reads != 0 {
		t.Fatalf("DataVersion reads = %d after cancellation, want 0", reads)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("onChange calls = %d after cancellation, want 0", got)
	}
}

func TestStartStoreWatch_DefaultsNonPositiveInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			fake := &countingVersioner{enough: make(chan struct{})}

			StartStoreWatch(ctx, fake, interval, func() {})
			cancel()

			if reads := fake.reads.Load(); reads != 1 {
				t.Fatalf("DataVersion reads = %d at startup, want 1", reads)
			}
		})
	}
}

type blockedPollVersioner struct {
	reads       atomic.Int64
	pollStarted chan struct{}
	releasePoll chan struct{}
}

func (v *blockedPollVersioner) DataVersion(context.Context) (int64, error) {
	if v.reads.Add(1) == 1 {
		return 1, nil
	}
	close(v.pollStarted)
	<-v.releasePoll
	return 2, nil
}

// Cancellation while a poll is in flight suppresses a later successful result.
func TestStartStoreWatch_CancelDuringReadDoesNotNotify(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &blockedPollVersioner{
		pollStarted: make(chan struct{}),
		releasePoll: make(chan struct{}),
	}
	var calls atomic.Int64
	StartStoreWatch(ctx, fake, time.Millisecond, func() { calls.Add(1) })

	select {
	case <-fake.pollStarted:
	case <-time.After(time.Second):
		t.Fatal("poll did not start")
	}
	cancel()
	close(fake.releasePoll)
	time.Sleep(5 * time.Millisecond)

	if got := calls.Load(); got != 0 {
		t.Fatalf("onChange calls = %d after cancellation, want 0", got)
	}
}
