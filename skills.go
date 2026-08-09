package grimoire

import "embed"

// AgentSkillName is the skill's directory name, used as the leaf when it is
// installed into an agent's skill directory.
const AgentSkillName = "grimoire-cli"

// AgentSkillFS holds the agent skill shipped inside the binary: a plain
// Markdown file teaching an AI agent to drive the grimoire CLI. It is not tied
// to any one agent — the format (YAML frontmatter, then instructions) is what
// the common skill loaders read, and `grimoire skill` hands it to whichever
// directory the user's agent looks in.
//
// It lives under skills/ next to kernels/ for the same reason those do: go:embed
// only reaches files at or below its own directory, so the directive sits at the
// repo root. The copy under this repo's own agent directory is a symlink to this
// file, so the two can't drift — go:embed never follows symlinks, which is why
// this side is the real one.
//
//go:embed skills/grimoire-cli/SKILL.md
var AgentSkillFS embed.FS

// AgentSkill returns the skill's Markdown. The read cannot fail: go:embed
// resolves the path at build time.
func AgentSkill() string {
	data, err := AgentSkillFS.ReadFile("skills/" + AgentSkillName + "/SKILL.md")
	if err != nil {
		panic(err) // unreachable: the file is embedded at build time.
	}
	return string(data)
}
