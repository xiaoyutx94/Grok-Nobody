package protectedbundle

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestPackExtractRoundTrip(t *testing.T) {
	src := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins/ezsolver/service.py", "print('protected')\n", 0o600)
	write("plugins/veloraturn/veloraturn-windows-amd64.exe", "MZ-fake", 0o700)

	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "plugins.ufp")
	info, err := Pack(bundle, PackOptions{
		Root:       src,
		BuildID:    "test-build",
		MasterKey:  master,
		SigningKey: priv,
		Paths: []string{
			"plugins/ezsolver/service.py",
			"plugins/veloraturn/veloraturn-windows-amd64.exe",
		},
	})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if info.BuildID != "test-build" || info.FileCount != 2 {
		t.Fatalf("unexpected pack info: %+v", info)
	}

	dst := filepath.Join(t.TempDir(), "runtime")
	got, err := Extract(bundle, dst, master, pub)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.BuildID != info.BuildID || got.FileCount != info.FileCount {
		t.Fatalf("extract info = %+v, want %+v", got, info)
	}
	for rel, want := range map[string][]byte{
		"plugins/ezsolver/service.py":                     []byte("print('protected')\n"),
		"plugins/veloraturn/veloraturn-windows-amd64.exe": []byte("MZ-fake"),
	} {
		body, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !bytes.Equal(body, want) {
			t.Fatalf("%s = %q, want %q", rel, body, want)
		}
	}
}

func newTestBundle(t *testing.T, root string, paths []string) (string, []byte, ed25519.PublicKey) {
	t.Helper()
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "plugins.ufp")
	if _, err := Pack(bundle, PackOptions{
		Root: root, BuildID: "security-test", MasterKey: master, SigningKey: priv, Paths: paths,
	}); err != nil {
		t.Fatal(err)
	}
	return bundle, master, pub
}

func TestEncryptedBundleHidesPayloadAndPaths(t *testing.T) {
	root := t.TempDir()
	rel := "plugins/ezsolver/secret-core.py"
	if err := os.MkdirAll(filepath.Join(root, "plugins", "ezsolver"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := []byte("UNIQUE_SECRET_SOLVER_IMPLEMENTATION")
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, _, _ := newTestBundle(t, root, []string{rel})
	blob, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, secret) || bytes.Contains(blob, []byte(rel)) || bytes.Contains(blob, []byte("ezsolver")) {
		t.Fatal("encrypted bundle exposes plaintext payload or plugin path")
	}
}

func TestExtractRejectsTamperedOrWrongKeyBundle(t *testing.T) {
	root := t.TempDir()
	rel := "plugins/auralith/auralithd-windows-amd64.exe"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(rel))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("MZ-core"), 0o700); err != nil {
		t.Fatal(err)
	}
	bundle, master, pub := newTestBundle(t, root, []string{rel})

	wrong := append([]byte(nil), master...)
	wrong[0] ^= 0xff
	if _, err := Extract(bundle, filepath.Join(t.TempDir(), "wrong-key"), wrong, pub); err == nil {
		t.Fatal("wrong master key was accepted")
	}

	blob, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)/2] ^= 0x40
	tampered := filepath.Join(t.TempDir(), "tampered.ufp")
	if err := os.WriteFile(tampered, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dest, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(tampered, dest, master, pub); err == nil {
		t.Fatal("tampered bundle was accepted")
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
		t.Fatalf("failed extraction changed destination: body=%q err=%v", body, err)
	}
}

func TestExtractRepairsTamperedRuntimePayload(t *testing.T) {
	root := t.TempDir()
	rel := "plugins/veloraturn/veloraturn-windows-amd64.exe"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(rel))), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("MZ-original")
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), want, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle, master, pub := newTestBundle(t, root, []string{rel})
	dest := filepath.Join(t.TempDir(), "runtime")
	if _, err := Extract(bundle, dest, master, pub); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dest, filepath.FromSlash(rel))
	if err := os.WriteFile(out, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(bundle, dest, master, pub); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("runtime payload was not repaired: got=%q err=%v", got, err)
	}
}

func TestPackRejectsTraversal(t *testing.T) {
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Pack(filepath.Join(t.TempDir(), "bad.ufp"), PackOptions{
		Root: t.TempDir(), BuildID: "bad", MasterKey: master, SigningKey: priv, Paths: []string{"../secret"},
	}); err == nil {
		t.Fatal("path traversal was accepted")
	}
}

func TestPackRejectsWindowsCaseCollision(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"plugins/solver.py", "plugins/SOLVER.py"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Pack(filepath.Join(t.TempDir(), "collision.ufp"), PackOptions{
		Root: root, BuildID: "bad", MasterKey: master, SigningKey: priv,
		Paths: []string{"plugins/solver.py", "plugins/SOLVER.py"},
	}); err == nil {
		t.Fatal("case-fold path collision was accepted")
	}
}

func TestExtractRemovesUnexpectedRuntimeFiles(t *testing.T) {
	root := t.TempDir()
	rel := "plugins/ezsolver/service.py"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(rel))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("print('signed')"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, master, pub := newTestBundle(t, root, []string{rel})
	dest := filepath.Join(t.TempDir(), "runtime")
	if _, err := Extract(bundle, dest, master, pub); err != nil {
		t.Fatal(err)
	}
	unexpected := filepath.Join(dest, "plugins", "ezsolver", "sitecustomize.py")
	if err := os.WriteFile(unexpected, []byte("raise SystemExit('injected')"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(bundle, dest, master, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unexpected); !os.IsNotExist(err) {
		t.Fatalf("unexpected runtime file survived verification: %v", err)
	}
}

func TestValidateBuildIDRejectsUnsafeDirectoryNames(t *testing.T) {
	for _, id := range []string{".", "..", "CON", "NUL.txt", "-leading", "trailing-", "a/b"} {
		if err := ValidateBuildID(id); err == nil {
			t.Errorf("unsafe build id %q was accepted", id)
		}
	}
	for _, id := range []string{"a", "release-1", "win-amd64-20260810"} {
		if err := ValidateBuildID(id); err != nil {
			t.Errorf("safe build id %q rejected: %v", id, err)
		}
	}
}

func TestExtractExpectedRejectsMismatchWithoutChangingDestination(t *testing.T) {
	root := t.TempDir()
	rel := "plugins/ezsolver/service.py"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(rel))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("print('signed')"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, master, pub := newTestBundle(t, root, []string{rel})
	dest := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dest, "keep.txt")
	if err := os.WriteFile(marker, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractExpected(bundle, dest, master, pub, "different-build"); err == nil {
		t.Fatal("mismatched BuildID was accepted")
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "previous" {
		t.Fatalf("BuildID mismatch changed destination: body=%q err=%v", body, err)
	}
}
