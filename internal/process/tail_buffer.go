package process

import "io"

// TailBuffer retains only the final Limit bytes, making diagnostics bounded
// even when a failed installer emits an unbounded stream.
type TailBuffer struct {
	limit     int
	data      []byte
	truncated bool
}

func NewTailBuffer(limit int) *TailBuffer { return &TailBuffer{limit: limit} }

func (buffer *TailBuffer) Write(data []byte) (int, error) {
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

func (buffer *TailBuffer) String() string { return string(buffer.data) }

func (buffer *TailBuffer) Truncated() bool { return buffer.truncated }

func outputWriter(capture *TailBuffer, destination io.Writer) io.Writer {
	if destination == nil {
		return capture
	}
	return io.MultiWriter(capture, destination)
}
