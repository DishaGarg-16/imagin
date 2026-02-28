package pool

import (
	"bufio"
	"compress/gzip"
	"io"
	"sync"

	"github.com/imagin/imagin/internal/digest"
)

// ---------------------------------------------------------------------------
// Gzip writer pool — avoids the expensive gzip.NewWriter allocation.
// ---------------------------------------------------------------------------

var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		// Level 6 (default) is a good balance between speed and ratio.
		// Using a fixed level avoids compression-time variance that
		// causes tail latency spikes.
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return w
	},
}

// GetGzipWriter returns a *gzip.Writer from the pool, reset to write to w.
func GetGzipWriter(w io.Writer) *gzip.Writer {
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(w)
	return gz
}

// PutGzipWriter returns a *gzip.Writer to the pool.
// The caller must have called gz.Close() before returning.
func PutGzipWriter(gz *gzip.Writer) {
	gzipWriterPool.Put(gz)
}

// ---------------------------------------------------------------------------
// Buffered writer pool
// ---------------------------------------------------------------------------

var bufWriterPool = sync.Pool{
	New: func() interface{} {
		return bufio.NewWriterSize(io.Discard, DefaultBufSize)
	},
}

// GetBufWriter returns a buffered writer from the pool, reset to write to w.
func GetBufWriter(w io.Writer) *bufio.Writer {
	bw := bufWriterPool.Get().(*bufio.Writer)
	bw.Reset(w)
	return bw
}

// PutBufWriter returns a buffered writer to the pool.
// The caller should have called bw.Flush() first.
func PutBufWriter(bw *bufio.Writer) {
	bufWriterPool.Put(bw)
}

// ---------------------------------------------------------------------------
// StreamingPipeline chains: writer → bufio → tee(sha256) → gzip → tee(sha256) → dest
// This lets us compute both the uncompressed and compressed digests in
// a single pass with zero intermediate buffering.
// ---------------------------------------------------------------------------

// StreamingPipeline holds pooled resources for a hash+compress pipeline.
type StreamingPipeline struct {
	// Writer is where tar entries should be written. Data flows:
	//   Writer → (uncompressed hasher) → gzip → (compressed hasher) → destination
	Writer io.Writer

	uncompHasher *digest.TeeHasher
	compHasher   *digest.TeeHasher
	gzWriter     *gzip.Writer
	bufWriter    *bufio.Writer
}

// NewStreamingPipeline creates a pipeline that compresses and hashes in one pass.
// dest is the final output (e.g. an os.File for the layer blob).
func NewStreamingPipeline(dest io.Writer) *StreamingPipeline {
	p := &StreamingPipeline{}

	// 1. Buffered writer around destination for syscall amortisation
	p.bufWriter = GetBufWriter(dest)

	// 2. Compressed-data hasher: hashes the gzip bytes (distribution digest)
	p.compHasher = digest.NewTeeHasher(p.bufWriter)

	// 3. Gzip compressor
	p.gzWriter = GetGzipWriter(p.compHasher)

	// 4. Uncompressed-data hasher: hashes the raw tar bytes (diff ID)
	p.uncompHasher = digest.NewTeeHasher(p.gzWriter)

	// Callers write tar data here
	p.Writer = p.uncompHasher

	return p
}

// Close flushes and closes the pipeline, returning all pooled resources.
// Returns (diffID, distributionDigest, compressedSize, error).
func (p *StreamingPipeline) Close() (diffID string, distDigest string, compressedSize int64, err error) {
	// Close gzip (flushes remaining compressed data)
	if err = p.gzWriter.Close(); err != nil {
		return
	}

	// Flush buffered writer
	if err = p.bufWriter.Flush(); err != nil {
		return
	}

	diffID = p.uncompHasher.Digest()
	distDigest = p.compHasher.Digest()
	compressedSize = p.compHasher.BytesWritten()

	// Return resources to pools
	PutGzipWriter(p.gzWriter)
	PutBufWriter(p.bufWriter)
	p.uncompHasher.Close()
	p.compHasher.Close()

	return
}
