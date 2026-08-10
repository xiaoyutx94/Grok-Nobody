package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"

	"github.com/umbraforge/desktop/internal/protectedbundle"
	"github.com/umbraforge/desktop/internal/protectionconfig"
)

func materializeProtectedRoot(bundlePath, dataDir, expectedBuildID string, master []byte, pub ed25519.PublicKey) (string, error) {
	if err := protectedbundle.ValidateBuildID(expectedBuildID); err != nil {
		return "", err
	}
	dest := filepath.Join(dataDir, "protected-runtime", expectedBuildID)
	_, err := protectedbundle.ExtractExpected(bundlePath, dest, master, pub, expectedBuildID)
	if err != nil {
		return "", fmt.Errorf("verify/decrypt protected plugins: %w", err)
	}
	return dest, nil
}

func resolveProjectRoot(explicitRoot, dataDir string) (string, error) {
	if protectionconfig.Enabled() {
		if explicitRoot != "" {
			return "", fmt.Errorf("-root is disabled in protected releases; signed plugins.ufp is mandatory")
		}
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve executable for protected plugins: %w", err)
		}
		master, pub, err := protectionconfig.Credentials()
		if err != nil {
			return "", err
		}
		bundlePath := filepath.Join(filepath.Dir(exe), "plugins.ufp")
		if st, err := os.Stat(bundlePath); err != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("protected plugin bundle missing next to executable: %s", bundlePath)
		}
		return materializeProtectedRoot(bundlePath, dataDir, protectionconfig.BuildID(), master, pub)
	}
	if explicitRoot != "" {
		return resolveRoot(explicitRoot), nil
	}
	return resolveRoot(""), nil
}
