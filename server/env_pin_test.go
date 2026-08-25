package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The environment is read in config.go and nowhere else — engine
// included. A knob that grows a private os.Getenv somewhere deep is
// invisible to the startup printout and to .env.example, which is
// exactly the drift this pins against.
func TestNoEnvReadsOutsideConfig(t *testing.T) {
	roots := []string{".", "../engine"}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			t.Logf("skipping %s: %v", root, err)
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if filepath.Base(path) == "config.go" && root == "." {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, call := range []string{"os.Getenv(", "os.LookupEnv(", "os.Environ("} {
				if strings.Contains(string(src), call) {
					t.Errorf("%s reads the environment (%s); configuration is read in server/config.go only", path, call)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
