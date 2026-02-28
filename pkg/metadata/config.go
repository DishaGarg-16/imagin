package metadata

import (
	"runtime"
	"time"

	"github.com/imagin/imagin/internal/types"
)

// ConfigBuilder incrementally constructs an OCI Image Configuration.
// Each Dockerfile instruction that affects config calls the corresponding
// method; the final result is retrieved via Build().
type ConfigBuilder struct {
	config types.ImageConfig
}

// NewConfigBuilder creates a ConfigBuilder with sensible defaults.
func NewConfigBuilder() *ConfigBuilder {
	return &ConfigBuilder{
		config: types.ImageConfig{
			Architecture: runtime.GOARCH,
			OS:           "linux",
			Config: types.ContainerConfig{
				Env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			},
			RootFS: types.RootFSConfig{
				Type:    "layers",
				DiffIDs: make([]string, 0, 8),
			},
			History: make([]types.HistoryEntry, 0, 16),
			Created: time.Now().UTC(),
		},
	}
}

// AddEnv adds or updates environment variables. Each kv is "KEY=VALUE".
func (b *ConfigBuilder) AddEnv(kvs ...string) {
	for _, kv := range kvs {
		// Check if key already exists — replace it
		key := envKey(kv)
		replaced := false
		for i, existing := range b.config.Config.Env {
			if envKey(existing) == key {
				b.config.Config.Env[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			b.config.Config.Env = append(b.config.Config.Env, kv)
		}
	}
}

// SetCmd sets the default command.
func (b *ConfigBuilder) SetCmd(cmd []string) {
	b.config.Config.Cmd = cmd
}

// SetEntrypoint sets the entrypoint.
func (b *ConfigBuilder) SetEntrypoint(ep []string) {
	b.config.Config.Entrypoint = ep
	// Setting ENTRYPOINT clears CMD (Docker behaviour)
	b.config.Config.Cmd = nil
}

// SetWorkdir sets the working directory.
func (b *ConfigBuilder) SetWorkdir(dir string) {
	b.config.Config.WorkingDir = dir
}

// SetUser sets the user/group.
func (b *ConfigBuilder) SetUser(user string) {
	b.config.Config.User = user
}

// AddExposedPort adds an exposed port (e.g. "8080/tcp").
func (b *ConfigBuilder) AddExposedPort(port string) {
	if b.config.Config.ExposedPorts == nil {
		b.config.Config.ExposedPorts = make(map[string]struct{})
	}
	b.config.Config.ExposedPorts[port] = struct{}{}
}

// AddVolume adds a volume mount point.
func (b *ConfigBuilder) AddVolume(path string) {
	if b.config.Config.Volumes == nil {
		b.config.Config.Volumes = make(map[string]struct{})
	}
	b.config.Config.Volumes[path] = struct{}{}
}

// AddLabel adds a label key=value pair.
func (b *ConfigBuilder) AddLabel(key, value string) {
	if b.config.Config.Labels == nil {
		b.config.Config.Labels = make(map[string]string)
	}
	b.config.Config.Labels[key] = value
}

// SetShell sets the default shell for RUN instructions.
func (b *ConfigBuilder) SetShell(shell []string) {
	b.config.Config.Shell = shell
}

// SetStopSignal sets the system call signal for stopping the container.
func (b *ConfigBuilder) SetStopSignal(signal string) {
	b.config.Config.StopSignal = signal
}

// AddDiffID appends a layer diff ID to the rootfs config.
func (b *ConfigBuilder) AddDiffID(diffID string) {
	b.config.RootFS.DiffIDs = append(b.config.RootFS.DiffIDs, diffID)
}

// AddHistory adds a history entry for a build step.
func (b *ConfigBuilder) AddHistory(createdBy string, emptyLayer bool) {
	b.config.History = append(b.config.History, types.HistoryEntry{
		CreatedBy:  createdBy,
		Created:    time.Now().UTC(),
		EmptyLayer: emptyLayer,
	})
}

// Build returns the finalized image configuration.
func (b *ConfigBuilder) Build() *types.ImageConfig {
	cfg := b.config // copy
	return &cfg
}

// envKey extracts the key portion of a "KEY=VALUE" string.
func envKey(kv string) string {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i]
		}
	}
	return kv
}
