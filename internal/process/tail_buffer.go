package process

import (
	"io"
	"os"
	"sync"
)

// TailBuffer retains only the final Limit bytes, making diagnostics bounded
// even when a failed installer emits an unbounded stream. It is safe for
// concurrent use: when cleanup cannot be proven, Runner returns the tails
// while a retained descendant's exec copy goroutine may still be writing.
type TailBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func NewTailBuffer(limit int) *TailBuffer { return &TailBuffer{limit: limit} }

func (buffer *TailBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.limit <= 0 {
		if len(data) > 0 {
			buffer.truncated = true
		}
		return len(data), nil
	}
	if len(data) > buffer.limit {
		buffer.data = append(buffer.data[:0], data[len(data)-buffer.limit:]...)
		buffer.truncated = true
		return len(data), nil
	}
	if len(data) == buffer.limit {
		buffer.data = append(buffer.data[:0], data...)
		return len(data), nil
	}
	if len(buffer.data)+len(data) > buffer.limit {
		drop := len(buffer.data) + len(data) - buffer.limit
		buffer.data = append(buffer.data[:0], buffer.data[drop:]...)
		buffer.truncated = true
	}
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}

func (buffer *TailBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(buffer.data)
}

func (buffer *TailBuffer) Truncated() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.truncated
}

func outputWriter(capture *TailBuffer, destination io.Writer, direct bool) io.Writer {
	if destination == nil {
		return capture
	}
	if file, ok := destination.(*os.File); ok && direct {
		return file
	}
	return io.MultiWriter(capture, destination)
}
