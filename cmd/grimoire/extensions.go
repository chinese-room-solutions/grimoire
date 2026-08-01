package main

// The Extensions dialog's list fragments: one GET per tab, rendering the same
// grimoireapi listings the JSON API and the CLI use into the dialog's panels
// over Datastar SSE (the modelOptionsHandler pattern). Install and remove are
// not repeated here — the dialog's buttons call the /api/v1/{theme,kernel}/*
// JSON endpoints directly, because the clicking session must also update its
// own palette dropdown, which only the browser can do (Datastar v1 has no
// execute-script event). Installing does NOT activate a theme — the user does
// that explicitly, from the row or the palette.

import (
	"net/http"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/kernel"
	"github.com/chinese-room-solutions/grimoire/internal/ui"
	"github.com/rs/zerolog"
	"github.com/starfederation/datastar-go/datastar"
)

// extensionThemesHandler renders the Themes tab: the registered themes
// (built-ins first, then pluggable ones) above the registry packages not yet
// installed. A registry failure degrades to the listing's warning — the
// installed section still renders, so the tab works offline.
func extensionThemesHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		sections := ui.ExtensionSections{Kind: ui.ExtKindTheme}
		res, err := api.ThemeList(r.Context())
		if err != nil {
			logger.Warn().Err(err).Msg("listing themes for the extensions dialog")
			sections.Warning = shortErr(err)
		} else {
			sections.Warning = res.Warning
			for _, t := range res.Installed {
				item := ui.ExtensionItem{ID: t.Name, Label: t.Label, Meta: t.Base}
				if t.Builtin {
					item.Locked = "built-in"
				}
				sections.Installed = append(sections.Installed, item)
			}
			for _, p := range res.Available {
				if p.Installed {
					continue
				}
				label := p.DisplayName
				if label == "" {
					label = p.ID
				}
				sections.Available = append(sections.Available, ui.ExtensionItem{
					ID: p.ID, Package: p.Name, Label: label, Meta: p.Version, Version: p.Version,
					Desc: p.Description,
				})
			}
		}
		_ = sse.PatchElementTempl(ui.ExtensionList(sections),
			datastar.WithSelector("#g-ext-themes"), datastar.WithModeInner())
	}
}

// extensionKernelsHandler renders the Kernels tab: the kernels this vault
// resolves against above the registry packages it hasn't installed. Built-in
// kernels (the bash one) and kernels living in the vault's own kernels dir are
// managed outside the registry, so they're locked rather than removable.
func extensionKernelsHandler(api *grimoireapi.API, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		sections := ui.ExtensionSections{Kind: ui.ExtKindKernel}
		res, err := api.KernelList(r.Context())
		if err != nil {
			logger.Warn().Err(err).Msg("listing kernels for the extensions dialog")
			sections.Warning = shortErr(err)
		} else {
			sections.Warning = res.Warning
			for _, k := range res.Installed {
				label := k.DisplayName
				if label == "" {
					label = k.Family
				}
				sections.Installed = append(sections.Installed, ui.ExtensionItem{
					ID: k.Family, Label: label, Meta: k.Version, Version: k.Version,
					Locked: kernelLock(k.Source),
				})
			}
			for _, p := range res.Available {
				if p.Installed {
					continue
				}
				label := p.DisplayName
				if label == "" {
					label = p.Family
				}
				sections.Available = append(sections.Available, ui.ExtensionItem{
					ID: p.Family, Package: p.Name, Label: label, Meta: p.Version, Version: p.Version,
					Desc: p.Description,
				})
			}
		}
		_ = sse.PatchElementTempl(ui.ExtensionList(sections),
			datastar.WithSelector("#g-ext-kernels"), datastar.WithModeInner())
	}
}

// kernelLock names why an installed kernel can't be removed from the dialog, or
// returns "" for the shared-dir kernels the API does manage.
func kernelLock(source string) string {
	switch kernel.Source(source) {
	case kernel.SourceBuiltin:
		return "built-in"
	case kernel.SourceVault:
		return "in this vault"
	default:
		return ""
	}
}
