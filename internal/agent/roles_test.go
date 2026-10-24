package agent

import (
	"sync"
	"testing"
	"time"
)

func TestReviewRolesRunTogether(t *testing.T) {
	var mu sync.Mutex
	active, peak := 0, 0
	arrived := 0
	release := make(chan struct{})
	roles := append([]reviewRole(nil), reviewRoles...)
	errCh := make(chan error, 1)
	go func() {
		runReviewRoles(roles, func(role reviewRole) error {
			mu.Lock()
			active++
			arrived++
			if active > peak {
				peak = active
			}
			allHere := arrived == len(roles)
			mu.Unlock()
			if allHere {
				close(release)
			}
			<-release
			mu.Lock()
			active--
			mu.Unlock()
			return nil
		})
		errCh <- nil
	}()
	select {
	case <-errCh:
	case <-time.After(time.Second):
		t.Fatal("分工没有结束")
	}
	if peak < 2 {
		t.Fatalf("分工没有并行，峰值 %d", peak)
	}
}
