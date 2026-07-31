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
