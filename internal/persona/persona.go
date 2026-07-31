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
	"strings"
)

// MaxLen caps a derived persona; longer text is truncated cleanly.
const MaxLen = 200

// Sources are searched in order: the directory's own files first, the user's
// global CLAUDE.md last.
func sources(dir string) []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(dir, "CLAUDE.md"),
		filepath.Join(home, ".claude", "CLAUDE.md"),
	}
}

// Derive returns a short persona for dir, or "" when nothing is readable.
func Derive(dir string) string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	for _, p := range sources(dir) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if s := FromMarkdown(string(b)); s != "" {
			return s
		}
	}
	return ""
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
func groupsKV(line string) (string, bool) {
	t := strings.TrimLeft(line, "-* ")
	i := strings.Index(t, ":")
	if i <= 0 || strings.ToLower(strings.TrimSpace(t[:i])) != "groups" {
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

// fromProse falls back to the document's first real sentence of prose.
func fromProse(doc string) string {
	body := doc
	if strings.HasPrefix(strings.TrimLeft(doc, "\n"), "---") {
		rest := strings.TrimLeft(doc, "\n")[3:]
		if i := strings.Index(rest, "\n---"); i >= 0 {
			body = rest[i+4:]
		}
	}
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") ||
			strings.HasPrefix(t, "|") || strings.HasPrefix(t, ">") {
			continue
		}
		return strings.TrimLeft(t, "-* ")
	}
	return ""
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
