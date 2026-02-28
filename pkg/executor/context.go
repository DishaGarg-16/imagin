package executor

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// BuildContext manages access to the build context directory.
// It handles .dockerignore rules and provides scoped file access.
type BuildContext struct {
	root        string // absolute path to context directory
	ignoreRules []ignoreRule
}

type ignoreRule struct {
	pattern string
	negate  bool // lines starting with '!'
}

// NewBuildContext creates a build context from the given directory.
func NewBuildContext(root string) (*BuildContext, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	bc := &BuildContext{
		root:        abs,
		ignoreRules: make([]ignoreRule, 0, 16),
	}

	// Load .dockerignore if it exists
	ignorePath := filepath.Join(abs, ".dockerignore")
	bc.loadIgnoreFile(ignorePath)

	return bc, nil
}

// Root returns the absolute path to the context directory.
func (bc *BuildContext) Root() string {
	return bc.root
}

// IsIgnored checks if a relative path should be excluded based on .dockerignore.
func (bc *BuildContext) IsIgnored(relPath string) bool {
	relPath = filepath.ToSlash(relPath)

	ignored := false
	for _, rule := range bc.ignoreRules {
		matched, _ := filepath.Match(rule.pattern, relPath)
		if !matched {
			// Also try matching against just the basename
			matched, _ = filepath.Match(rule.pattern, filepath.Base(relPath))
		}
		if !matched {
			// Try as directory prefix
			if strings.HasSuffix(rule.pattern, "/") || strings.HasSuffix(rule.pattern, "/*") {
				prefix := strings.TrimSuffix(strings.TrimSuffix(rule.pattern, "/*"), "/")
				matched = strings.HasPrefix(relPath, prefix+"/") || relPath == prefix
			}
		}
		if matched {
			if rule.negate {
				ignored = false
			} else {
				ignored = true
			}
		}
	}
	return ignored
}

// WalkFiles walks all non-ignored files in the build context.
func (bc *BuildContext) WalkFiles(fn func(rel string, info os.FileInfo) error) error {
	return filepath.Walk(bc.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(bc.root, path)
		if rel == "." {
			return nil
		}

		if bc.IsIgnored(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		return fn(rel, info)
	})
}

// loadIgnoreFile parses a .dockerignore file.
func (bc *BuildContext) loadIgnoreFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // no .dockerignore, that's fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = strings.TrimPrefix(line, "!")
		}
		rule.pattern = line
		bc.ignoreRules = append(bc.ignoreRules, rule)
	}
}
