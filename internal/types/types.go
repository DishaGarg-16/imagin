// Package types defines shared types and interfaces used across all
// components of the IMAGIN image builder.
package types

import (
	"context"
	"io"
	"time"
)

// ---------------------------------------------------------------------------
// Dockerfile instruction types
// ---------------------------------------------------------------------------

// InstructionType represents a Dockerfile instruction keyword.
type InstructionType string

const (
	InstructionFrom        InstructionType = "FROM"
	InstructionRun         InstructionType = "RUN"
	InstructionCopy        InstructionType = "COPY"
	InstructionAdd         InstructionType = "ADD"
	InstructionCmd         InstructionType = "CMD"
	InstructionEntrypoint  InstructionType = "ENTRYPOINT"
	InstructionEnv         InstructionType = "ENV"
	InstructionExpose      InstructionType = "EXPOSE"
	InstructionVolume      InstructionType = "VOLUME"
	InstructionWorkdir     InstructionType = "WORKDIR"
	InstructionUser        InstructionType = "USER"
	InstructionArg         InstructionType = "ARG"
	InstructionLabel       InstructionType = "LABEL"
	InstructionShell       InstructionType = "SHELL"
	InstructionStopsignal  InstructionType = "STOPSIGNAL"
	InstructionHealthcheck InstructionType = "HEALTHCHECK"
	InstructionOnbuild     InstructionType = "ONBUILD"
	InstructionMaintainer  InstructionType = "MAINTAINER"
)

// Instruction represents a single parsed Dockerfile instruction.
type Instruction struct {
	Type  InstructionType   // e.g. FROM, RUN, COPY
	Args  []string          // positional arguments
	Flags map[string]string // e.g. --from=builder, --chown=user
	Raw   string            // original line text for cache key
	Line  int               // 1-based line number in Dockerfile
	Stage int               // build stage index (incremented at each FROM)
}

// BuildStage represents one FROM ... block in a multi-stage Dockerfile.
type BuildStage struct {
	Name         string        // alias (FROM ... AS <name>), empty if unnamed
	BaseName     string        // image reference after FROM
	BaseTag      string        // tag portion, default "latest"
	Instructions []Instruction // instructions belonging to this stage
	Index        int           // 0-based stage index
}

// Dockerfile is the fully parsed representation of a Dockerfile.
type Dockerfile struct {
	Stages     []BuildStage  // build stages in order
	GlobalArgs []Instruction // ARG instructions before the first FROM
}

// ---------------------------------------------------------------------------
// Layer and image types
// ---------------------------------------------------------------------------

// Digest is a content-addressable identifier, e.g. "sha256:abc123...".
type Digest string

// Layer represents a single filesystem layer in the image.
type Layer struct {
	DiffID     Digest // digest of the *uncompressed* tar
	Digest     Digest // digest of the compressed tar (distribution digest)
	Size       int64  // compressed size in bytes
	UncompSize int64  // uncompressed size in bytes
	MediaType  string // e.g. "application/vnd.oci.image.layer.v1.tar+gzip"
	CreatedBy  string // instruction that created this layer
	CreatedAt  time.Time
	BlobPath   string // path to the layer blob on disk
}

// ImageConfig holds the runtime configuration for the built image.
// This maps to the OCI Image Configuration specification.
type ImageConfig struct {
	Architecture string          `json:"architecture"`
	OS           string          `json:"os"`
	Config       ContainerConfig `json:"config"`
	RootFS       RootFSConfig    `json:"rootfs"`
	History      []HistoryEntry  `json:"history"`
	Created      time.Time       `json:"created"`
}

// ContainerConfig holds runtime-settable fields.
type ContainerConfig struct {
	Env          []string            `json:"Env,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	User         string              `json:"User,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Volumes      map[string]struct{} `json:"Volumes,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	Shell        []string            `json:"Shell,omitempty"`
	StopSignal   string              `json:"StopSignal,omitempty"`
}

// RootFSConfig describes the layer chain.
type RootFSConfig struct {
	Type    string   `json:"type"` // always "layers"
	DiffIDs []string `json:"diff_ids"`
}

// HistoryEntry records how each layer was created.
type HistoryEntry struct {
	CreatedBy  string    `json:"created_by"`
	Created    time.Time `json:"created"`
	EmptyLayer bool      `json:"empty_layer,omitempty"`
	Comment    string    `json:"comment,omitempty"`
}

// BuildResult is the output of a complete build.
type BuildResult struct {
	ImageConfig ImageConfig
	Layers      []Layer
	OutputPath  string
}

// ---------------------------------------------------------------------------
// Component interfaces
// ---------------------------------------------------------------------------

// RootFSManager manages the layered root filesystem for build steps.
type RootFSManager interface {
	// Prepare sets up a writable layer on top of existing layers.
	// Returns the path to the merged root and the upper (writable) dir.
	Prepare(ctx context.Context, lowerDirs []string) (mergedDir string, upperDir string, err error)

	// Commit finalises the current layer and returns the upper dir contents.
	Commit(ctx context.Context, mergedDir string) error

	// Cleanup tears down mounts / temp dirs for the given merged path.
	Cleanup(ctx context.Context, mergedDir string) error

	// Close releases all resources held by the manager.
	Close() error
}

// Snapshotter captures filesystem changes into compressed tar layers.
type Snapshotter interface {
	// Snapshot creates a layer from the filesystem changes in upperDir.
	// It returns the Layer metadata (including digests and blob path).
	Snapshot(ctx context.Context, upperDir string, createdBy string) (*Layer, error)
}

// MetadataStore stores and retrieves image metadata and build cache.
type MetadataStore interface {
	// AddLayer records a layer in the store.
	AddLayer(layer *Layer) error

	// GetLayers returns all layers in order.
	GetLayers() []*Layer

	// SetConfig stores the image configuration.
	SetConfig(config *ImageConfig) error

	// GetConfig returns the current image configuration.
	GetConfig() *ImageConfig

	// CacheLookup checks if an instruction result is cached.
	// Returns the cached layer if found, nil otherwise.
	CacheLookup(parentChainID string, instructionHash string) *Layer

	// CacheStore records a build result in the cache.
	CacheStore(parentChainID string, instructionHash string, layer *Layer) error
}

// Exporter writes the final image in a specified format.
type Exporter interface {
	// Export writes the image (layers + config) to the output path.
	Export(ctx context.Context, config *ImageConfig, layers []*Layer, outputPath string) error
}

// MetricsTracker instruments build phases for latency measurement.
type MetricsTracker interface {
	// StartPhase begins timing a named phase.
	StartPhase(name string)

	// EndPhase stops timing the named phase and records the duration.
	EndPhase(name string)

	// StartSubPhase begins timing a sub-phase within the current phase.
	StartSubPhase(parent string, name string)

	// EndSubPhase stops timing a sub-phase.
	EndSubPhase(parent string, name string)

	// RecordAlloc takes a snapshot of current memory statistics.
	RecordAlloc()

	// WriteTo writes the formatted report to the given writer.
	// Implements io.WriterTo.
	WriteTo(w io.Writer) (int64, error)

	// WriteJSON writes metrics as JSON to the given writer.
	WriteJSON(w io.Writer) error
}
