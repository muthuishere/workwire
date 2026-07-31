package hubaddr

import "testing"

func TestIsLoopback(t *testing.T) {
	yes := []string{
		"http://127.0.0.1:14411", "http://localhost:14411", "http://LOCALHOST:14411/",
		"http://[::1]:14411", "http://127.0.0.2:14411", "https://localhost",
	}
	for _, u := range yes {
		if !IsLoopback(u) {
			t.Errorf("IsLoopback(%q) = false, want true", u)
		}
	}
	no := []string{
		"http://hub.example.com:14411", "https://10.0.0.5", "http://192.168.1.9:14411",
		"", "not a url", "http://", "http://0.0.0.0:14411",
	}
	for _, u := range no {
		if IsLoopback(u) {
			t.Errorf("IsLoopback(%q) = true, want false", u)
		}
	}
}

func TestKeyNormalises(t *testing.T) {
	same := []string{
		"http://127.0.0.1:14411", "http://127.0.0.1:14411/", "http://localhost:14411",
		"http://[::1]:14411", "HTTP://LocalHost:14411/some/path",
	}
	want := Key(same[0])
	for _, u := range same[1:] {
		if got := Key(u); got != want {
			t.Errorf("Key(%q) = %q, want %q", u, got, want)
		}
	}
	if Key("https://hub.example.com") == Key("http://hub.example.com") {
		t.Error("scheme must not collapse: https and http are different hubs")
	}
	if Key("https://hub.example.com") != "https://hub.example.com:443" {
		t.Errorf("default port not made explicit: %q", Key("https://hub.example.com"))
	}
}
