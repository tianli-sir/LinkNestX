package p2p

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linknestx/linknestx/server/internal/auth"
	"github.com/linknestx/linknestx/server/internal/files"
)

func TestAllocationDisjointCollidingDevicesConcurrentAndIdempotent(t *testing.T) {
	for _, chunks := range []int{3, 12} {
		t.Run(fmt.Sprint(chunks), func(t *testing.T) {
			a := allocationBook{}
			now := time.Now()
			devices := []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000004", "00000000-0000-0000-0000-000000000007"}
			results := make([]Allocation, 3)
			var wg sync.WaitGroup
			for i, d := range devices {
				wg.Add(1)
				go func(i int, d string) {
					defer wg.Done()
					var err error
					results[i], err = a.allocate(now, "version", d, uuid.NewString(), chunks)
					if err != nil {
						t.Error(err)
					}
				}(i, d)
			}
			wg.Wait()
			claimed := map[int]bool{}
			for i, r := range results {
				for idx := r.StartIndex; idx < r.EndIndex; idx++ {
					if claimed[idx] {
						t.Fatalf("overlap %v", results)
					}
					claimed[idx] = true
				}
				again, err := a.allocate(now, "version", devices[i], uuid.NewString(), chunks)
				if err != nil || again.StartIndex != r.StartIndex || again.EndIndex != r.EndIndex {
					t.Fatalf("same device changed allocation: %+v %v", again, err)
				}
			}
			if len(claimed) != chunks {
				t.Fatalf("regions omit chunks: %v", claimed)
			}
		})
	}
}
func TestAllocationBoundExpiryAndEmptyVersion(t *testing.T) {
	a := allocationBook{}
	now := time.Now()
	for i := 0; i < maxAllocations; i++ {
		if _, err := a.allocate(now, "version", fmt.Sprint(i), "task", 3); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.allocate(now, "version", "overflow", "task", 3); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit %v", err)
	}
	r, err := a.allocate(now.Add(allocationLease+time.Second), "zero", "device", "task", 0)
	if err != nil || r.StartIndex != 0 || r.EndIndex != 0 {
		t.Fatalf("empty %+v %v", r, err)
	}
	if len(a.leases) != 1 {
		t.Fatalf("stale leases retained %d", len(a.leases))
	}
}
func TestAllocationChecksCurrentActorAndFilePermissions(t *testing.T) {
	f := setup(t)
	input := AllocationInput{VersionID: f.version.VersionID, TaskID: uuid.NewString()}
	got, err := f.service.Allocate(f.contexts[0], f.sessions[0].User.ID, input)
	if err != nil || got.StartIndex != 0 || got.EndIndex != 1 {
		t.Fatalf("authorized %+v %v", got, err)
	}
	if _, err = f.service.Allocate(context.Background(), f.sessions[0].User.ID, input); err == nil {
		t.Fatal("untrusted actor accepted")
	}
	if _, err = f.service.Allocate(f.contexts[0], f.sessions[1].User.ID, input); err == nil {
		t.Fatal("spoofed actor accepted")
	}
	if _, err = f.service.Allocate(f.contexts[2], f.sessions[2].User.ID, input); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("outsider %v", err)
	}
	bad := input
	bad.TaskID = "bad"
	if _, err = f.service.Allocate(f.contexts[0], f.sessions[0].User.ID, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("task %v", err)
	}
	if _, err = f.db.Exec("UPDATE sessions SET revoked_at=? WHERE id=?", stamp(time.Now()), f.sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Allocate(f.contexts[0], f.sessions[0].User.ID, input); !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("revoked %v", err)
	}
}
