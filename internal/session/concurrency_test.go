package session

import (
	"sync"
	"testing"
	"time"
)

func TestConcurrentReadStatusWithHardReset(t *testing.T) {
	id := openLocalReady(t)
	defer Close(id)
	var wg sync.WaitGroup
	stop := time.Now().Add(1 * time.Second)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				_ = Read(id, 0, "tail", 0, 0)
				_ = Status(id)
			}
		}()
	}
	// concurrently do a hard reset mid-flight
	go func() {
		time.Sleep(200 * time.Millisecond)
		Control(id, "hard")
	}()
	wg.Wait()
}
