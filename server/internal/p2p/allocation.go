package p2p

import (
	"context"
	"sync"
	"time"

	"github.com/linknestx/linknestx/server/internal/auth"
)

const allocationLease = 60 * time.Second
const maxAllocations = 1024

type AllocationInput struct {
	VersionID string `json:"versionId"`
	TaskID    string `json:"taskId"`
}

// Allocation is a half-open initial region; it never grants access to bytes.
type Allocation struct {
	StartIndex int       `json:"startIndex"`
	EndIndex   int       `json:"endIndex"`
	ExpiresAt  time.Time `json:"expiresAt"`
}
type allocationKey struct{ version, device string }
type allocationEntry struct {
	Allocation
	region int
	task   string
}
type allocationBook struct {
	mu     sync.Mutex
	leases map[allocationKey]allocationEntry
}

func (a *allocationBook) prune(now time.Time) {
	for key, lease := range a.leases {
		if !now.Before(lease.ExpiresAt) {
			delete(a.leases, key)
		}
	}
}
func (a *allocationBook) cleanup(now time.Time) { a.mu.Lock(); defer a.mu.Unlock(); a.prune(now) }
func (a *allocationBook) allocate(now time.Time, version, device, task string, chunks int) (Allocation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.leases == nil {
		a.leases = make(map[allocationKey]allocationEntry)
	}
	a.prune(now)
	key := allocationKey{version, device}
	if existing, ok := a.leases[key]; ok {
		return existing.Allocation, nil
	}
	if len(a.leases) >= maxAllocations {
		return Allocation{}, ErrLimit
	}
	regions := min(3, chunks)
	selected := 0
	if regions > 0 {
		occupancy := make([]int, regions)
		for k, entry := range a.leases {
			if k.version == version && entry.region < regions {
				occupancy[entry.region]++
			}
		}
		for i := 1; i < regions; i++ {
			if occupancy[i] < occupancy[selected] {
				selected = i
			}
		}
	}
	result := Allocation{ExpiresAt: now.Add(allocationLease)}
	if regions > 0 {
		result.StartIndex = selected * chunks / regions
		result.EndIndex = (selected + 1) * chunks / regions
	}
	a.leases[key] = allocationEntry{Allocation: result, region: selected, task: task}
	return result, nil
}
func (s *Service) Allocate(ctx context.Context, actor string, input AllocationInput) (Allocation, error) {
	if !validID(input.VersionID) || !validID(input.TaskID) {
		return Allocation{}, ErrInvalidInput
	}
	if err := s.ValidateActor(ctx, actor); err != nil {
		return Allocation{}, err
	}
	device := auth.DeviceIDFromContext(ctx)
	if !validID(device) {
		return Allocation{}, auth.ErrUnauthorized
	}
	manifest, err := s.files.Manifest(ctx, actor, input.VersionID)
	if err != nil {
		return Allocation{}, err
	}
	if err = ctx.Err(); err != nil {
		return Allocation{}, err
	}
	return s.allocations.allocate(s.now(), input.VersionID, device, input.TaskID, len(manifest.Chunks))
}
