package grimoireapi

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
)

// ErrSwitchUnsupported is returned by OpenVault on an API whose vault is fixed
// for its lifetime (no open hook was wired) — there is nothing to switch.
var ErrSwitchUnsupported = errors.New("vault switching is not supported by this instance")

// Vault is one vault's status: what it's called, where it is, and what state
// Grimoire has it in. It answers both "which vaults can I navigate to" (an agent
// picking a --vault) and "what shape is this vault in" (the GUI's Vaults tab and
// `grimoire vault list`).
//
// The index fields are best-effort snapshots, not guarantees: Chunks is reported
// only for a vault whose runtime is resident — listing vaults never forces a
// store open — and LastSync comes from the index file's timestamp.
type Vault struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Current marks the vault a caller that names none acts on.
	Current bool `json:"current"`
	// Available reports that the vault's folder is still on disk. A vault that
	// was moved, deleted or unmounted stays listed (so it can be forgotten) but
	// can't be opened.
	Available bool `json:"available"`
	// Chunks is the indexed chunk count, omitted when the vault's store isn't
	// open — 0 here means "not known", not "empty index".
	Chunks int `json:"chunks,omitempty"`
	// EmbedModel is the vault's configured embedding model, empty when it has
	// none yet.
	EmbedModel string `json:"embedModel,omitempty"`
	// LastSync is when the index was last written, RFC3339; empty when the vault
	// has never been indexed.
	LastSync string `json:"lastSync,omitempty"`
}

// ListVaults returns every vault in Grimoire's registry with its status,
// flagging the one a caller that names no vault acts on. An agent uses it to
// discover which vaults exist, then names one on each call (or makes it the
// default with OpenVault); the GUI's Vaults tab and `grimoire vault list` render
// it directly. It works before any vault has been opened, so it's the entry
// point from a fresh install.
//
// Vaults whose folder is gone are listed too, marked unavailable — they are what
// ForgetVault exists to clear.
func (a *API) ListVaults(ctx context.Context) ([]Vault, error) {
	paths, err := vaultdir.RecordedVaults()
	if err != nil {
		return nil, err
	}
	current := a.currentVault()
	var live map[string]*app.Service
	if a.live != nil {
		live = a.live()
	}
	out := make([]Vault, len(paths))
	for i, p := range paths {
		out[i] = vaultStatus(p, current, live)
	}
	return out, nil
}

// ForgetVault drops a vault from the registry and retires its resident runtime.
// Forgetting is not deleting: the folder and every note in it stay on disk, as
// do the vault's own data and index dirs, so opening the path again restores
// everything. A path Grimoire doesn't know is a no-op. Forgetting the current
// vault repoints the default at another known one.
func (a *API) ForgetVault(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("vault path is required")
	}
	if a.closeVault != nil {
		a.closeVault(path)
	}
	return vaultdir.Forget(path)
}

// OpenVault opens the vault at path in the daemon and makes it the one a caller
// that names no vault acts on (what the GUI reopens, and what a bare CLI verb
// targets). Other vaults stay open — an agent normally just names the vault it
// wants; this is for changing the default. Returns the now-current vault.
func (a *API) OpenVault(ctx context.Context, path string) (Vault, error) {
	if a.open == nil {
		return Vault{}, ErrSwitchUnsupported
	}
	if path == "" {
		return Vault{}, errors.New("vault path is required")
	}
	if err := a.open(ctx, path); err != nil {
		return Vault{}, err
	}
	return a.describe(a.currentVault()), nil
}

// CurrentVault reports the vault a caller that names none acts on, with ok=false
// when there is none yet (a first run).
func (a *API) CurrentVault(ctx context.Context) (Vault, bool) {
	current := a.currentVault()
	if current == "" {
		return Vault{}, false
	}
	return a.describe(current), true
}

// describe is vaultStatus for a single vault, so one vault reports the same
// fields whether it came from a list or from OpenVault/CurrentVault.
func (a *API) describe(path string) Vault {
	var live map[string]*app.Service
	if a.live != nil {
		live = a.live()
	}
	return vaultStatus(path, path, live)
}

// vaultStatus assembles one vault's status from disk plus, when its runtime is
// resident, the live index. current is the vault the default points at; live
// maps canonical vault paths to their resident services.
func vaultStatus(path, current string, live map[string]*app.Service) Vault {
	v := Vault{Name: vaultdir.Name(path), Path: path}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		v.Available = true
	}
	key, err := vaultdir.Canonical(path)
	if err != nil {
		return v
	}
	if ck, err := vaultdir.Canonical(current); err == nil {
		v.Current = ck == key
	}
	if svc := live[key]; svc != nil {
		v.EmbedModel = svc.Config().EmbedModel
		// A store that can't be counted (a broken index file) reports no count
		// rather than failing the whole listing.
		if n, cerr := svc.Count(); cerr == nil {
			v.Chunks = n
		}
	} else if dir, derr := vaultdir.DataPath(path); derr == nil {
		v.EmbedModel = appconfig.Load(dir).EmbedModel
	}
	v.LastSync = lastSync(path, v.EmbedModel)
	return v
}

// lastSync is when the vault's index for model was last written, RFC3339, or ""
// when it has never been indexed.
//
// It's the index file's mtime rather than a recorded timestamp: that costs one
// stat, needs no store open and no schema, and survives the index being
// rebuilt. The tradeoff is that it tracks "the index file was written", so a
// pass that changed nothing can still move it, and a checkpoint of an unrelated
// write would too.
func lastSync(vault, model string) string {
	if model == "" {
		return "" // no model, no index file to look for.
	}
	dir, err := vaultdir.CachePath(vault)
	if err != nil {
		return ""
	}
	info, err := os.Stat(app.IndexPath(dir, model))
	if err != nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339)
}
