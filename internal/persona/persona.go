// Package persona derives a peer's self-description from the files that
// already describe it — the directory's own AGENTS.md / CLAUDE.md — so a
// participant never hand-writes a persona: they drop the file in and join.
//
// The whole file is NEVER used. These are long, instruction-heavy operating
// manuals; broadcasting one as a persona is noise and would drop one
// person's instructions into every other session's context. A persona is a
// capped one-liner, and every consumer treats it as untrusted DATA.
package persona

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/muthuishere/workwire/internal/origin"
)

// MaxLen caps a derived persona; longer text is truncated cleanly.
const MaxLen = 200

// localSources are the directory's OWN files — the only ones that describe
// this peer. The user's global CLAUDE.md describes the human's whole setup,
// never this repo, so a persona is never taken from it.
func localSources(dir string) []string {
	return []string{
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(dir, "CLAUDE.md"),
	}
}

// Sources are searched in order: the directory's own files first, the user's
// global CLAUDE.md last (group declarations may legitimately live there).
func sources(dir string) []string {
	home, _ := os.UserHomeDir()
	return append(localSources(dir), filepath.Join(home, ".claude", "CLAUDE.md"))
}

// Derive returns a short persona for dir. When the directory's own files say
// nothing substantive about it, the fallback is a bare identifier — repo and
// folder — never a line of harness boilerplate: "koine (muthuishere/koine)"
// tells a peer less than a good sentence but, unlike "Guidance for Claude
// Code working in this repository", it is at least true.
func Derive(dir string) string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	for _, p := range localSources(dir) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if s := FromMarkdown(string(b)); s != "" {
			return s
		}
	}
	return Identifier(dir)
}

// Identifier is the honest last resort: the folder name, plus the repo it
// belongs to when there is one.
func Identifier(dir string) string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	base := filepath.Base(abs)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	if repo := origin.Detect(abs).Repo; repo != "" && repo != base {
		return Truncate(base + " (" + repo + ")")
	}
	return Truncate(base)
}

// DeriveGroups returns the workwire groups declared by dir's own files
// (ADR-012): a `groups:` line inside the `## workwire` block, or a `groups`
// key in YAML frontmatter. Onboarding stays "write the file, say the
// phrase" — the listener joins these at startup.
func DeriveGroups(dir string) []string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	for _, p := range sources(dir) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if g := GroupsFromMarkdown(string(b)); len(g) > 0 {
			return g
		}
	}
	return nil
}

// GroupsFromMarkdown parses a `groups:` declaration out of one document.
// Names are normalised to the `@name` form; a group is only ever a name, so
// nothing else is read out of the file.
func GroupsFromMarkdown(doc string) []string {
	raw := groupsLine(doc)
	if raw == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		n := strings.TrimSpace(strings.Trim(f, `"'`))
		n = strings.TrimLeft(n, "@")
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, "@"+n)
	}
	return out
}

// groupsLine finds the raw right-hand side of a `groups:` declaration, in
// the `## workwire` block first, then frontmatter.
func groupsLine(doc string) string {
	in := false
	for _, ln := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			if in {
				break
			}
			h := strings.ToLower(strings.TrimSpace(strings.TrimLeft(t, "# ")))
			in = strings.HasPrefix(t, "##") && h == "workwire"
			continue
		}
		if in {
			if v, ok := groupsKV(t); ok {
				return v
			}
		}
	}
	if strings.HasPrefix(strings.TrimLeft(doc, "\n"), "---") {
		body := strings.TrimLeft(doc, "\n")[3:]
		if end := strings.Index(body, "\n---"); end >= 0 {
			for _, ln := range strings.Split(body[:end], "\n") {
				if v, ok := groupsKV(strings.TrimSpace(ln)); ok {
					return v
				}
			}
		}
	}
	return ""
}

// groupsKV matches a `groups: ...` line, with or without a list bullet.
func groupsKV(line string) (string, bool) { return keyValue(line, "groups") }

// keyValue matches a `<key>: ...` declaration line, with or without a list
// bullet, case-insensitively.
func keyValue(line, key string) (string, bool) {
	t := strings.TrimLeft(line, "-* ")
	i := strings.Index(t, ":")
	if i <= 0 || strings.ToLower(strings.TrimSpace(t[:i])) != key {
		return "", false
	}
	return strings.TrimSpace(t[i+1:]), true
}

// FromMarkdown extracts a persona from one markdown document: an explicit
// `## workwire` declaration wins, then YAML frontmatter keys, then the
// opening prose.
func FromMarkdown(doc string) string {
	if s := fromSection(doc); s != "" {
		return Truncate(s)
	}
	if s := fromFrontmatter(doc); s != "" {
		return Truncate(s)
	}
	return Truncate(fromProse(doc))
}

// fromSection reads an explicit `## workwire` block: its declaration lines,
// joined, are the persona the peer wrote for exactly this purpose.
func fromSection(doc string) string {
	lines := strings.Split(doc, "\n")
	var buf []string
	in := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			if in {
				break
			}
			// A `## workwire` block only — the document TITLE (`# workwire`)
			// is the repo's name, not a declaration about the peer.
			h := strings.ToLower(strings.TrimSpace(strings.TrimLeft(t, "# ")))
			in = strings.HasPrefix(t, "##") && h == "workwire"
			continue
		}
		if in && t != "" {
			// `groups:` is addressing config (ADR-012), not a
			// self-description — it never becomes part of the persona.
			if _, ok := groupsKV(t); ok {
				continue
			}
			// `name:` is the identity the peer is already shown under;
			// repeating it inside the persona ("name: koine; owns …") is
			// noise, so only the descriptive keys are rendered.
			if _, ok := keyValue(t, "name"); ok {
				continue
			}
			buf = append(buf, strings.TrimLeft(t, "-* "))
		}
	}
	return strings.Join(buf, "; ")
}

// fromFrontmatter reads YAML frontmatter keys name/owns/will-not-speak-for/
// depends-on without a YAML dependency (flat scalars only).
func fromFrontmatter(doc string) string {
	if !strings.HasPrefix(strings.TrimLeft(doc, "\n"), "---") {
		return ""
	}
	body := strings.TrimLeft(doc, "\n")[3:]
	end := strings.Index(body, "\n---")
	if end < 0 {
		return ""
	}
	kv := map[string]string{}
	for _, ln := range strings.Split(body[:end], "\n") {
		i := strings.Index(ln, ":")
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(ln[:i]))
		v := strings.Trim(strings.TrimSpace(ln[i+1:]), `"'`)
		if v != "" {
			kv[k] = v
		}
	}
	var parts []string
	if v := kv["name"]; v != "" {
		parts = append(parts, v)
	}
	if v := kv["owns"]; v != "" {
		parts = append(parts, "owns "+v)
	}
	if v := kv["depends-on"]; v != "" {
		parts = append(parts, "depends on "+v)
	}
	if v := kv["will-not-speak-for"]; v != "" {
		parts = append(parts, "will not speak for "+v)
	}
	return strings.Join(parts, "; ")
}

// descriptiveHeading matches the sections where a repo actually says what it
// is, rather than how an agent should behave in it.
var descriptiveHeading = regexp.MustCompile(
	`(?i)^(what (this|the) (project|repo|repository) is|what it is|overview|about)\b`)

// boilerplate are the openers harness files start with. They describe the
// FILE ("guidance for Claude Code…"), not the peer, so a persona built from
// one tells other peers nothing — that is the bug this list exists for.
var boilerplate = []string{
	"guidance for claude code",
	"guidance to claude code",
	"this file provides guidance",
	"provides guidance to claude",
	"instructions for claude",
	"agent instructions",
	"when working with code in this repository",
	"working in this repository",
}

// fromProse falls back to the document's first substantive descriptive
// sentence: what the repo says it IS, skipping the title, the badges and the
// harness boilerplate.
func fromProse(doc string) string {
	lines := strings.Split(stripFrontmatter(doc), "\n")
	if s := underDescriptiveHeading(lines); s != "" {
		return s
	}
	for _, ln := range lines {
		if s, ok := substantive(ln); ok {
			return s
		}
	}
	return ""
}

// stripFrontmatter drops a leading YAML block so its keys never read as prose.
func stripFrontmatter(doc string) string {
	body := strings.TrimLeft(doc, "\n")
	if !strings.HasPrefix(body, "---") {
		return doc
	}
	rest := body[3:]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		return rest[i+4:]
	}
	return doc
}

// underDescriptiveHeading returns the first substantive line inside a
// "## What this project is" / "## Overview" style section, if one exists.
func underDescriptiveHeading(lines []string) string {
	in := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			if in {
				break // section ended without prose
			}
			in = descriptiveHeading.MatchString(markdownToText(strings.TrimLeft(t, "# ")))
			continue
		}
		if in {
			if s, ok := substantive(ln); ok {
				return s
			}
		}
	}
	return ""
}

// substantive reports whether one line is real prose about the project: not a
// heading, fence, table, quote, badge/link-only line, nor known boilerplate.
func substantive(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") ||
		strings.HasPrefix(t, "|") || strings.HasPrefix(t, ">") ||
		strings.HasPrefix(t, "---") || strings.HasPrefix(t, "<") {
		return "", false
	}
	if strings.HasPrefix(t, "![") || strings.HasPrefix(t, "[!") {
		return "", false // badge row
	}
	s := markdownToText(strings.TrimLeft(t, "-*+ "))
	if s == "" {
		return "", false
	}
	low := strings.ToLower(s)
	for _, b := range boilerplate {
		if strings.Contains(low, b) {
			return "", false
		}
	}
	// A sentence, not a label: a fragment like "TODO" or a bare link text is
	// no more informative than the identifier fallback.
	if len(s) < 15 || len(strings.Fields(s)) < 3 {
		return "", false
	}
	return s, true
}

// mdLink rewrites [text](url) / ![alt](url) to plain text.
var mdLink = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)

// markdownToText flattens emphasis, code spans and links to plain prose.
func markdownToText(s string) string {
	s = mdLink.ReplaceAllString(s, "$1")
	for _, m := range []string{"**", "__", "`"} {
		s = strings.ReplaceAll(s, m, "")
	}
	return strings.TrimSpace(s)
}

// Truncate caps a persona at MaxLen, cutting on a word boundary.
func Truncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= MaxLen {
		return s
	}
	cut := s[:MaxLen]
	if i := strings.LastIndex(cut, " "); i > MaxLen/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:-") + "…"
}
