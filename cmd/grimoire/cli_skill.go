package main

import (
	"os"
	"path/filepath"

	"github.com/chinese-room-solutions/grimoire"
	"github.com/chinese-room-solutions/mass-sdk/fsutil"
)

// runSkill dispatches the `grimoire skill` verbs: the agent instruction file
// shipped inside the binary (grimoire.AgentSkillFS). Bare or `show` prints it to
// stdout so it can be piped anywhere; `install DIR` writes it into DIR. It
// touches no vault, no backend, and no network — the file travels with the
// binary, so the instructions always describe the verbs this build actually has.
//
// Nothing here names a particular agent: the caller says where its agent reads
// skills from, and the layout written (DIR/<name>/SKILL.md) is what the common
// loaders expect.
func (e *cliEnv) runSkill(args []string) int {
	if len(args) == 0 {
		return e.runSkillShow(nil)
	}
	switch args[0] {
	case "show":
		return e.runSkillShow(args[1:])
	case "install":
		return e.runSkillInstall(args[1:])
	default:
		e.usageErrf("unknown skill subcommand %q (want show|install)", args[0])
		return exitUsage
	}
}

// runSkillShow handles `grimoire skill show` (and the bare `grimoire skill`):
// print the skill's Markdown to stdout, undecorated so it pipes cleanly, the way
// `note get` prints a note. --json wraps it as {name, file, content}.
func (e *cliEnv) runSkillShow(args []string) int {
	if len(args) != 0 {
		e.usageErrf("skill show takes no arguments")
		return exitUsage
	}
	content := grimoire.AgentSkill()
	if e.json {
		e.writeJSON(e.out, map[string]any{
			"name":    grimoire.AgentSkillName,
			"file":    "SKILL.md",
			"content": content,
		})
		return exitOK
	}
	e.outf("%s", content)
	return exitOK
}

// runSkillInstall handles `grimoire skill install DIR`: write the skill to
// DIR/<name>/SKILL.md, creating DIR and the leaf as needed. An existing file is
// overwritten rather than refused — reinstalling after an upgrade is the point,
// since a stale skill describes verbs the binary may no longer have.
func (e *cliEnv) runSkillInstall(args []string) int {
	if len(args) != 1 {
		e.usageErrf("skill install takes exactly one DIR argument (the directory your agent reads skills from)")
		return exitUsage
	}
	dir := filepath.Join(args[0], grimoire.AgentSkillName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.errorf("creating %s: %v", dir, err)
		return exitError
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := fsutil.WriteFileAtomic(path, []byte(grimoire.AgentSkill()), 0o644); err != nil {
		e.errorf("writing %s: %v", path, err)
		return exitError
	}
	if e.json {
		e.writeJSON(e.out, map[string]any{"name": grimoire.AgentSkillName, "path": path})
		return exitOK
	}
	e.outf("installed %s to %s\n", grimoire.AgentSkillName, path)
	return exitOK
}
