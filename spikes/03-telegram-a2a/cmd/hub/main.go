package main

import (
	"log"
	"net/http"
	"os"

	"spike03/hub"
)

func main() {
	addr := os.Getenv("SPIKE03_HUB_ADDR")
	if addr == "" {
		addr = "127.0.0.1:24303"
	}
	log.Printf("spike03 hub listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, hub.New().Handler()))
}
