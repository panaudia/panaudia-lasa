package main

import "testing"

// The -X main.version value wins; without it the fallback is the build
// info's tag or "dev", never empty and never "(devel)".
func TestServerVersion(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = "1.2.3-4-gabcdef0-dirty"
	if got := serverVersion(); got != "1.2.3-4-gabcdef0-dirty" {
		t.Fatalf("ldflags version: got %q", got)
	}
	version = ""
	got := serverVersion()
	if got == "" || got == "(devel)" || got[0] == 'v' {
		t.Fatalf("fallback version: got %q", got)
	}
}
