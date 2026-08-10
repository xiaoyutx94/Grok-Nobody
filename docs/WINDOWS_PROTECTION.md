# Windows release protection model

## What this build protects

The protected Windows pipeline (`scripts/build-windows-protected.sh`) uses layered, independently verifiable controls:

1. **Go code obfuscation** — UmbraForge, VeloraTurn and Auralith are built with Garble using a fresh random seed, `-trimpath`, disabled VCS metadata, stripped symbols/debug data and an empty Go build ID.
2. **Encrypted plugin delivery** — the Windows installer contains only the protected executable and `plugins.ufp`. The latter is a ZIP payload encrypted as one authenticated envelope with **AES-256-GCM**. Plugin names, source paths, Python source, patch JavaScript and plugin binaries are ciphertext in the installed program directory.
3. **Bundle integrity** — every encrypted envelope is signed with **Ed25519**. The private key remains in the narrowly scoped `ufpack` build process; only the verification key is compiled into the paired executable. Verification happens before decryption. A stable CI-held Ed25519 key is mandatory in production mode; an ephemeral local key proves pairing, not publisher identity.
4. **Per-build key separation** — every build receives a new random 256-bit content key unless the release operator explicitly provides one. The executable stores two independent XOR shares, not a printable key string, and Garble renames their surrounding code.
5. **Runtime integrity** — every decrypted file has a signed SHA-256 manifest. Changed, missing, symlinked, case-colliding, reserved-name, or unexpected files make the exact immutable tree get rebuilt. Logs, Python virtualenvs and Docker staging live in a separate mutable state root.
6. **Installer minimization** — raw `plugins/`, Go source, Python tests, cache files and an external web directory are not shipped. The Vue UI is embedded in the protected Go executable.
7. **Patched toolchain and dependency gates** — release builds refuse to run without Go 1.26.5. `govulncheck` is required to report zero reachable Go vulnerabilities, and the frontend production dependency audit must report no known vulnerabilities.
8. **Docker disclosure reduction** — `docker cp` uses private random staging directories that are deleted after copying. Protected builds refuse to reuse or create code-bearing `docker commit` images and remove legacy captured-image tags during redeployment. The running container remains readable to the Docker owner.
9. **Safe packaging** — Inno Setup creates a per-user installer. Production mode refuses to build without a stable Ed25519 key and an external Authenticode signing script, signs Windows helpers before encryption, signs the main EXE before ISCC, then signs the installer and hashes final artifacts.

## Security boundary (important)

No client-side program can make locally executed code impossible to recover. The executable must eventually reconstruct the content key, and Windows/Linux must eventually map executable instructions or Python code into memory. An administrator, debugger or memory-dump analyst can therefore recover runtime material with enough effort.

This design protects against archive inspection, source browsing, simple string extraction, casual decompilation, binary replacement and unsigned bundle tampering. It **raises reverse-engineering cost**; it does not provide mathematical secrecy after execution. Secrets that must never reach a customer machine belong on a server-side service.

## Explicitly excluded techniques

The project deliberately does **not** disable Defender, add antivirus exclusions, detect sandboxes/VMs, kill analysis tools, inject into other processes, install kernel drivers or implement anti-debug retaliation. Those techniques create malware-like behavior, operational instability and false positives. The aggressive Garble `-literals -tiny` combination was rejected after Defender quarantined a test binary; the release uses balanced Garble settings and treats antivirus acceptance as a quality gate.

UPX is also not used: it is trivial to unpack and commonly increases antivirus false positives.

## Key management

By default `ufpack` generates an ephemeral AES key and Ed25519 signing key for a local verification build. The temporary generated Go file and dedicated Go build cache are removed by a shell `trap` after compilation. Such a build is not a trusted public release.

For repeatable controlled releases, CI can provide:

- `UMBRAFORGE_BUNDLE_MASTER_KEY_B64` — raw-base64 32-byte AES master key.
- `UMBRAFORGE_BUNDLE_SIGNING_KEY_B64` — raw-base64 64-byte Ed25519 private key.

These variables must be stored in a CI secret manager, never committed or printed. The build script removes them from the global child-process environment and exposes them only to `ufpack`. Public distribution must set `UMBRAFORGE_RELEASE=1` and `UMBRAFORGE_SIGN_SCRIPT` to an executable signer backed by a hardware/managed Authenticode certificate; release mode verifies every resulting signature.

## Verification gates

A release is acceptable only when all of the following pass:

- cryptographic round-trip, wrong-key, tamper, BuildID binding, traversal, case-collision, resource-bound and exact-runtime-tree tests;
- plugin and backend test suites;
- protected-tag compilation;
- release directory allowlist (`exe`, `plugins.ufp`, checksum only);
- plaintext marker scan of `plugins.ufp`;
- valid PE and installer build;
- Microsoft Defender custom scan with no new detection;
- silent install, protected bundle materialization, API health and three local solver health checks;
- silent uninstall and residual process cleanup.

A trusted Authenticode signature is a production release requirement, but cannot be fabricated by the source tree. Current locally built artifacts are explicitly **unsigned verification artifacts**, not public-release binaries. A self-signed test certificate does not provide public trust and must not be represented as production signing.
