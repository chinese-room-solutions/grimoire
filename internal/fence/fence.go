// Package fence parses a Markdown fenced-code info string into the pieces
// Grimoire's runnable blocks need: the language and optional kernel-family /
// version overrides. It is a pure leaf package so both the app (which extracts
// blocks to run) and the ui (which renders them) agree on the syntax without
// depending on each other.
//
// Syntax: the first word is the language; optional {kernel=FAMILY} and
// {version=VER} attributes anywhere after it pick a specific installed kernel,
// e.g.
//
//	go {kernel=go} {version=1.21}
//
// {kernel=} names a toolchain family (a kernels/<family>/ folder); {version=}
// picks one of its installed versions. Either may be omitted (family alone =
// newest version of that family; neither = newest claimant of the language).
package fence

import "strings"

// Lang returns the info string's language — its first whitespace-delimited word,
// lowercased ("" if none).
func Lang(info string) string {
	return strings.ToLower(firstWord(info))
}

// Kernel returns the kernel-family override from a {kernel=FAMILY} attribute, or
// "" if absent. FAMILY is taken verbatim (folder names are case-sensitive).
func Kernel(info string) string {
	return attr(info, "{kernel=")
}

// Version returns the version override from a {version=VER} attribute, or "" if
// absent.
func Version(info string) string {
	return attr(info, "{version=")
}

// attr extracts the value of a "{key=value}" attribute given its "{key=" prefix,
// or "" when the attribute is absent or unterminated.
func attr(info, open string) string {
	_, after, ok := strings.Cut(info, open)
	if !ok {
		return ""
	}
	val, _, ok := strings.Cut(after, "}")
	if !ok {
		return "" // unterminated; treat as absent.
	}
	return strings.TrimSpace(val)
}

// firstWord returns the first whitespace-delimited token of s.
func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i]
		}
	}
	return s
}
