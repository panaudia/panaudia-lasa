package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Every module linked into the binary has a section in
// third-party-licences.md — a new dependency cannot arrive without its
// notice. Regenerate with tools/gen-third-party-licences.py.
func TestThirdPartyLicencesCoverBuildGraph(t *testing.T) {
	doc, err := os.ReadFile("../third-party-licences.md")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}", ".")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		p := strings.TrimSpace(line)
		if p == "" || seen[p] || p == "github.com/panaudia/panaudia-lasa/engine" {
			continue
		}
		seen[p] = true
		if !strings.Contains(string(doc), "### "+p+"\n") && !strings.Contains(string(doc), p+" v") {
			t.Errorf("third-party-licences.md has no section for %s", p)
		}
	}
	if len(seen) < 10 {
		t.Fatalf("suspiciously few modules in the build graph (%d)", len(seen))
	}
}
