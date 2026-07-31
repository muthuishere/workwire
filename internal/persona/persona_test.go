package persona

import "testing"

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

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
