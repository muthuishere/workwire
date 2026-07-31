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
		wantAuto    bool
		wantOff     bool
	}{
		{name: "no flags is usage", args: nil, wantErr: true},
		{name: "skills only", args: []string{"--skills"}, wantSkills: true},
		{name: "service only", args: []string{"--service"}, wantService: true},
		{name: "both", args: []string{"--service", "--skills"}, wantSkills: true, wantService: true},
		{name: "all implies everything", args: []string{"--all"}, wantSkills: true, wantService: true, wantAuto: true},
		{name: "auto only", args: []string{"--auto"}, wantAuto: true},
		{name: "toggle needs no other flag", args: []string{"--off"}, wantOff: true},
		{name: "on and off are contradictory", args: []string{"--on", "--off"}, wantErr: true},
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
			if f.skills != c.wantSkills || f.service != c.wantService || f.dir != c.wantDir ||
				f.auto != c.wantAuto || f.off != c.wantOff {
				t.Fatalf("parsed %+v want skills=%v service=%v auto=%v off=%v dir=%q",
					f, c.wantSkills, c.wantService, c.wantAuto, c.wantOff, c.wantDir)
			}
		})
	}
}
