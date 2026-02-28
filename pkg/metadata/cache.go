package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/imagin/imagin/internal/types"
)

// Cache provides persistent build caching across builds.
// It wraps the in-memory sync.Map cache with optional JSON file persistence.
type Cache struct {
	mu       sync.Mutex
	filePath string
	entries  map[string]CacheEntry
}

// CacheEntry stores a cached layer reference.
type CacheEntry struct {
	DiffID    string `json:"diff_id"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	BlobPath  string `json:"blob_path"`
	CreatedBy string `json:"created_by"`
}

// NewCache creates a new build cache. If filePath is non-empty, the cache
// is loaded from and persisted to that file.
func NewCache(filePath string) *Cache {
	c := &Cache{
		filePath: filePath,
		entries:  make(map[string]CacheEntry, 64),
	}

	if filePath != "" {
		c.loadFromFile()
	}
	return c
}

// Lookup checks for a cached result. Returns nil if no cache hit.
func (c *Cache) Lookup(parentChainID, instructionHash string) *types.Layer {
	c.mu.Lock()
	entry, ok := c.entries[cacheKey(parentChainID, instructionHash)]
	c.mu.Unlock()

	if !ok {
		return nil
	}

	// Verify blob still exists on disk
	if entry.BlobPath != "" {
		if _, err := os.Stat(entry.BlobPath); err != nil {
			return nil // blob gone, cache miss
		}
	}

	return &types.Layer{
		DiffID:   types.Digest(entry.DiffID),
		Digest:   types.Digest(entry.Digest),
		Size:     entry.Size,
		BlobPath: entry.BlobPath,
	}
}

// Store records a cache entry.
func (c *Cache) Store(parentChainID, instructionHash string, layer *types.Layer) error {
	key := cacheKey(parentChainID, instructionHash)
	entry := CacheEntry{
		DiffID:   string(layer.DiffID),
		Digest:   string(layer.Digest),
		Size:     layer.Size,
		BlobPath: layer.BlobPath,
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()

	// Persist to disk if file path configured
	if c.filePath != "" {
		return c.saveToFile()
	}
	return nil
}

// Clear removes all cache entries.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string]CacheEntry, 64)
	c.mu.Unlock()

	if c.filePath != "" {
		os.Remove(c.filePath)
	}
}

// Size returns the number of cached entries.
func (c *Cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// loadFromFile reads cache entries from the JSON file.
func (c *Cache) loadFromFile() {
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return // no cache file yet, that's fine
	}

	var entries map[string]CacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return // corrupted cache, start fresh
	}

	c.mu.Lock()
	c.entries = entries
	c.mu.Unlock()
}

// saveToFile writes cache entries to the JSON file.
func (c *Cache) saveToFile() error {
	c.mu.Lock()
	data, err := json.MarshalIndent(c.entries, "", "  ")
	c.mu.Unlock()

	if err != nil {
		return fmt.Errorf("metadata: failed to marshal cache: %w", err)
	}

	return os.WriteFile(c.filePath, data, 0644)
}
