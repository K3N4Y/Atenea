package event

import (
	"context"
	"time"
)

// DataVersioner exposes a store's data version (PRAGMA data_version in SQLite):
// a counter changed by writes from another connection, typically another
// process such as the TUI, but not by writes through the current connection.
type DataVersioner interface {
	DataVersion(ctx context.Context) (int64, error)
}

const DefaultStoreWatchInterval = time.Second

// StartStoreWatch makes the initial DataVersion attempt synchronously, then
// starts polling in a goroutine. If that attempt fails, the first later valid
// read is reported conservatively because a write may have happened before a
// baseline could be established. Read errors are retried on the next tick.
func StartStoreWatch(ctx context.Context, v DataVersioner, interval time.Duration, onChange func()) {
	if ctx.Err() != nil {
		return
	}
	baseline, err := v.DataVersion(ctx)
	if ctx.Err() != nil {
		return
	}
	if interval <= 0 {
		interval = DefaultStoreWatchInterval
	}
	ticker := time.NewTicker(interval)

	go pollStore(ctx, v, ticker, baseline, err == nil, onChange)
}

func pollStore(ctx context.Context, v DataVersioner, ticker *time.Ticker, baseline int64, hasBaseline bool, onChange func()) {
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			version, err := v.DataVersion(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				continue
			}
			if !hasBaseline {
				if ctx.Err() != nil {
					return
				}
				baseline, hasBaseline = version, true
				onChange()
				continue
			}
			if version != baseline {
				baseline = version
				if ctx.Err() != nil {
					return
				}
				onChange()
			}
		}
	}
}
