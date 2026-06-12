// Package config handles loading and writing AgentDash configuration and
// resolving the locations of the AgentDash data directories.
//
// All paths are derived at runtime from the user's home directory or the
// AGENTDASH_HOME environment variable. No absolute paths are hardcoded.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// envHome lets users relocate the AgentDash data directory for testing or
// non-standard setups.
const envHome = "AGENTDASH_HOME"

// Config is the on-disk configuration stored at <home>/config.yaml.
type Config struct {
	// Repos is the list of git repositories whose worktrees are scanned.
	// Paths may be relative or use ~; they are expanded on load.
	Repos []string `yaml:"repos"`

	// Bases maps a repo path to the base branch used for ahead/behind and
	// mergeability checks. When a repo is absent, the base is auto-detected.
	Bases map[string]string `yaml:"bases,omitempty"`

	// Agents optionally restricts which agent adapters run. Empty means all
	// registered adapters are used.
	Agents []string `yaml:"agents,omitempty"`

	// RefreshSeconds controls the TUI poll interval. Defaults to 2.
	RefreshSeconds int `yaml:"refresh_seconds,omitempty"`
}

// Paths bundles the resolved on-disk locations AgentDash uses.
type Paths struct {
	Home        string // the AgentDash data root
	ConfigFile  string // <home>/config.yaml
	SessionsDir string // <home>/sessions  (optional JSON overlays)
	HistoryDir  string // <home>/history   (churn timeline)
}

// Home returns the AgentDash data root, honoring AGENTDASH_HOME and falling
// back to ~/.agentdash.
func Home() (string, error) {
	if h := os.Getenv(envHome); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".agentdash"), nil
}

// ResolvePaths computes all AgentDash data locations from Home.
func ResolvePaths() (Paths, error) {
	home, err := Home()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Home:        home,
		ConfigFile:  filepath.Join(home, "config.yaml"),
		SessionsDir: filepath.Join(home, "sessions"),
		HistoryDir:  filepath.Join(home, "history"),
	}, nil
}

// Default returns a Config with sensible defaults applied.
func Default() Config {
	return Config{
		Repos:          []string{},
		Bases:          map[string]string{},
		RefreshSeconds: 2,
	}
}

// Load reads and parses the config file, expanding repo paths. A missing file
// is reported as a clear, actionable error.
func Load() (Config, error) {
	p, err := ResolvePaths()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(p.ConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("config not found at %s — run 'agentdash init' first", p.ConfigFile)
		}
		return Config{}, fmt.Errorf("reading %s: %w", p.ConfigFile, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", p.ConfigFile, err)
	}
	if cfg.RefreshSeconds <= 0 {
		cfg.RefreshSeconds = 2
	}

	expanded := make([]string, 0, len(cfg.Repos))
	for _, r := range cfg.Repos {
		abs, err := ExpandPath(r)
		if err != nil {
			return Config{}, fmt.Errorf("invalid repo path %q: %w", r, err)
		}
		expanded = append(expanded, abs)
	}
	cfg.Repos = expanded
	return cfg, nil
}

// Save writes the config file, creating the data directories if needed.
func Save(cfg Config) error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if err := EnsureDirs(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.WriteFile(p.ConfigFile, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", p.ConfigFile, err)
	}
	return nil
}

// EnsureDirs creates the AgentDash data directories if they do not exist.
func EnsureDirs() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	for _, dir := range []string{p.Home, p.SessionsDir, p.HistoryDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}

// ExpandPath expands a leading ~ and resolves the path to an absolute,
// symlink-resolved form when possible.
func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if path == "~" || (len(path) >= 2 && path[:2] == "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve symlinks when the path exists so correlation join keys match.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}
