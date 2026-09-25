package process_test

import (
	"strings"
	"sync"
	"testing"

	processpkg "github.com/xenoviz/ruk/internal/process"
)

func TestTailBufferKeepsBoundedTail(t *testing.T) {
	t.Parallel()
	buffer := processpkg.NewTailBuffer(4)
	_, _ = buffer.Write([]byte("12"))
	_, _ = buffer.Write([]byte("345"))
	if got := buffer.String(); got != "2345" || !buffer.Truncated() {
		t.Fatalf("tail = %q truncated = %v", got, buffer.Truncated())
	}
}

func TestTailBufferConcurrentWriteAndRead(t *testing.T) {
	t.Parallel()
	// A retained descendant may keep writing through exec's copy goroutine
	// while Runner reads the tail for its result; run under -race.
	buffer := processpkg.NewTailBuffer(64)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for range 2000 {
			_, _ = buffer.Write([]byte("abcdefgh"))
		}
	}()
	go func() {
		defer group.Done()
		for range 2000 {
			if got := buffer.String(); len(got) > 64 || strings.Trim(got, "abcdefgh") != "" {
				t.Errorf("torn tail %q", got)
				return
			}
			_ = buffer.Truncated()
		}
	}()
	group.Wait()
}
