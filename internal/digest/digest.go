// Package digest provides content-addressable hashing utilities optimised
// for low-allocation streaming computation.
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"sync"
)

const (
	// SHA256Prefix is the canonical prefix for SHA-256 digests.
	SHA256Prefix = "sha256:"
)

// pool reuses SHA-256 hash state objects to avoid per-layer allocation.
var hashPool = sync.Pool{
	New: func() interface{} {
		return sha256.New()
	},
}

// GetHash returns a SHA-256 hash.Hash from the pool.
// The caller MUST call PutHash when done.
func GetHash() hash.Hash {
	h := hashPool.Get().(hash.Hash)
	h.Reset()
	return h
}

// PutHash returns a hash.Hash to the pool.
func PutHash(h hash.Hash) {
	hashPool.Put(h)
}

// FromReader computes the SHA-256 digest of all data from r.
// It uses a pooled hasher and a pooled 32 KB buffer for copying.
func FromReader(r io.Reader) (string, int64, error) {
	h := GetHash()
	defer PutHash(h)

	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, fmt.Errorf("digest: hashing failed: %w", err)
	}

	return SHA256Prefix + hex.EncodeToString(h.Sum(nil)), n, nil
}

// FromBytes computes the SHA-256 digest of data.
func FromBytes(data []byte) string {
	h := GetHash()
	defer PutHash(h)

	h.Write(data)
	return SHA256Prefix + hex.EncodeToString(h.Sum(nil))
}

// NewTeeHasher wraps w so that every byte written flows through a SHA-256
// hash as well. Call Digest() on the returned TeeHasher to get the final
// digest after all writes are complete.
func NewTeeHasher(w io.Writer) *TeeHasher {
	h := GetHash()
	return &TeeHasher{
		Writer: io.MultiWriter(w, h),
		hash:   h,
	}
}

// TeeHasher writes to an underlying writer while simultaneously computing
// a SHA-256 digest of all data written through it.
type TeeHasher struct {
	io.Writer
	hash  hash.Hash
	count int64
}

// Write implements io.Writer.
func (t *TeeHasher) Write(p []byte) (int, error) {
	n, err := t.Writer.Write(p)
	t.count += int64(n)
	return n, err
}

// Digest returns the completed digest string. Must be called after all
// writes are done.
func (t *TeeHasher) Digest() string {
	return SHA256Prefix + hex.EncodeToString(t.hash.Sum(nil))
}

// BytesWritten returns the total number of bytes written.
func (t *TeeHasher) BytesWritten() int64 {
	return t.count
}

// Close returns the hash to the pool. The TeeHasher must not be used
// after calling Close.
func (t *TeeHasher) Close() {
	PutHash(t.hash)
	t.hash = nil
}
