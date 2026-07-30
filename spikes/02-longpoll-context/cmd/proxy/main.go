// Tiny reverse proxy enforcing a 30s idle timeout (no nginx). ~20 lines.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:14412", "proxy bind")
	upstream := flag.String("upstream", "http://127.0.0.1:14411", "hub URL")
	flag.Parse()
	u, _ := url.Parse(*upstream)
	p := httputil.NewSingleHostReverseProxy(u)
	// Kill any upstream request that produces no response headers within 30s —
	// this is the "LB idle timeout" a hosted hub sits behind.
	p.Transport = &http.Transport{ResponseHeaderTimeout: 30 * time.Second}
	srv := &http.Server{Addr: *addr, Handler: p, IdleTimeout: 30 * time.Second}
	log.Printf("proxy %s -> %s (30s idle timeout)", *addr, *upstream)
	log.Fatal(srv.ListenAndServe())
}
