package wikilink

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name, match string
		want        Link
	}{
		{"a bare target", "[[My Note]]", Link{Target: "My Note"}},
		{"a heading", "[[My Note#Rollback]]", Link{Target: "My Note", Heading: "Rollback"}},
		{"an alias", "[[My Note|shown]]", Link{Target: "My Note", Alias: "shown"}},
		{
			"a heading and an alias",
			"[[My Note#Rollback|shown]]",
			Link{Target: "My Note", Heading: "Rollback", Alias: "shown"},
		},
		{"padding is trimmed", "[[ My Note | shown ]]", Link{Target: "My Note", Alias: "shown"}},
		{"not a wikilink", "plain text", Link{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Parse(tc.match))
		})
	}
}

// Replace rewrites prose links and leaves code alone: a fence or a span that
// happens to hold brackets is code, not a link.
func TestReplace(t *testing.T) {
	upper := func(m string) string { return strings.ToUpper(m) }
	tests := []struct {
		name, source, want string
	}{
		{"a prose link", "see [[a]] here", "see [[A]] here"},
		{"a fenced block", "```sh\n[[ -f x ]] && echo y\n```\n", "```sh\n[[ -f x ]] && echo y\n```\n"},
		{"an inline code span", "use `[[nodiscard]]` there", "use `[[nodiscard]]` there"},
		{"an indented block", "    [[a]]\n", "    [[a]]\n"},
		{
			"prose either side of a fence",
			"[[a]]\n\n```sh\n[[b]]\n```\n\n[[c]]\n",
			"[[A]]\n\n```sh\n[[b]]\n```\n\n[[C]]\n",
		},
		{"no links at all", "plain prose\n", "plain prose\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Replace(tc.source, upper))
		})
	}
}

// Retarget follows a renamed note: every link form that named it is repointed,
// each keeping the shape its author wrote, and nothing else in the note moves.
func TestRetarget(t *testing.T) {
	// Stands in for the vault resolver: the renamed note answered to its bare
	// name and to its full path, case-insensitively, with or without the
	// extension.
	matches := func(target string) bool {
		name := strings.ToLower(strings.TrimSuffix(strings.ToLower(target), ".md"))
		return name == "old name" || name == "notes/old name"
	}
	tests := []struct {
		name, source, want string
		links              int
	}{
		{"a bare link", "see [[Old Name]] here", "see [[New Name]] here", 1},
		{"a heading link", "see [[Old Name#Setup]]", "see [[New Name#Setup]]", 1},
		{"an aliased link", "see [[Old Name|the note]]", "see [[New Name|the note]]", 1},
		{
			"a heading and an alias ride along",
			"see [[Old Name#Setup|the note]]",
			"see [[New Name#Setup|the note]]",
			1,
		},
		{"an embed stays an embed", "![[Old Name]]\n", "![[New Name]]\n", 1},
		{"a path link stays a path", "see [[notes/Old Name]]", "see [[notes/New Name]]", 1},
		{"the target matches case-insensitively", "see [[oLD nAME]]", "see [[New Name]]", 1},
		{"a written extension is dropped", "see [[Old Name.md]]", "see [[New Name]]", 1},
		{
			"several links in one note",
			"[[Old Name]] and [[Old Name#Setup]] and ![[Old Name]]",
			"[[New Name]] and [[New Name#Setup]] and ![[New Name]]",
			3,
		},
		{"a fenced block is code, not a link", "```sh\n[[Old Name]]\n```\n", "```sh\n[[Old Name]]\n```\n", 0},
		{"a code span is code, not a link", "run `[[Old Name]]` now", "run `[[Old Name]]` now", 0},
		{"a note with no links", "plain prose\n", "plain prose\n", 0},
		{"another note sharing the prefix", "see [[Old Name Extra]]", "see [[Old Name Extra]]", 0},
		{"an unrelated note", "see [[Something Else]]", "see [[Something Else]]", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, links := Retarget(tc.source, "New Name", "notes/New Name", matches)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.links, links)
		})
	}
}

// A move that keeps the note's name leaves the notes linking to it by name
// alone: the link already reads as the new target, so there is nothing to
// rewrite and nothing to report.
func TestRetargetLeavesUnchangedTargets(t *testing.T) {
	matches := func(target string) bool {
		return strings.EqualFold(target, "Note") || strings.EqualFold(target, "old/Note")
	}

	got, links := Retarget("see [[Note]] and [[old/Note]]", "Note", "new/Note", matches)
	require.Equal(t, "see [[Note]] and [[new/Note]]", got)
	require.Equal(t, 1, links)
}
