// Standalone mock Telegram Bot API server for manual runs without a real token.
// Token is read by NAME from the environment (never printed).
package main

import (
	"log"
	"net/http"
	"os"

	"spike03/mocktg"
)

func main() {
	addr := os.Getenv("SPIKE03_MOCKTG_ADDR")
	if addr == "" {
		addr = "127.0.0.1:24304"
	}
	tokenEnv := os.Getenv("SPIKE03_TOKEN_ENV")
	if tokenEnv == "" {
		tokenEnv = "SPIKE_TELEGRAM_BOT_TOKEN"
	}
	token := os.Getenv(tokenEnv)
	if token == "" {
		log.Fatalf("set %s in the environment (any fake value works for the mock)", tokenEnv)
	}
	log.Printf("mock telegram api on %s (token from $%s)", addr, tokenEnv)
	log.Fatal(http.ListenAndServe(addr, mocktg.New(token).Handler()))
}
