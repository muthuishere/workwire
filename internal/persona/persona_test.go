package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFromMarkdown(t *testing.T) {
	cases := []struct {
		name, doc, want string
	}{
		{
			name: "explicit workwire section wins",
			doc:  "# repo\n\nLong operating manual with many rules.\n\n## workwire\n- owns the Go hub: storage, auth, HTTP\n- will not speak for the TS client\n\n## other\nnoise\n",
			want: "owns the Go hub: storage, auth, HTTP; will not speak for the TS client",
		},
		{
			name: "declaration block never repeats the peer's own name",
			doc:  "# koine\n\n## workwire\n- name: koine\n- owns the Kotlin DSL and its compiler plugin\n",
			want: "owns the Kotlin DSL and its compiler plugin",
		},
		{
			name: "frontmatter beats inference",
			doc:  "---\nowns: the auth service\n---\n\n# thing\n\nA perfectly good descriptive sentence that is not used.\n",
			want: "owns the auth service",
		},
		{
			name: "harness boilerplate is never the persona",
			doc:  "# koine\n\nGuidance for Claude Code (claude.ai/code) working in this repository.\n\n## What this project is\n\nA Kotlin DSL for describing data pipelines, compiled to Spark jobs.\n",
			want: "A Kotlin DSL for describing data pipelines, compiled to Spark jobs.",
		},
		{
			name: "boilerplate-only document yields nothing to infer",
			doc:  "# koine\n\nThis file provides guidance to Claude Code when working with code in this repository.\n",
			want: "",
		},
		{
			name: "badges and links are skipped, emphasis is flattened",
			doc:  "# thing\n\n[![build](a.svg)](b)\n\n**A tiny** [HTTP hub](https://x) for `agents` to reach each other.\n",
			want: "A tiny HTTP hub for agents to reach each other.",
		},
		{
			name: "descriptive heading wins over an earlier stray line",
			doc:  "# thing\n\nSee CONTRIBUTING.md before you open a pull request here.\n\n## Overview\n\nA scheduler for long-running batch jobs.\n",
			want: "A scheduler for long-running batch jobs.",
		},
		{
			name: "frontmatter declaration",
			doc:  "---\nname: api\nowns: the auth service\nwill-not-speak-for: the web UI\n---\n\nprose\n",
			want: "api; owns the auth service; will not speak for the web UI",
		},
		{
			name: "falls back to opening prose",
			doc:  "# workwire\n\nOpen-source, HTTP-only message hub for the work between workers.\n\nmore\n",
			want: "Open-source, HTTP-only message hub for the work between workers.",
		},
		{name: "empty doc", doc: "", want: ""},
	}
	for _, c := range cases {
		if got := FromMarkdown(c.doc); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A peer declares the audiences it wants in its own AGENTS.md / CLAUDE.md
// (ADR-012), so onboarding stays "write the file, say the phrase".
func TestGroupsFromMarkdown(t *testing.T) {
	cases := []struct {
		name, doc string
		want      []string
	}{
		{
			name: "workwire section, comma separated, @ optional",
			doc:  "# repo\n\n## workwire\n- owns the Go hub\n- groups: @platform, data\n",
			want: []string{"@platform", "@data"},
		},
		{
			name: "frontmatter declaration",
			doc:  "---\nname: api\ngroups: \"@platform @payments\"\n---\n\nprose\n",
			want: []string{"@platform", "@payments"},
		},
		{name: "no declaration", doc: "# repo\n\n## workwire\n- owns the hub\n", want: nil},
		{name: "empty doc", doc: "", want: nil},
	}
	for _, c := range cases {
		got := GroupsFromMarkdown(c.doc)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
			}
		}
	}
}

// The `groups:` line is addressing config, never part of the persona.
func TestGroupsLineIsNotPersona(t *testing.T) {
	doc := "# repo\n\n## workwire\n- groups: @platform\n- owns the Go hub\n"
	if got := FromMarkdown(doc); got != "owns the Go hub" {
		t.Fatalf("groups leaked into the persona: %q", got)
	}
}

func TestNeverSendsTheWholeFile(t *testing.T) {
	long := "# repo\n\n" + repeat("this is a long instruction-heavy operating manual. ", 40)
	got := FromMarkdown(long)
	if len(got) > MaxLen+3 {
		t.Fatalf("persona not capped: %d chars", len(got))
	}
	if got == "" {
		t.Fatal("truncation swallowed everything")
	}
}

// A bare identifier is more honest than a misleading sentence: when the
// repo's own file says nothing about the repo, say what the repo IS called
// rather than advertising the harness boilerplate as a self-description.
func TestDeriveFallsBackToTheIdentifierNotBoilerplate(t *testing.T) {
	dir := t.TempDir()
	doc := "# koine\n\nThis file provides guidance to Claude Code when working with code in this repository.\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Derive(dir)
	if strings.Contains(strings.ToLower(got), "guidance") {
		t.Fatalf("boilerplate advertised as a persona: %q", got)
	}
	if got != filepath.Base(dir) {
		t.Fatalf("expected the folder identifier %q, got %q", filepath.Base(dir), got)
	}
}

// The prose path must survive a real-world CLAUDE.md: title, boilerplate
// opener, then the section that actually describes the project.
func TestDeriveReadsTheDescriptiveSection(t *testing.T) {
	dir := t.TempDir()
	doc := "# koine\n\nGuidance for Claude Code (claude.ai/code) working in this repository.\n\n" +
		"## What this project is\n\nA Kotlin DSL for describing data pipelines, compiled to Spark jobs.\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "A Kotlin DSL for describing data pipelines, compiled to Spark jobs."
	if got := Derive(dir); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
