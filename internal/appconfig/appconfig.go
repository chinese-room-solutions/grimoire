// Package appconfig holds Grimoire-specific settings (the vault path and the
// embedding model) persisted alongside the SDK's app config. The SDK owns log
// level + theme in config.json; these live in grimoire.json next to it.
package appconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chinese-room-solutions/mass-sdk/fsutil"
)

const fileName = "grimoire.json"

// Config is the persisted app state Grimoire owns.
type Config struct {
	// Vault is the absolute path to the folder of Markdown notes.
	Vault string `json:"vault,omitempty"`
	// EmbedModel is the gateway model id used to embed chunks. The vector index
	// is bound to this model's dimension; changing it requires a reindex.
	EmbedModel string `json:"embedModel,omitempty"`
	// EmbedQueryPrefix and EmbedDocPrefix override the instruction prefixes
	// prepended to search queries and note chunks before embedding. Empty means
	// auto-detect from the model id's family (Qwen3/nomic/e5/bge); forcing an
	// explicitly empty prefix on a detected family is not expressible. Changing
	// the document prefix rebuilds the index.
	EmbedQueryPrefix string `json:"embedQueryPrefix,omitempty"`
	EmbedDocPrefix   string `json:"embedDocPrefix,omitempty"`
	// ConvertModel is the gateway model id (a vision/structurizer GGUF) used to
	// convert imported PDFs to Markdown.
	ConvertModel string `json:"convertModel,omitempty"`
	// ConvertMaxPixels caps how many pixels a rendered PDF page image keeps
	// before it goes to the convert model (downscaled preserving aspect ratio).
	// 0 means the pdfconvert default — the model's training resolution.
	ConvertMaxPixels int `json:"convertMaxPixels,omitempty"`
	// ConvertPageTimeoutSec is how long one PDF page may take to convert before
	// the job is cancelled and the page fails. 0 means the pdfconvert default;
	// raise it for deliberately slow hardware.
	ConvertPageTimeoutSec int `json:"convertPageTimeoutSec,omitempty"`
	// IndexConcurrency is how many notes a full vault reindex embeds at once.
	// 0 means use the indexer's default.
	IndexConcurrency int `json:"indexConcurrency,omitempty"`
	// TrashMode controls whether deleting a note moves it to the vault's .trash/
	// folder (restorable) or removes it permanently, and for whom. An empty value
	// (never configured) reads as the default, TrashAll. Use Trashes to resolve it
	// for a given caller.
	TrashMode TrashMode `json:"trashMode,omitempty"`
}

// TrashMode is the soft-delete policy: who gets the restorable trash on delete.
type TrashMode string

const (
	// TrashAll soft-deletes for everyone (the default).
	TrashAll TrashMode = "all"
	// TrashAgents soft-deletes only for AI-agent (API) deletes; the user's
	// own deletes in the GUI are permanent.
	TrashAgents TrashMode = "agents"
	// TrashOff removes permanently for everyone.
	TrashOff TrashMode = "off"
)

// TrashModeOrDefault returns the configured mode, defaulting to TrashAll when
// unset or unrecognized — for seeding the settings UI.
func (c Config) TrashModeOrDefault() TrashMode {
	return c.trashMode()
}

// trashMode returns the configured mode, defaulting to TrashAll when unset or
// unrecognized.
func (c Config) trashMode() TrashMode {
	switch c.TrashMode {
	case TrashAll, TrashAgents, TrashOff:
		return c.TrashMode
	default:
		return TrashAll
	}
}

// Trashes reports whether a delete should soft-delete to the trash, given who is
// deleting: byAgent is true for AI-agent (API) deletes, false for the user's
// own GUI deletes.
func (c Config) Trashes(byAgent bool) bool {
	switch c.trashMode() {
	case TrashAll:
		return true
	case TrashAgents:
		return byAgent
	default: // TrashOff
		return false
	}
}

// Load reads the config from dir, returning a zero Config if absent.
func Load(dir string) Config {
	var cfg Config
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg) // a corrupt file degrades to defaults.
	return cfg
}

// Save writes the config to dir.
func Save(dir string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, fileName), data, 0o600)
}
