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
			h := strings.ToLower(strings.TrimSpace(strings.TrimLeft(t, "# ")))
			in = h == "workwire"
			continue
		}
		if in && t != "" {
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
