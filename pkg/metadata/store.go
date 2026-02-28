// Package metadata provides an in-memory metadata store for image layers,
// configuration, and build caching. Optimized for lock-free reads via sync.Map.
package metadata

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/imagin/imagin/internal/digest"
	"github.com/imagin/imagin/internal/types"
)

// Store implements types.MetadataStore with an in-memory backend.
// Read operations are lock-free (sync.Map); write operations use a mutex
// only for slice append ordering.
type Store struct {
	// layers is the ordered list of layers — protected by layerMu for appends.
	layers  []*types.Layer
	layerMu sync.Mutex

	// config is stored atomically (pointer swap).
	config atomic.Value // *types.ImageConfig

	// cache maps "parentChainID:instructionHash" → *types.Layer
	cache sync.Map
}

// NewStore creates an empty metadata store.
func NewStore() *Store {
	s := &Store{
		layers: make([]*types.Layer, 0, 16),
	}
	return s
}

// AddLayer records a layer in the store. Thread-safe.
func (s *Store) AddLayer(layer *types.Layer) error {
	if layer == nil {
		return fmt.Errorf("metadata: cannot add nil layer")
	}
	s.layerMu.Lock()
	s.layers = append(s.layers, layer)
	s.layerMu.Unlock()
	return nil
}

// GetLayers returns all layers in order. Returns a snapshot copy.
func (s *Store) GetLayers() []*types.Layer {
	s.layerMu.Lock()
	defer s.layerMu.Unlock()

	out := make([]*types.Layer, len(s.layers))
	copy(out, s.layers)
	return out
}

// LayerCount returns the current number of layers without copying.
func (s *Store) LayerCount() int {
	s.layerMu.Lock()
	defer s.layerMu.Unlock()
	return len(s.layers)
}

// SetConfig stores the image configuration. Thread-safe (atomic pointer swap).
func (s *Store) SetConfig(config *types.ImageConfig) error {
	if config == nil {
		return fmt.Errorf("metadata: cannot set nil config")
	}
	s.config.Store(config)
	return nil
}

// GetConfig returns the current image configuration.
func (s *Store) GetConfig() *types.ImageConfig {
	v := s.config.Load()
	if v == nil {
		return nil
	}
	return v.(*types.ImageConfig)
}

// ---------------------------------------------------------------------------
// Build cache — lock-free reads via sync.Map
// ---------------------------------------------------------------------------

// cacheKey constructs the cache lookup key.
func cacheKey(parentChainID, instructionHash string) string {
	return parentChainID + ":" + instructionHash
}

// CacheLookup checks if an instruction result is cached.
// Returns the cached layer or nil. This is the READ hot-path and is lock-free.
func (s *Store) CacheLookup(parentChainID string, instructionHash string) *types.Layer {
	key := cacheKey(parentChainID, instructionHash)
	val, ok := s.cache.Load(key)
	if !ok {
		return nil
	}
	return val.(*types.Layer)
}

// CacheStore records a build result in the cache. Thread-safe.
func (s *Store) CacheStore(parentChainID string, instructionHash string, layer *types.Layer) error {
	if layer == nil {
		return fmt.Errorf("metadata: cannot cache nil layer")
	}
	key := cacheKey(parentChainID, instructionHash)
	s.cache.Store(key, layer)
	return nil
}

// ComputeChainID computes a chain ID from a list of layer diff IDs.
// The chain ID for a single layer is its diff ID. For multiple layers:
//
//	chainID(L0, L1, ..., Ln) = SHA256(chainID(L0..Ln-1) + " " + diffID(Ln))
//
// This is how Docker/OCI determines if two layer stacks are identical.
func ComputeChainID(diffIDs []types.Digest) string {
	if len(diffIDs) == 0 {
		return ""
	}

	chainID := string(diffIDs[0])
	for i := 1; i < len(diffIDs); i++ {
		chainID = digest.FromBytes([]byte(chainID + " " + string(diffIDs[i])))
	}
	return chainID
}

// InstructionHash computes a deterministic hash for a Dockerfile instruction.
// Used as part of the cache key.
func InstructionHash(raw string) string {
	return digest.FromBytes([]byte(raw))
}
