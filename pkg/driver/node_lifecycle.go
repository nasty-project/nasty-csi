package driver

import (
	"context"
	"sync"
)

// volumeLifecycleLocks serialize stage and unstage for a volume, including an
// in-progress filesystem repair that must not be interrupted by RPC cancellation.
type volumeLifecycleLocks struct {
	entries map[string]*volumeLifecycleEntry
	mu      sync.Mutex
}

type volumeLifecycleEntry struct {
	token chan struct{}
	refs  int
}

func (l *volumeLifecycleLocks) lock(ctx context.Context, volumeID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*volumeLifecycleEntry)
	}
	entry := l.entries[volumeID]
	if entry == nil {
		entry = &volumeLifecycleEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		l.entries[volumeID] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			l.release(volumeID, entry)
			return nil, err
		}
		return func() {
			entry.token <- struct{}{}
			l.release(volumeID, entry)
		}, nil
	case <-ctx.Done():
		l.release(volumeID, entry)
		return nil, ctx.Err()
	}
}

func (l *volumeLifecycleLocks) release(volumeID string, entry *volumeLifecycleEntry) {
	l.mu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.entries, volumeID)
	}
	l.mu.Unlock()
}
