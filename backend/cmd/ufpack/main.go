package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/umbraforge/desktop/internal/protectbuild"
)

func main() {
	root := flag.String("root", "", "repository root containing plugin payloads")
	manifest := flag.String("manifest", "", "JSON payload manifest")
	out := flag.String("out", "", "encrypted bundle output (.ufp)")
	config := flag.String("config-out", "", "generated protected Go config")
	buildID := flag.String("build-id", "", "release/build identifier (random if empty)")
	flag.Parse()

	var master []byte
	if raw := os.Getenv("UMBRAFORGE_BUNDLE_MASTER_KEY_B64"); raw != "" {
		var err error
		master, err = base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			log.Fatalf("invalid UMBRAFORGE_BUNDLE_MASTER_KEY_B64: %v", err)
		}
	}
	var signing ed25519.PrivateKey
	if raw := os.Getenv("UMBRAFORGE_BUNDLE_SIGNING_KEY_B64"); raw != "" {
		decoded, err := base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			log.Fatalf("invalid UMBRAFORGE_BUNDLE_SIGNING_KEY_B64: %v", err)
		}
		signing = ed25519.PrivateKey(decoded)
	}
	result, err := protectbuild.Build(protectbuild.Options{
		Root: *root, ManifestPath: *manifest, BundleOut: *out, ConfigOut: *config,
		BuildID: *buildID, MasterKey: master, SigningKey: signing,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("protected bundle ready: build=%s files=%d sha256=%s\n",
		result.Info.BuildID, result.Info.FileCount, result.Info.PlaintextSHA256)
}
