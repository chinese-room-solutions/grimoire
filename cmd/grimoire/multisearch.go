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
// Vaults are fused by embedding model. Vaults sharing a model are one corpus:
// their similarities are on the same scale, so their vector legs merge into a
// single global ranking (and a single relevance band) rather than an interleave
// of per-vault positions, which would rank every vault's best hit above every
// vault's second-best however much better it was. Keyword legs still merge by
// position — BM25 scores from separate FTS corpora are not comparable, so
// interleaving them is the honest merge.
//
// Vaults on different models can't be compared at all, so each model's results
// stay a group of their own.

// rrfK is the Reciprocal Rank Fusion constant, matching the store's own: a leg
// contributes 1/(rrfK+rank) at 1-based rank. Re-fusing one vault's hits with it
// reproduces that vault's order, so a single-vault cross-vault search ranks
// exactly like a plain store search.
const rrfK = 60

// vaultHit is a search hit plus its provenance: the vault it came from (its
// canonical absolute path), which is what a caller needs to label, preview, or
// open it, and the embedding model that ranked it — the scale its similarity is
// on, and so which other hits it is comparable with.
type vaultHit struct {
	store.Hit
	Vault string
	Model string
}

// searchGroup is one model's share of a cross-vault search: the vaults that
// answered with that model and the single fused ranking over all of them.
type searchGroup struct {
	Model  string     // "" = vaults with no embedding model (keyword only).
	Vaults []string   // canonical paths, sorted.
	Hits   []vaultHit // fused across the group's vaults, capped at k.
}

// legs are one vault's two retrieval legs, unfused.
type legs struct {
	vec []store.Hit
	fts []store.Hit
}

// multiSearch runs one query across every vault the daemon can serve and fuses
// the vaults sharing an embedding model into one ranking each. k caps the hits
// per group and minSim is the vector leg's similarity floor, both as for a
// single-vault search.
//
// A vault that can't answer (folder gone, no model, index still opening) is
// skipped with a warning rather than failing the search — the other vaults still
// have answers. Only a search no vault answered at all is an error.
func multiSearch(
	ctx context.Context, reg *vaultRegistry, query string, k int, minSim float64,
) ([]searchGroup, []string, error) {
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
		results = make(map[string]legs, len(vaults))
		failed  = make(map[string]error, len(vaults))
	)
	for _, vault := range vaults {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := targets[vault]
			vec, fts, err := svc.SearchLegsVec(query, qvecs[svc.EmbedModelName()], k, minSim)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed[vault] = err // one vault's failure is a warning, not the search's.
				return
			}
			results[vault] = legs{vec: vec, fts: fts}
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

	models := make(map[string]string, len(results))
	for vault := range results {
		models[vault] = targets[vault].EmbedModelName()
	}
	return fuseGroups(results, models, grimoireapp.SearchOptions(k, minSim)), warnings, nil
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

// fuseGroups fuses each embedding model's vaults into one ranking and orders
// the groups by their best fused score. That ordering is presentational only —
// scores from different models are not comparable — but it puts the strongest
// group first, which is the common (single-group) case anyway. Groups that
// produced no hits are dropped.
func fuseGroups(
	results map[string]legs, models map[string]string, opts store.SearchOptions,
) []searchGroup {
	byModel := map[string][]string{}
	for vault := range results {
		model := models[vault]
		byModel[model] = append(byModel[model], vault)
	}
	type scoredGroup struct {
		group searchGroup
		best  float64
	}
	var scored []scoredGroup
	for model, vaults := range byModel {
		sort.Strings(vaults)
		hits, best := fuseGroup(results, vaults, model, opts)
		if len(hits) == 0 {
			continue
		}
		scored = append(scored, scoredGroup{
			group: searchGroup{Model: model, Vaults: vaults, Hits: hits},
			best:  best,
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].best != scored[j].best {
			return scored[i].best > scored[j].best
		}
		return scored[i].group.Model < scored[j].group.Model
	})

	out := make([]searchGroup, len(scored))
	for i, s := range scored {
		out[i] = s.group
	}
	return out
}

// fuseGroup fuses one model group's vaults into a single ranking: their vector
// legs merged by similarity (one corpus, one relevance band) and their keyword
// legs merged by position, re-fused with Reciprocal Rank Fusion over the global
// ranks, deduped per (vault, note) window, and truncated to opts.K. It returns
// the ranking and its best fused score.
//
// The comparison mirrors the store's own fusion, extended with the vault so the
// order stays total: over one vault it reproduces that vault's order exactly.
func fuseGroup(
	results map[string]legs, vaults []string, model string, opts store.SearchOptions,
) ([]vaultHit, float64) {
	type key struct {
		vault string
		id    int64
	}
	type ranked struct {
		hit   vaultHit
		score float64
		fts   float64 // the keyword leg's share of score, for tie-breaking.
	}
	var order []key
	byKey := map[key]*ranked{}
	at := func(h vaultHit) *ranked {
		k := key{vault: h.Vault, id: h.ID}
		r := byKey[k]
		if r == nil {
			r = &ranked{hit: h}
			byKey[k] = r
			order = append(order, k)
		}
		return r
	}

	for rank, h := range groupVectorLeg(results, vaults, model, opts) {
		r := at(h)
		r.hit.VecRank = rank + 1
		r.score += 1 / float64(rrfK+rank+1)
	}
	for rank, h := range groupKeywordLeg(results, vaults, model) {
		r := at(h)
		r.hit.FTSRank = rank + 1
		r.fts = 1 / float64(rrfK+rank+1)
		r.score += r.fts
	}

	all := make([]*ranked, len(order))
	for i, k := range order {
		all[i] = byKey[k]
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
		if opts.K > 0 && len(out) == opts.K {
			break
		}
		if !adjacentToKept(out, r.hit) {
			out = append(out, r.hit)
		}
	}
	if len(out) == 0 {
		return nil, 0
	}
	return out, all[0].score
}

// groupVectorLeg merges the group's vector legs into one ranking by similarity
// — the vaults share a model, so their scores are on one scale — and applies
// the relevance band once, against the best hit anywhere in the group. A vault
// whose hits are all far below another's therefore contributes none, instead of
// having its own best promoted for being its own best.
//
// Ties break by vault then chunk id, matching the store's own id tie-break, so
// a group of one vault comes out in exactly that vault's order.
func groupVectorLeg(
	results map[string]legs, vaults []string, model string, opts store.SearchOptions,
) []vaultHit {
	var vec []vaultHit
	for _, vault := range vaults {
		for _, h := range results[vault].vec {
			vec = append(vec, vaultHit{Hit: h, Vault: vault, Model: model})
		}
	}
	sort.Slice(vec, func(i, j int) bool {
		if vec[i].Similarity != vec[j].Similarity {
			return vec[i].Similarity > vec[j].Similarity
		}
		if vec[i].Vault != vec[j].Vault {
			return vec[i].Vault < vec[j].Vault
		}
		return vec[i].ID < vec[j].ID
	})
	return store.Band(vec, func(h vaultHit) float64 { return h.Similarity }, opts)
}

// groupKeywordLeg merges the group's keyword legs by position: every vault's
// best match, then every vault's second, and so on (within a tier, by vault
// path). BM25 scores come from separate FTS indexes over different corpora and
// mean nothing against each other, so position is all there is to merge on.
func groupKeywordLeg(results map[string]legs, vaults []string, model string) []vaultHit {
	var fts []vaultHit
	for tier := 0; ; tier++ {
		before := len(fts)
		for _, vault := range vaults {
			hits := results[vault].fts
			if tier < len(hits) {
				fts = append(fts, vaultHit{Hit: hits[tier], Vault: vault, Model: model})
			}
		}
		if len(fts) == before {
			return fts
		}
	}
}

// flatten concatenates the groups' hits in group order, for the callers whose
// output is one list (the JSON API and the CLI) — each hit says which model
// ranked it, so a reader can still tell the groups apart.
func flatten(groups []searchGroup) []vaultHit {
	var out []vaultHit
	for _, g := range groups {
		out = append(out, g.Hits...)
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
		groups, warnings, err := multiSearch(ctx, reg, query, k, minSim)
		if err != nil {
			return nil, warnings, err
		}
		hits := flatten(groups)
		out := make([]grimoireapi.Hit, len(hits))
		for i, h := range hits {
			out[i] = grimoireapi.Hit{
				Path:       h.Path,
				Heading:    h.Heading,
				Text:       h.Text,
				Similarity: h.Similarity,
				Vault:      h.Vault,
				Model:      h.Model,
			}
		}
		return out, warnings, nil
	}
}
