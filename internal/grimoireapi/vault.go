package grimoireapi

import (
	"context"
	"errors"
	"path/filepath"
)

// ErrSwitchUnsupported is returned by OpenVault/CloseVault on a backend whose
// vault is fixed for its lifetime (no bind/unbind hooks were wired). The runtime
// open/switch/close surface only exists where a holder can swap the binding.
var ErrSwitchUnsupported = errors.New("vault switching is not supported by this instance")

// OpenVault binds the backend to the vault at path, replacing any vault currently
// open (the previous one is closed). It is how an agent navigates between vaults
// without spawning a separate instance. Returns the now-current vault.
func (a *API) OpenVault(ctx context.Context, path string) (Vault, error) {
	if a.bind == nil {
		return Vault{}, ErrSwitchUnsupported
	}
	if path == "" {
		return Vault{}, errors.New("vault path is required")
	}
	if err := a.bind(ctx, path); err != nil {
		return Vault{}, err
	}
	current := a.currentVault()
	return Vault{Name: filepath.Base(current), Path: current, Current: true}, nil
}

// SwitchVault is OpenVault under the name an agent reaches for when a vault is
// already open: binding a new vault replaces the old one, so the two are the same
// operation.
func (a *API) SwitchVault(ctx context.Context, path string) (Vault, error) {
	return a.OpenVault(ctx, path)
}

// CloseVault returns the backend to the empty state (no vault open).
func (a *API) CloseVault(ctx context.Context) error {
	if a.unbind == nil {
		return ErrSwitchUnsupported
	}
	return a.unbind()
}

// CurrentVault reports the vault the backend currently has open, with ok=false
// when none is bound (the empty state).
func (a *API) CurrentVault(ctx context.Context) (Vault, bool) {
	current := a.currentVault()
	if current == "" {
		return Vault{}, false
	}
	return Vault{Name: filepath.Base(current), Path: current, Current: true}, true
}
