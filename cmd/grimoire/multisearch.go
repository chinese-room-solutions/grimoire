package main

import (
	"context"
	"fmt"
	"sort"
	"sync"

	grimoireapp "github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
)

// Cross-vault search. Grimoire searches every vault it knows about by default:
// the user's knowledge is one body however it is foldered, so naming a vault is
// the narrowing, not the norm.
//
// Fusion is rank-based only. Each vault ranks its own hits with its own
// embedding model, and similarities from different models are not comparable
// (each compresses relevance into its own narrow band), so the global ranking
// re-fuses the per-leg *positions* every hit reports — what store.Hit carries
// VecRank/FTSRank for.

// rrfK is the Reciprocal Rank Fusion constant, matching the store's own: a leg
// contributes 1/(rrfK+rank) at 1-based rank. Re-fusing one vault's hits with it
// reproduces that vault's order, so a single-vault cross-vault search ranks
// exactly like a plain store search.
const rrfK = 60

// vaultHit is a search hit plus the vault it came from (its canonical absolute
// path), which is what a caller needs to label, preview, or open it.
type vaultHit struct {
	store.Hit
	Vault string
}

// multiSearch runs one query across every vault the daemon can serve and fuses
// the per-vault rankings into one. k caps the result count and minSim is the
// vector leg's similarity floor, both as for a single-vault search.
//
// A vault that can't answer (folder gone, no model, index still opening) is
// skipped with a warning rather than failing the search — the other vaults still
// have answers. Only a search no vault answered at all is an error.
func multiSearch(
	ctx context.Context, reg *vaultRegistry, query string, k int, minSim float64,
) ([]vaultHit, []string, error) {
	targets, warnings := searchTargets(ctx, reg)
	if len(targets) == 0 {
		return nil, warnings, grimoireapp.ErrNoVault
	}
	vaults := make([]string, 0, len(targets))
	for vault := range targets {
		vaults = append(vaults, vault)
	}
	sort.Strings(vaults) // deterministic embed ownership and warning order.

	qvecs, embedWarnings := embedPerModel(ctx, targets, vaults, query)
	warnings = append(warnings, embedWarnings...)

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make(map[string][]store.Hit, len(vaults))
		failed  = make(map[string]error, len(vaults))
	)
	for _, vault := range vaults {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := targets[vault]
			hits, err := svc.SearchVec(query, qvecs[svc.EmbedModelName()], k, minSim)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed[vault] = err // one vault's failure is a warning, not the search's.
				return
			}
			results[vault] = hits
		}()
	}
	wg.Wait()

	for _, vault := range vaults {
		if err := failed[vault]; err != nil {
			warnings = append(warnings, vaultWarning(vault, err))
		}
	}
	if len(results) == 0 {
		// Nothing answered: report the first vault's failure, so the caller sees a
		// real cause ("index not ready") rather than an empty result set.
		return nil, warnings, failed[vaults[0]]
	}
	return fuse(results, k), warnings, nil
}

// searchTargets resolves the vaults a cross-vault search covers: every vault
// Grimoire knows about, plus any other runtime already resident (one a client
// named explicitly). Known vaults are resolved here rather than taken from the
// resident set alone, so a search right after startup covers them all instead of
// racing the staggered warm-up. A vault that won't open is skipped with a
// warning.
func searchTargets(ctx context.Context, reg *vaultRegistry) (map[string]*grimoireapp.Service, []string) {
	targets := reg.live()
	known, err := vaultdir.KnownVaults()
	if err != nil {
		reg.logger.Warn().Err(err).Msg("listing known vaults for search")
		return targets, nil
	}
	var warnings []string
	for _, vault := range known {
		// Key by the canonical path, as live() does, so two spellings of one
		// vault can't be searched (and reported) twice.
		key, err := vaultdir.Canonical(vault)
		if err != nil {
			warnings = append(warnings, vaultWarning(vault, err))
			continue
		}
		if _, resident := targets[key]; resident {
			continue
		}
		svc, err := reg.runtime(ctx, vault)
		if err != nil {
			warnings = append(warnings, vaultWarning(vault, err))
			continue
		}
		targets[key] = svc
	}
	return targets, warnings
}

// embedPerModel embeds the query once per distinct embedding model in play,
// keyed by model id — vaults that share a model share the round trip. A model
// whose embedding fails is left out of the map, degrading its vaults to the
// keyword leg with one warning naming the model rather than one per vault.
// Vaults with no model are not embedded for at all.
func embedPerModel(
	ctx context.Context, targets map[string]*grimoireapp.Service, vaults []string, query string,
) (map[string][]float32, []string) {
	// The first vault (in sorted order) of each model group does the embedding,
	// so which service is asked doesn't depend on map iteration.
	owners := map[string]*grimoireapp.Service{}
	var models []string
	for _, vault := range vaults {
		model := targets[vault].EmbedModelName()
		if model == "" {
			continue
		}
		if _, ok := owners[model]; !ok {
			owners[model] = targets[vault]
			models = append(models, model)
		}
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		qvecs    = make(map[string][]float32, len(models))
		failures = make(map[string]error, len(models))
	)
	for _, model := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			qvec, err := owners[model].EmbedQuery(ctx, query)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures[model] = err // keyword-only for this group.
				return
			}
			qvecs[model] = qvec
		}()
	}
	wg.Wait()

	var warnings []string
	for _, model := range models {
		if err := failures[model]; err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: keyword-only (%s)", model, shortErr(err)))
		}
	}
	return qvecs, warnings
}

// fuse merges the per-vault rankings into one with Reciprocal Rank Fusion over
// the legs each hit reports, drops adjacent-window duplicates per (vault, note),
// and truncates to k.
//
// The comparison mirrors the store's own fusion, extended with the vault so the
// order stays total: over one vault it reproduces that vault's order exactly.
func fuse(results map[string][]store.Hit, k int) []vaultHit {
	type ranked struct {
		hit   vaultHit
		score float64
		fts   float64 // the keyword leg's share of score, for tie-breaking.
	}
	var all []ranked
	for vault, hits := range results {
		for _, h := range hits {
			r := ranked{hit: vaultHit{Hit: h, Vault: vault}}
			if h.VecRank > 0 {
				r.score += 1 / float64(rrfK+h.VecRank)
			}
			if h.FTSRank > 0 {
				r.fts = 1 / float64(rrfK+h.FTSRank)
				r.score += r.fts
			}
			all = append(all, r)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.score != b.score {
			return a.score > b.score
		}
		// Single-leg rank-1s tie at the same RRF score; prefer the keyword side,
		// as the store does — an exact term match is a precise signal, the top of
		// a compressed similarity band is not.
		if a.fts != b.fts {
			return a.fts > b.fts
		}
		if a.hit.Similarity != b.hit.Similarity {
			return a.hit.Similarity > b.hit.Similarity
		}
		if a.hit.Vault != b.hit.Vault {
			return a.hit.Vault < b.hit.Vault
		}
		if a.hit.Path != b.hit.Path {
			return a.hit.Path < b.hit.Path
		}
		return a.hit.Index < b.hit.Index
	})

	var out []vaultHit
	for _, r := range all {
		if k > 0 && len(out) == k {
			break
		}
		if !adjacentToKept(out, r.hit) {
			out = append(out, r.hit)
		}
	}
	return out
}

// adjacentToKept reports whether a better-ranked hit from the same note in the
// same vault is an adjacent or identical window — overlapping windows share
// content, so both matching is usually the same passage twice. The same note
// path in two vaults is two different notes.
func adjacentToKept(kept []vaultHit, h vaultHit) bool {
	for _, k := range kept {
		if k.Vault == h.Vault && k.Path == h.Path && abs(k.Index-h.Index) <= 1 {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// vaultWarning is the one-line note a skipped vault leaves on a cross-vault
// search, naming it the way the results label it (its folder name).
func vaultWarning(vault string, err error) string {
	return fmt.Sprintf("%s: %s", vaultdir.Name(vault), shortErr(err))
}

// searchFanout adapts multiSearch to the JSON API's cross-vault seam, so the
// GUI and the API run the same coordinator.
func searchFanout(reg *vaultRegistry) grimoireapi.SearchFanout {
	return func(ctx context.Context, query string, k int, minSim float64) ([]grimoireapi.Hit, []string, error) {
		hits, warnings, err := multiSearch(ctx, reg, query, k, minSim)
		if err != nil {
			return nil, warnings, err
		}
		out := make([]grimoireapi.Hit, len(hits))
		for i, h := range hits {
			out[i] = grimoireapi.Hit{
				Path:       h.Path,
				Heading:    h.Heading,
				Text:       h.Text,
				Similarity: h.Similarity,
				Vault:      h.Vault,
			}
		}
		return out, warnings, nil
	}
}
