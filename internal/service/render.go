package service

import (
	"fmt"
	"strings"
)

// RenderLaunchdPlist renders the launchd user-agent plist for the hub.
// Deterministic: the same spec always renders byte-identical output, which is
// what makes `install --service` idempotent.
func RenderLaunchdPlist(s Spec) string {
	var args strings.Builder
	for _, a := range append([]string{s.BinPath}, s.Args...) {
		fmt.Fprintf(&args, "\t\t<string>%s</string>\n", xmlEscape(a))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>WorkingDirectory</key>
	<string>%s</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
	<key>ProcessType</key>
	<string>Background</string>
</dict>
</plist>
`, xmlEscape(s.Label), args.String(), xmlEscape(s.WorkingDir), xmlEscape(s.LogPath()), xmlEscape(s.ErrLogPath()))
}

// RenderSystemdUnit renders the systemd --user unit for the hub.
func RenderSystemdUnit(s Spec) string {
	exec := shellJoin(append([]string{s.BinPath}, s.Args...))
	return fmt.Sprintf(`[Unit]
Description=workwire hub
Documentation=https://github.com/muthuishere/workwire
After=network.target

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, exec, s.WorkingDir)
}

// WindowsBinPath renders the value sc.exe wants for binPath=.
func WindowsBinPath(s Spec) string {
	return shellJoin(append([]string{s.BinPath}, s.Args...))
}

func shellJoin(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.ContainsAny(p, " \t\"") {
			p = `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
		}
		out = append(out, p)
	}
	return strings.Join(out, " ")
}
