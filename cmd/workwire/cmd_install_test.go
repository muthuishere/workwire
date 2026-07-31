package main

import "testing"

func TestParseInstallFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantErr     bool
		wantSkills  bool
		wantService bool
		wantDir     string
	}{
		{name: "no flags is usage", args: nil, wantErr: true},
		{name: "skills only", args: []string{"--skills"}, wantSkills: true},
		{name: "service only", args: []string{"--service"}, wantService: true},
		{name: "both", args: []string{"--service", "--skills"}, wantSkills: true, wantService: true},
		{name: "all implies both", args: []string{"--all"}, wantSkills: true, wantService: true},
		{name: "dir override", args: []string{"--skills", "--dir", "/tmp/s"}, wantSkills: true, wantDir: "/tmp/s"},
		{name: "unknown flag errors", args: []string{"--nope"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := parseInstallFlags(c.args)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v wantErr = %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if f.skills != c.wantSkills || f.service != c.wantService || f.dir != c.wantDir {
				t.Fatalf("parsed %+v want skills=%v service=%v dir=%q", f, c.wantSkills, c.wantService, c.wantDir)
			}
		})
	}
}
