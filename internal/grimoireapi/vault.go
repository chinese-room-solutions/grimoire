package grimoireapi

import (
	"context"
	"errors"
	"path/filepath"
)

// ErrSwitchUnsupported is returned by OpenVault on an API whose vault is fixed
// for its lifetime (no open hook was wired) — there is nothing to switch.
var ErrSwitchUnsupported = errors.New("vault switching is not supported by this instance")

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
	current := a.currentVault()
	return Vault{Name: filepath.Base(current), Path: current, Current: true}, nil
}

// CurrentVault reports the vault a caller that names none acts on, with ok=false
// when there is none yet (a first run).
func (a *API) CurrentVault(ctx context.Context) (Vault, bool) {
	current := a.currentVault()
	if current == "" {
		return Vault{}, false
	}
	return Vault{Name: filepath.Base(current), Path: current, Current: true}, true
}
