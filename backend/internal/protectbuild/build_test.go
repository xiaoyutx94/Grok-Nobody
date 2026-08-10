package protectbuild

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/umbraforge/desktop/internal/protectedbundle"
)

func TestBuildCreatesPairedBundleAndProtectedConfig(t *testing.T) {
	root := t.TempDir()
	rel := "plugins/ezsolver/solver.py"
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := []byte("SECRET_SOLVER_CORE")
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	manifest, _ := json.Marshal(map[string]any{"files": []string{rel}})
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "plugins.ufp")
	config := filepath.Join(t.TempDir(), "config_protected.go")
	result, err := Build(Options{
		Root: root, ManifestPath: manifestPath, BundleOut: bundle, ConfigOut: config, BuildID: "release-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Info.BuildID != "release-1" || result.Info.FileCount != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	cfg, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{[]byte("//go:build protected"), []byte("func Enabled() bool"), []byte("return true"), []byte("release-1")} {
		if !bytes.Contains(cfg, want) {
			t.Fatalf("generated config missing %q", want)
		}
	}
	if bytes.Contains(cfg, secret) || bytes.Contains(cfg, []byte(rel)) {
		t.Fatal("generated config leaks payload content/path")
	}
	dst := filepath.Join(t.TempDir(), "runtime")
	if _, err := protectedbundle.Extract(bundle, dst, result.MasterKey, result.PublicKey); err != nil {
		t.Fatalf("generated bundle cannot be extracted with paired credentials: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("extracted payload=%q err=%v", got, err)
	}
}
