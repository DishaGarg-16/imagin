package metadata

import (
	"testing"
	"time"

	"github.com/imagin/imagin/internal/types"
)

func TestStoreAddGetLayers(t *testing.T) {
	store := NewStore()

	l1 := &types.Layer{
		DiffID:    "sha256:aaa",
		Digest:    "sha256:bbb",
		Size:      1024,
		CreatedBy: "RUN echo hello",
		CreatedAt: time.Now(),
	}
	l2 := &types.Layer{
		DiffID:    "sha256:ccc",
		Digest:    "sha256:ddd",
		Size:      2048,
		CreatedBy: "COPY . /app",
		CreatedAt: time.Now(),
	}

	if err := store.AddLayer(l1); err != nil {
		t.Fatal(err)
	}
	if err := store.AddLayer(l2); err != nil {
		t.Fatal(err)
	}

	layers := store.GetLayers()
	if len(layers) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(layers))
	}
	if layers[0].DiffID != "sha256:aaa" {
		t.Errorf("expected first layer diffID sha256:aaa, got %s", layers[0].DiffID)
	}
	if layers[1].DiffID != "sha256:ccc" {
		t.Errorf("expected second layer diffID sha256:ccc, got %s", layers[1].DiffID)
	}
}

func TestStoreConfig(t *testing.T) {
	store := NewStore()

	// Initially nil
	if store.GetConfig() != nil {
		t.Error("expected nil config initially")
	}

	cfg := &types.ImageConfig{
		Architecture: "amd64",
		OS:           "linux",
	}
	if err := store.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}

	got := store.GetConfig()
	if got.Architecture != "amd64" {
		t.Errorf("expected amd64, got %s", got.Architecture)
	}
}

func TestStoreCacheLookup(t *testing.T) {
	store := NewStore()

	layer := &types.Layer{
		DiffID: "sha256:cached",
		Digest: "sha256:cachedgz",
		Size:   512,
	}

	// Cache miss
	if store.CacheLookup("parent1", "instrA") != nil {
		t.Error("expected cache miss")
	}

	// Store and hit
	if err := store.CacheStore("parent1", "instrA", layer); err != nil {
		t.Fatal(err)
	}

	hit := store.CacheLookup("parent1", "instrA")
	if hit == nil {
		t.Fatal("expected cache hit")
	}
	if hit.DiffID != "sha256:cached" {
		t.Errorf("expected sha256:cached, got %s", hit.DiffID)
	}

	// Different key → miss
	if store.CacheLookup("parent1", "instrB") != nil {
		t.Error("expected cache miss for different instruction")
	}
}

func TestConfigBuilder(t *testing.T) {
	b := NewConfigBuilder()

	b.AddEnv("APP_HOME=/app", "DEBUG=1")
	b.SetWorkdir("/app")
	b.SetCmd([]string{"./start"})
	b.SetUser("nobody")
	b.AddExposedPort("8080/tcp")
	b.AddLabel("version", "1.0")
	b.AddDiffID("sha256:layer1")
	b.AddDiffID("sha256:layer2")
	b.AddHistory("RUN echo hello", false)
	b.AddHistory("ENV DEBUG=1", true)

	cfg := b.Build()

	if cfg.Config.WorkingDir != "/app" {
		t.Errorf("expected WorkingDir=/app, got %s", cfg.Config.WorkingDir)
	}
	if len(cfg.Config.Cmd) != 1 || cfg.Config.Cmd[0] != "./start" {
		t.Errorf("unexpected Cmd: %v", cfg.Config.Cmd)
	}
	if cfg.Config.User != "nobody" {
		t.Errorf("expected User=nobody, got %s", cfg.Config.User)
	}
	if len(cfg.RootFS.DiffIDs) != 2 {
		t.Errorf("expected 2 diff IDs, got %d", len(cfg.RootFS.DiffIDs))
	}
	if len(cfg.History) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(cfg.History))
	}
	if !cfg.History[1].EmptyLayer {
		t.Error("expected second history entry to be empty layer")
	}
}

func TestConfigBuilderEnvReplace(t *testing.T) {
	b := NewConfigBuilder()
	b.AddEnv("PATH=/usr/bin")
	b.AddEnv("PATH=/custom/bin") // should replace

	cfg := b.Build()
	pathCount := 0
	for _, env := range cfg.Config.Env {
		if len(env) >= 5 && env[:5] == "PATH=" {
			pathCount++
			if env != "PATH=/custom/bin" {
				t.Errorf("PATH not updated: %s", env)
			}
		}
	}
	if pathCount != 1 {
		t.Errorf("expected 1 PATH entry, got %d", pathCount)
	}
}

func TestComputeChainID(t *testing.T) {
	single := ComputeChainID([]types.Digest{"sha256:abc"})
	if single != "sha256:abc" {
		t.Errorf("single layer chain ID should equal diff ID, got %s", single)
	}

	multi := ComputeChainID([]types.Digest{"sha256:abc", "sha256:def"})
	if multi == "" || multi == "sha256:abc" {
		t.Error("multi-layer chain ID should be a new hash")
	}
}

func TestBuildCache(t *testing.T) {
	c := NewCache("") // in-memory only

	layer := &types.Layer{
		DiffID:   "sha256:x",
		Digest:   "sha256:y",
		Size:     100,
		BlobPath: "", // no file check for in-memory
	}

	if c.Lookup("p", "i") != nil {
		t.Error("expected cache miss")
	}

	if err := c.Store("p", "i", layer); err != nil {
		t.Fatal(err)
	}

	if c.Size() != 1 {
		t.Errorf("expected size 1, got %d", c.Size())
	}

	hit := c.Lookup("p", "i")
	if hit == nil {
		t.Fatal("expected cache hit")
	}
	if hit.DiffID != "sha256:x" {
		t.Errorf("unexpected diff ID: %s", hit.DiffID)
	}

	c.Clear()
	if c.Size() != 0 {
		t.Error("expected empty cache after clear")
	}
}

func BenchmarkCacheLookup(b *testing.B) {
	store := NewStore()
	layer := &types.Layer{DiffID: "sha256:bench", Digest: "sha256:benchgz", Size: 100}
	store.CacheStore("parent", "instr", layer)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.CacheLookup("parent", "instr")
	}
}
