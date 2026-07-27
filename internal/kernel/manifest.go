package kernel

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrBadManifest is returned when a kernel manifest is missing required fields or
// can't be decoded.
var ErrBadManifest = errors.New("invalid kernel manifest")

// ErrKernelUnavailable is returned when a kernel is configured but its command
// can't be found on this machine (e.g. bash absent on a Windows box without
// Git Bash). It is distinct from "no kernel for this language" so the UI can tell
// the user to install the interpreter rather than that the language is unsupported.
var ErrKernelUnavailable = errors.New("kernel command not found")

// command is one OS's spawn recipe: the executable (resolved on PATH) and its
// args. The token "{runner}" in args is replaced with the runner script's path.
type command struct {
	Exe  string   `yaml:"exe"`
	Args []string `yaml:"args"`
}

// Manifest is a decoded kernel spec — one toolchain version's runner. Identity
// comes from the on-disk path, not the YAML: a kernel lives at
// kernels/<family>/<version>/, so Family and Version are set by the loader from
// that path (e.g. family "go", version "1.21"). Match lists the fenced-code
// info-strings it claims (e.g. go, golang). Command is keyed by GOOS with a
// "default" fallback. DisplayName is an optional friendly label. dir is the
// manifest's own directory, used to resolve the runner script path.
type Manifest struct {
	Language    string             `yaml:"language"`
	DisplayName string             `yaml:"display_name,omitempty"`
	Match       []string           `yaml:"match"`
	Runner      string             `yaml:"runner"`
	Command     map[string]command `yaml:"command"`

	Family  string // toolchain family, the parent folder (e.g. "go").
	Version string // version, the leaf folder (e.g. "1.21").
	dir     string
}

// Name is the unique selection key: "family@version" (e.g. "go@1.21"). It keys
// the session map, dedup, and logs — one per installed kernel.
func (m *Manifest) Name() string {
	return m.Family + "@" + m.Version
}

// Label is the friendly name: DisplayName (plus version) if set, else the bare
// "family version" pair.
func (m *Manifest) Label() string {
	if m.DisplayName != "" {
		return m.DisplayName + " " + m.Version
	}
	return m.Family + " " + m.Version
}

// loadManifest decodes a manifest from YAML and stamps its path-derived identity:
// family and version come from the kernel's folder (kernels/<family>/<version>/),
// dir is that folder (for resolving the runner). It validates the YAML fields and
// lowercases the match strings so lookups are case-insensitive.
func loadManifest(data []byte, dir, family, version string) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	m.dir = dir
	m.Family = family
	m.Version = version
	if family == "" || version == "" {
		return nil, fmt.Errorf("%w: kernel must live at kernels/<family>/<version>/", ErrBadManifest)
	}
	if len(m.Match) == 0 || m.Runner == "" {
		return nil, fmt.Errorf("%w: match and runner are required", ErrBadManifest)
	}
	if _, ok := m.resolveCommand(); !ok {
		return nil, fmt.Errorf("%w: no command for %s or default", ErrBadManifest, runtime.GOOS)
	}
	for i, lang := range m.Match {
		m.Match[i] = strings.ToLower(lang)
	}
	return &m, nil
}

// resolveCommand returns the command for this OS, preferring a GOOS-specific entry
// over "default". ok is false when neither exists.
func (m *Manifest) resolveCommand() (command, bool) {
	if c, ok := m.Command[runtime.GOOS]; ok {
		return c, true
	}
	c, ok := m.Command["default"]
	return c, ok
}

// spawnCommand resolves the runnable executable and args for this OS: it looks the
// executable up on PATH and substitutes "{runner}" with the runner script's path.
// It returns ErrKernelUnavailable when the executable isn't installed.
func (m *Manifest) spawnCommand() (exe string, args []string, err error) {
	c, ok := m.resolveCommand()
	if !ok {
		return "", nil, fmt.Errorf("%w: %s", ErrBadManifest, runtime.GOOS)
	}
	exe, err = lookExe(c.Exe)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %s", ErrKernelUnavailable, c.Exe)
	}
	runner := filepath.Join(m.dir, m.Runner)
	args = make([]string, len(c.Args))
	for i, a := range c.Args {
		args[i] = strings.ReplaceAll(a, "{runner}", runner)
	}
	return exe, args, nil
}
