package main

import (
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/chinese-room-solutions/mass-sdk/install"
	"github.com/chinese-room-solutions/mass-sdk/tui"
)

// Field indices into buildFields() — kept in one place so the form, the scope
// reload trigger, and collectFrom agree on the order. Unlike MASS, Grimoire has
// nothing to configure before first launch: it has no listen address (the webview
// is local), and its data dir is the per-user Grimoire root that vaultdir owns —
// not an operator choice. The vault itself is picked in the app's empty state at
// runtime. So the form collects only where the app lives (scope + install dir).
const (
	fieldScope = iota
	fieldInstallDir
)

// collected is everything the form gathers. dataDir is not a form field — it's
// Grimoire's per-user data root, recorded so a re-run/uninstall knows where the
// app's data sits (it's never inside the install dir, so removal can't touch it).
type collected struct {
	scope      install.Scope
	installDir string
	dataDir    string
	perUser    bool
}

// prefill is the seed for the form's fields.
type prefill struct {
	scope      install.Scope
	installDir string
}

// dataRoot is Grimoire's per-user data directory (<cache>/grimoire), where the
// vault subdirs and app config live. Recorded in the install record; never a
// form field. Returns "" if the cache root can't be resolved — the install still
// proceeds (the record just omits it).
func dataRoot() string {
	root, err := vaultdir.Root()
	if err != nil {
		return ""
	}
	return root
}

// defaultCollected returns the per-OS defaults (used by the non-interactive
// install face before flags are overlaid).
func defaultCollected() collected {
	p := defaultPrefill()
	return collected{
		scope:      p.scope,
		installDir: p.installDir,
		dataDir:    dataRoot(),
		perUser:    p.scope == install.ScopeUser,
	}
}

// defaultPrefill is the per-OS factory default seed. The scope defaults to User
// (no elevation) — Grimoire is a user-launched desktop app — and the install dir
// follows from that scope. The operator can switch to System in the form, which
// moves the install dir to a machine-wide location and prompts for elevation.
func defaultPrefill() prefill {
	scope := install.AvailableScopes()[0] // User leads
	return prefill{
		scope:      scope,
		installDir: appSpec.ScopeInstallDir(scope),
	}
}

// scopeFromField parses the Scope choice field. The field is UI-constrained to
// valid scope labels, so a parse failure is a can't-happen invariant; we fall
// back to the leading scope (User) rather than fail the form re-seed.
func scopeFromField(fields []tui.Field) install.Scope {
	scope, err := install.ParseScope(fields[fieldScope].Value)
	if err != nil {
		return install.AvailableScopes()[0]
	}
	return scope
}

// prefillForScope re-seeds the install dir to the scope's default when the
// operator flips the Scope field.
func prefillForScope(fields []tui.Field) prefill {
	scope := scopeFromField(fields)
	return prefill{
		scope:      scope,
		installDir: appSpec.ScopeInstallDir(scope),
	}
}

// loadPrefill seeds the form from the install record (a prior install's
// location), falling back to per-OS defaults. The scope is inferred from the
// recorded install dir so the Scope field shows the prior install's scope.
func loadPrefill() prefill {
	p := defaultPrefill()
	if rec, err := appSpec.LoadRecord(); err == nil && rec != nil && rec.InstallDir != "" {
		p.installDir = rec.InstallDir
	}
	p.scope = scopeForInstallDir(p.installDir)
	return p
}

// scopeForInstallDir infers the scope from an install dir: a user-scoped path is
// ScopeUser, anything machine-wide is ScopeSystem.
func scopeForInstallDir(dir string) install.Scope {
	if install.IsUserScoped(dir) {
		return install.ScopeUser
	}
	return install.ScopeSystem
}

// buildFields builds the form's field list (in display order) from the prefill.
// Scope leads: it's the choice that drives the install dir default below.
func buildFields(p prefill) []tui.Field {
	return []tui.Field{
		fieldScope:      {Label: "Installation scope", Kind: tui.FieldChoice, Choices: scopeLabels(), Value: p.scope.Label()},
		fieldInstallDir: {Label: "Install directory", Kind: tui.FieldPath, Value: p.installDir},
	}
}

// scopeLabels is the Scope field's choice list, in AvailableScopes order.
func scopeLabels() []string {
	scopes := install.AvailableScopes()
	labels := make([]string, len(scopes))
	for i, s := range scopes {
		labels[i] = s.Label()
	}
	return labels
}

// collectFrom assembles the collected result from the edited fields. perUser
// follows the chosen scope, while the elevation gate (NeedsElevation) reads the
// actual install dir — so a System scope with a hand-edited home path still
// behaves correctly.
func collectFrom(fields []tui.Field) collected {
	scope := scopeFromField(fields)
	return collected{
		scope:      scope,
		installDir: fields[fieldInstallDir].Value,
		dataDir:    dataRoot(),
		perUser:    scope == install.ScopeUser,
	}
}
