package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/kernel"
	"github.com/chinese-room-solutions/grimoire/internal/runs"
)

// RunResult and RunItem are a code block's persisted last run and one of its
// output items, re-exported so the web layer depends only on app.
type RunResult = runs.Result
type RunItem = runs.Item

// MIME types for run output items, re-exported for the web layer.
const (
	MIMEText = runs.MIMEText
	MIMEPNG  = runs.MIMEPNG
	MIMESVG  = runs.MIMESVG
	MIMEHTML = runs.MIMEHTML
)

// RunEvent is one piece of a code block's output (stdout/stderr/exit/error),
// re-exported so the web layer depends only on app.
type RunEvent = kernel.Event

// Kernel run sentinels, re-exported for handlers to branch on.
var (
	// ErrNoKernel means no kernel claims the block's language (or kernels are
	// disabled). The UI tells the user the language isn't runnable.
	ErrNoKernel = kernel.ErrNoKernel
	// ErrKernelUnavailable means a kernel exists but its interpreter isn't
	// installed (e.g. bash absent). The UI tells the user to install it.
	ErrKernelUnavailable = kernel.ErrKernelUnavailable
)

// RunBlock runs a code block from notePath through a kernel for lang, delivering
// each output event to emit. family and version are optional per-block overrides
// (from {kernel=FAMILY} {version=VER} fence attributes); otherwise the newest
// version of the first family claiming lang is used. Blocks that resolve to the
// same kernel in one note share its session, so variables and cwd persist between
// them. Returns ErrNoKernel when nothing can run the language (or the override is
// unknown) and ErrKernelUnavailable when the interpreter is missing.
func (s *Service) RunBlock(ctx context.Context, notePath, lang, family, version, code string, emit func(RunEvent)) error {
	if s.kernels == nil {
		return ErrNoKernel
	}
	return s.kernels.Run(ctx, notePath, lang, family, version, code, emit)
}

// KernelInfo returns the label and version of the kernel that would run a block
// of lang with the given per-block family/version override — for showing on the
// block before it runs. ok is false when the language has no kernel (or the
// override is unknown), so the caller can omit the badge.
func (s *Service) KernelInfo(lang, family, version string) (label, resolvedVersion string, ok bool) {
	if s.kernels == nil {
		return "", "", false
	}
	return s.kernels.ResolveInfo(lang, family, version)
}

// RunAbove runs every runnable code block in notePath from the top through the
// one at targetIndex, in order, into the note's shared kernel session — so the
// blocks build on each other like notebook cells run top-to-bottom. emit receives
// each block's events tagged with that block's index, so the caller can stream
// output into the right panel. It stops at the first block that fails (a non-zero
// exit, a missing kernel, or a dead kernel), leaving the rest unrun, and returns
// that block's error. Blocks whose language no kernel claims are skipped (they
// can't contribute to the session), not treated as failures.
func (s *Service) RunAbove(ctx context.Context, notePath string, targetIndex int, emit func(block int, code string, ev RunEvent)) error {
	if s.kernels == nil {
		return ErrNoKernel
	}
	source, err := s.ReadNote(notePath)
	if err != nil {
		return err
	}
	for _, b := range extractCodeBlocks(source) {
		if b.Index > targetIndex {
			break
		}
		if !s.kernels.Has(b.Lang) {
			continue // not runnable; skip rather than fail the sequence.
		}
		failed := false
		err := s.kernels.Run(ctx, notePath, b.Lang, b.Kernel, b.Version, b.Code, func(ev RunEvent) {
			if ev.Type == kernel.EventExit && ev.Code != 0 {
				failed = true
			}
			emit(b.Index, b.Code, ev)
		})
		if err != nil {
			return err
		}
		if failed {
			return ErrBlockFailed
		}
	}
	return nil
}

// ErrBlockFailed stops a RunAbove sequence when a block exits non-zero. The
// per-block exit event already showed the failure in the UI, so the handler just
// stops the sequence; this isn't surfaced as a message.
var ErrBlockFailed = errors.New("a block exited non-zero")

// CloseNoteKernel ends and forgets a note's kernel sessions, called when its tab
// closes so a stray shell doesn't outlive the note.
func (s *Service) CloseNoteKernel(notePath string) {
	if s.kernels != nil {
		s.kernels.CloseNote(notePath)
	}
}

// SaveRunResult commits a block's run as its saved result, overwriting any
// previous one — the explicit "Save" the user clicks to keep the current output.
// code is the block's source, hashed to the content key the result is stored
// under (the same key the renderer looks up on note open). A nil store drops the
// save silently. Returns an error only so the caller can log it.
func (s *Service) SaveRunResult(notePath, code string, r RunResult) error {
	if s.runs == nil || notePath == "" {
		return nil
	}
	return s.runs.Save(notePath, BlockHash(code), r)
}

// AutoSaveRunResult preserves a block's run only if it has no saved result yet
// (its first-ever run). saved reports whether it was stored; false means a saved
// result already existed, so the run is held as a pending (unsaved) result the
// user can later keep with SavePendingRun or discard — the caller marks the panel
// unsaved. Best-effort: a nil store or failure returns saved=false.
func (s *Service) AutoSaveRunResult(notePath, code string, r RunResult) (saved bool) {
	if s.runs == nil || notePath == "" {
		return false
	}
	saved, err := s.runs.SaveIfAbsent(notePath, BlockHash(code), r)
	if err != nil {
		s.logger.Warn().Err(err).Str("note", notePath).Msg("auto-saving run result")
		return false
	}
	if !saved {
		// A saved result already existed; hold this run so an explicit Save can
		// commit exactly the output the user just saw, without re-running.
		s.setPendingRun(notePath, code, r)
	}
	return saved
}

// SavePendingRun commits a block's pending (unsaved) run as its saved result,
// then clears the pending entry. ok is false when the block has no pending run
// (nothing to save). This is the per-block "Save".
func (s *Service) SavePendingRun(notePath, code string) (ok bool, err error) {
	r, ok := s.takePendingRun(notePath, code)
	if !ok {
		return false, nil
	}
	if err := s.SaveRunResult(notePath, code, r); err != nil {
		// Put it back so the user can retry; the save failed, it's still unsaved.
		s.setPendingRun(notePath, code, r)
		return false, err
	}
	return true, nil
}

// DiscardPendingRun drops a block's pending (unsaved) run without saving it,
// leaving any previously-saved result untouched, and returns that saved result so
// the caller can revert the panel to it. ok is false when the block had no
// pending run (nothing to discard). saved is false when nothing was stored before
// (the block's first run was the pending one), so the caller collapses the panel.
// This is the per-block "Discard": undo a re-run, keep what was saved.
func (s *Service) DiscardPendingRun(notePath, code string) (saved RunResult, hasSaved, ok bool) {
	if _, ok = s.takePendingRun(notePath, code); !ok {
		return RunResult{}, false, false
	}
	saved, hasSaved = s.RunResultFor(notePath, code)
	return saved, hasSaved, true
}

// SaveAllPendingRuns commits every pending run for a note and returns how many it
// saved. This is the per-note "Save all". A block whose save fails is left
// pending and counted as not saved.
func (s *Service) SaveAllPendingRuns(notePath string) int {
	prefix := notePath + "\x00"
	s.pendingMu.Lock()
	codes := make([]string, 0)
	for key, p := range s.pendingRuns {
		if strings.HasPrefix(key, prefix) {
			codes = append(codes, p.code)
		}
	}
	s.pendingMu.Unlock()

	n := 0
	for _, code := range codes {
		if ok, err := s.SavePendingRun(notePath, code); err != nil {
			s.logger.Warn().Err(err).Str("note", notePath).Msg("saving pending run")
		} else if ok {
			n++
		}
	}
	return n
}

// DiscardAllPendingRuns drops every unsaved (pending) run in a note without
// saving, leaving saved results untouched, and returns how many it dropped. The
// per-note "Discard all": revert every re-run block to its saved output in one
// click. The caller re-renders the note so each panel shows its saved result (or
// clears where there was none).
func (s *Service) DiscardAllPendingRuns(notePath string) int {
	prefix := notePath + "\x00"
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	n := 0
	for key := range s.pendingRuns {
		if strings.HasPrefix(key, prefix) {
			delete(s.pendingRuns, key)
			n++
		}
	}
	return n
}

// pendingRun is a block's unsaved run: its source (so Save can re-key and prune)
// and the result to commit.
type pendingRun struct {
	code   string
	result RunResult
}

func pendingKey(notePath, code string) string {
	return notePath + "\x00" + BlockHash(code)
}

func (s *Service) setPendingRun(notePath, code string, r RunResult) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pendingRuns == nil {
		s.pendingRuns = map[string]pendingRun{}
	}
	s.pendingRuns[pendingKey(notePath, code)] = pendingRun{code: code, result: r}
}

// takePendingRun returns and removes a block's pending run, if any.
func (s *Service) takePendingRun(notePath, code string) (RunResult, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	key := pendingKey(notePath, code)
	p, ok := s.pendingRuns[key]
	if ok {
		delete(s.pendingRuns, key)
	}
	return p.result, ok
}

// RunResultFor returns a block's last run for re-hydrating its panel on note
// open, looked up by the hash of the block's source. ok is false when the block
// was never run, or its code changed since (the hash no longer matches).
func (s *Service) RunResultFor(notePath, code string) (RunResult, bool) {
	if s.runs == nil || notePath == "" {
		return RunResult{}, false
	}
	r, ok, err := s.runs.Get(notePath, BlockHash(code))
	if err != nil {
		s.logger.Warn().Err(err).Str("note", notePath).Msg("loading run result")
		return RunResult{}, false
	}
	return r, ok
}

// DeleteRunResult removes a block's saved result and drops any pending (unsaved)
// run for it, so its panel clears on the next open. The per-block "remove output".
func (s *Service) DeleteRunResult(notePath, code string) error {
	s.takePendingRun(notePath, code) // discard any unsaved run too.
	if s.runs == nil || notePath == "" {
		return nil
	}
	return s.runs.Delete(notePath, BlockHash(code))
}

// DeleteNoteRunResults removes every saved result for a note and drops all its
// pending runs. The per-note "remove all output".
func (s *Service) DeleteNoteRunResults(notePath string) error {
	s.clearPendingRuns(notePath)
	if s.runs == nil || notePath == "" {
		return nil
	}
	return s.runs.DeleteNote(notePath)
}

// clearPendingRuns drops all of a note's pending (unsaved) runs.
func (s *Service) clearPendingRuns(notePath string) {
	prefix := notePath + "\x00"
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for key := range s.pendingRuns {
		if strings.HasPrefix(key, prefix) {
			delete(s.pendingRuns, key)
		}
	}
}

// dropRunsForDeleted removes a note's cached run output — saved results and
// pending (unsaved) runs — once the note itself is gone from disk. This is the
// watcher's prune path for external deletes and renames: in-app deletes clean up
// inline, but a note removed by Obsidian or the shell would otherwise leave its
// results orphaned in runs.db forever. A no-op while the note still exists.
func (s *Service) dropRunsForDeleted(rel string) {
	abs, err := s.vaultPath(filepath.FromSlash(rel))
	if err != nil {
		return
	}
	if _, err := os.Stat(abs); !errors.Is(err, fs.ErrNotExist) {
		return // still there (or unknowable) — keep its results.
	}
	s.clearPendingRuns(rel)
	if s.runs == nil {
		return
	}
	if err := s.runs.DeleteNote(rel); err != nil {
		s.logger.Warn().Err(err).Str("note", rel).Msg("dropping deleted note's run results")
	}
}

// sweepOrphanRunResults deletes run results whose note no longer exists in the
// vault — orphans left by deletes and renames the watcher couldn't attribute to
// a note: those made while Grimoire wasn't running, and external folder moves,
// which fsnotify reports only at the directory level. Best-effort: a stat that
// fails for any reason other than "not there" keeps the results.
func (s *Service) sweepOrphanRunResults() {
	if s.runs == nil {
		return
	}
	s.mu.Lock()
	vault := s.cfg.Vault
	s.mu.Unlock()
	if vault == "" {
		return
	}
	paths, err := s.runs.NotePaths()
	if err != nil {
		s.logger.Warn().Err(err).Msg("listing run-result notes for the orphan sweep")
		return
	}
	for _, rel := range paths {
		if _, err := os.Stat(filepath.Join(vault, filepath.FromSlash(rel))); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := s.runs.DeleteNote(rel); err != nil {
			s.logger.Warn().Err(err).Str("note", rel).Msg("sweeping orphaned run results")
		}
	}
}

// pruneRunResults drops a note's stored run results whose block is no longer
// present (edited or removed), keeping the cache in step with the note's current
// blocks. Called after a note's body is saved. Best-effort.
func (s *Service) pruneRunResults(notePath, source string) {
	if s.runs == nil {
		return
	}
	blocks := extractCodeBlocks(source)
	keep := make([]string, 0, len(blocks))
	for _, b := range blocks {
		keep = append(keep, BlockHash(b.Code))
	}
	if err := s.runs.PruneNote(notePath, keep); err != nil {
		s.logger.Warn().Err(err).Str("note", notePath).Msg("pruning run results")
	}
}
