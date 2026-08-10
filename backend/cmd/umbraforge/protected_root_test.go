package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/umbraforge/desktop/internal/protectedbundle"
)

func TestMaterializeProtectedRoot(t *testing.T) {
	src := t.TempDir()
	rel := "plugins/ezsolver/service.py"
	path := filepath.Join(src, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	bundle := filepath.Join(t.TempDir(), "plugins.ufp")
	if _, err := protectedbundle.Pack(bundle, protectedbundle.PackOptions{
		Root: src, BuildID: "protected-test", MasterKey: master, SigningKey: priv, Paths: []string{rel},
	}); err != nil {
		t.Fatal(err)
	}

	root, err := materializeProtectedRoot(bundle, t.TempDir(), "protected-test", master, pub)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || string(got) != "print('ok')\n" {
		t.Fatalf("materialized payload = %q, err=%v", got, err)
	}
}
