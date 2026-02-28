// Package pool provides sync.Pool-backed buffer management to reduce GC
// pressure on the hot path (tar creation, compression, hashing).
package pool

import (
	"bytes"
	"sync"
)

const (
	// DefaultBufSize is the default buffer size — 32 KB is a sweet spot for
	// tar I/O: large enough to amortise syscall overhead, small enough to
	// fit in L1 cache on most architectures.
	DefaultBufSize = 32 * 1024

	// LargeBufSize is used for file copy operations where larger buffers
	// reduce the number of read/write syscalls.
	LargeBufSize = 256 * 1024
)

// ---------------------------------------------------------------------------
// Byte-slice pool
// ---------------------------------------------------------------------------

var bufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, DefaultBufSize)
		return &b
	},
}

var largeBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, LargeBufSize)
		return &b
	},
}

// GetBuffer returns a *[]byte of DefaultBufSize from the pool.
// The caller MUST call PutBuffer when done.
func GetBuffer() *[]byte {
	return bufPool.Get().(*[]byte)
}

// PutBuffer returns a buffer to the pool.
func PutBuffer(b *[]byte) {
	if b == nil {
		return
	}
	// Reset length to capacity so the next consumer gets a full buffer.
	*b = (*b)[:cap(*b)]
	bufPool.Put(b)
}

// GetLargeBuffer returns a *[]byte of LargeBufSize from the pool.
func GetLargeBuffer() *[]byte {
	return largeBufPool.Get().(*[]byte)
}

// PutLargeBuffer returns a large buffer to the pool.
func PutLargeBuffer(b *[]byte) {
	if b == nil {
		return
	}
	*b = (*b)[:cap(*b)]
	largeBufPool.Put(b)
}

// ---------------------------------------------------------------------------
// bytes.Buffer pool
// ---------------------------------------------------------------------------

var bytesBufferPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, DefaultBufSize))
	},
}

// GetBytesBuffer returns a *bytes.Buffer pre-allocated with DefaultBufSize.
func GetBytesBuffer() *bytes.Buffer {
	buf := bytesBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// PutBytesBuffer returns a *bytes.Buffer to the pool.
func PutBytesBuffer(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	bytesBufferPool.Put(buf)
}
