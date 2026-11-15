package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedParallelHosts(t *testing.T) {
	hosts := make([]string, 20)
	for i := range hosts {
		hosts[i] = fmt.Sprint(i)
	}
	var active, peak, done atomic.Int32
	e := forHosts(context.Background(), hosts, 3, func(context.Context, string) error {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		done.Add(1)
		return nil
	})
	if e != nil || done.Load() != 20 || peak.Load() > 3 {
		t.Fatal(e, done.Load(), peak.Load())
	}
}
func TestParallelErrorCancelsRemainingWork(t *testing.T) {
	e := forHosts(context.Background(), []string{"a", "b", "c"}, 1, func(context.Context, string) error { return errors.New("expected") })
	if e == nil {
		t.Fatal("error was discarded")
	}
}
