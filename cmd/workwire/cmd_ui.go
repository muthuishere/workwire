package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/config"
)

//go:embed ui.html
var uiPage []byte

// cmdUI serves a local dashboard for a hub.
//
// The page is served by THIS command, not by the hub, and that is deliberate.
// The hub's read endpoints are authenticated (auth R7 keeps `/health` as the
// only open one), and a browser has nowhere safe to keep a bearer token: the
// moment the token is embedded in a page or handed to JavaScript it is one
// screenshot, one extension or one copied URL away from leaving the machine.
//
// So the viewer holds the credential the CLI already has, binds to loopback,
// and proxies a SHORT ALLOW-LIST of read-only GETs. The browser talks to the
// viewer; the token never reaches it. It also works unchanged against a remote
// hub, because the credential resolution is the CLI's, not the page's.
func cmdUI(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	port := fs.Int("port", 14412, "port to serve the dashboard on (loopback only)")
	open := fs.Bool("open", false, "open the dashboard in the default browser")
	fs.Parse(args)

	c := newClient(cfg)

	// Read-only, and an explicit list rather than a prefix match: a proxy that
	// forwards whatever it is given is a hole, not a feature.
	allowed := map[string]bool{
		"/metrics":  true,
		"/agents":   true,
		"/threads":  true,
		"/groups":   true,
		"/contacts": true,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The page is self-contained: no CDN, no fonts, no analytics. Nothing
		// about this mesh should leave the machine to render a table.
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		w.Write(uiPage)
	})
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api")
		if !allowed[path] {
			http.Error(w, `{"error":"not proxied"}`, http.StatusForbidden)
			return
		}
		var body any
		code, err := c.do("GET", path, nil, &body)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":%q}`, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		writeJSONValue(w, body)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot bind %s: %w (another viewer may already be running)", addr, err)
	}
	url := "http://" + addr
	fmt.Printf("workwire dashboard: %s\n", url)
	fmt.Printf("hub:                %s\n", cfg.HubURL)
	fmt.Println("loopback only; the hub credential stays in this process and never reaches the browser.")
	fmt.Println("stop with ctrl-c")
	if *open {
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = openBrowser(url)
		}()
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.Serve(ln)
}

func writeJSONValue(w io.Writer, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
