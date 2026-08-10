package protectedbundle

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var bundleMagic = []byte("UFPKG01\x00")

const (
	bundleVersion          = 1
	manifestPath           = ".umbraforge/manifest.json"
	maxHeaderSize          = 1 << 20
	maxBundleFiles         = 4096
	maxBundleFileSize      = 512 << 20
	maxBundlePlaintextSize = 1 << 30
	maxBundleEnvelopeSize  = maxBundlePlaintextSize + (64 << 20)
)

// PackOptions describes one protected plugin bundle. MasterKey must be exactly
// 32 random bytes. SigningKey is kept only by the build pipeline; the runtime
// receives the corresponding Ed25519 public key.
type PackOptions struct {
	Root       string
	BuildID    string
	MasterKey  []byte
	SigningKey ed25519.PrivateKey
	Paths      []string
}

// Info is the non-secret identity of a verified bundle.
type Info struct {
	BuildID         string `json:"build_id"`
	FileCount       int    `json:"file_count"`
	PlaintextSHA256 string `json:"plaintext_sha256"`
}

type wireHeader struct {
	Version         int    `json:"version"`
	BuildID         string `json:"build_id"`
	Salt            string `json:"salt"`
	Nonce           string `json:"nonce"`
	FileCount       int    `json:"file_count"`
	PlaintextSHA256 string `json:"plaintext_sha256"`
}

type fileMeta struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

type bundleManifest struct {
	Version int        `json:"version"`
	BuildID string     `json:"build_id"`
	Files   []fileMeta `json:"files"`
}

type packedFile struct {
	meta fileMeta
	body []byte
}

// Pack creates an AES-256-GCM encrypted ZIP and signs the complete encrypted
// envelope with Ed25519. No source path or plugin filename is exposed outside
// the ciphertext.
func Pack(out string, opts PackOptions) (Info, error) {
	if len(opts.MasterKey) != 32 {
		return Info{}, fmt.Errorf("master key must be 32 bytes, got %d", len(opts.MasterKey))
	}
	if len(opts.SigningKey) != ed25519.PrivateKeySize {
		return Info{}, fmt.Errorf("signing key must be %d bytes", ed25519.PrivateKeySize)
	}
	if strings.TrimSpace(opts.Root) == "" {
		return Info{}, errors.New("root is required")
	}
	rootAbs, err := filepath.Abs(opts.Root)
	if err != nil {
		return Info{}, fmt.Errorf("resolve payload root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return Info{}, fmt.Errorf("resolve payload root links: %w", err)
	}
	if len(opts.Paths) == 0 {
		return Info{}, errors.New("at least one payload path is required")
	}
	if len(opts.Paths) > maxBundleFiles {
		return Info{}, fmt.Errorf("payload contains too many files: %d", len(opts.Paths))
	}
	buildID := strings.TrimSpace(opts.BuildID)
	if buildID == "" {
		var id [16]byte
		if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
			return Info{}, fmt.Errorf("generate build id: %w", err)
		}
		buildID = hex.EncodeToString(id[:])
	}
	if err := ValidateBuildID(buildID); err != nil {
		return Info{}, err
	}

	seen := make(map[string]struct{}, len(opts.Paths))
	seenFolded := make(map[string]string, len(opts.Paths))
	files := make([]packedFile, 0, len(opts.Paths))
	var totalPayload int64
	for _, raw := range opts.Paths {
		rel, err := cleanBundlePath(raw)
		if err != nil {
			return Info{}, err
		}
		if rel == manifestPath {
			return Info{}, fmt.Errorf("payload path %q is reserved", rel)
		}
		if _, ok := seen[rel]; ok {
			return Info{}, fmt.Errorf("duplicate payload path %q", rel)
		}
		folded := strings.ToLower(rel)
		if previous, ok := seenFolded[folded]; ok {
			return Info{}, fmt.Errorf("case-fold payload path collision %q and %q", previous, rel)
		}
		seen[rel] = struct{}{}
		seenFolded[folded] = rel
		src := filepath.Join(rootResolved, filepath.FromSlash(rel))
		if err := ensureWithin(rootResolved, src); err != nil {
			return Info{}, err
		}
		st, err := os.Lstat(src)
		if err != nil {
			return Info{}, fmt.Errorf("stat %s: %w", rel, err)
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return Info{}, fmt.Errorf("payload %q is not a regular non-link file", rel)
		}
		resolvedSrc, err := filepath.EvalSymlinks(src)
		if err != nil || filepath.Clean(resolvedSrc) != filepath.Clean(src) {
			return Info{}, fmt.Errorf("payload %q traverses a symbolic link or junction", rel)
		}
		if st.Size() > maxBundleFileSize {
			return Info{}, fmt.Errorf("payload %q exceeds %d bytes", rel, maxBundleFileSize)
		}
		totalPayload += st.Size()
		if totalPayload > maxBundlePlaintextSize {
			return Info{}, fmt.Errorf("payload expands beyond %d bytes", maxBundlePlaintextSize)
		}
		body, err := os.ReadFile(src)
		if err != nil {
			return Info{}, fmt.Errorf("read %s: %w", rel, err)
		}
		digest := sha256.Sum256(body)
		files = append(files, packedFile{
			meta: fileMeta{
				Path:   rel,
				Size:   int64(len(body)),
				SHA256: hex.EncodeToString(digest[:]),
				Mode:   uint32(st.Mode().Perm()),
			},
			body: body,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].meta.Path < files[j].meta.Path })

	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	fixedTime := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	manifest := bundleManifest{Version: bundleVersion, BuildID: buildID, Files: make([]fileMeta, 0, len(files))}
	for _, f := range files {
		manifest.Files = append(manifest.Files, f.meta)
		h := &zip.FileHeader{Name: f.meta.Path, Method: zip.Deflate}
		h.SetMode(fs.FileMode(f.meta.Mode))
		h.SetModTime(fixedTime)
		w, err := zw.CreateHeader(h)
		if err != nil {
			_ = zw.Close()
			return Info{}, fmt.Errorf("create zip entry %s: %w", f.meta.Path, err)
		}
		if _, err := w.Write(f.body); err != nil {
			_ = zw.Close()
			return Info{}, fmt.Errorf("write zip entry %s: %w", f.meta.Path, err)
		}
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		_ = zw.Close()
		return Info{}, err
	}
	mh := &zip.FileHeader{Name: manifestPath, Method: zip.Deflate}
	mh.SetMode(0o600)
	mh.SetModTime(fixedTime)
	mw, err := zw.CreateHeader(mh)
	if err != nil {
		_ = zw.Close()
		return Info{}, err
	}
	if _, err := mw.Write(manifestJSON); err != nil {
		_ = zw.Close()
		return Info{}, err
	}
	if err := zw.Close(); err != nil {
		return Info{}, fmt.Errorf("close zip: %w", err)
	}

	plainDigest := sha256.Sum256(plain.Bytes())
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return Info{}, err
	}
	key := deriveKey(opts.MasterKey, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Info{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Info{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Info{}, err
	}
	header := wireHeader{
		Version:         bundleVersion,
		BuildID:         buildID,
		Salt:            base64.RawStdEncoding.EncodeToString(salt),
		Nonce:           base64.RawStdEncoding.EncodeToString(nonce),
		FileCount:       len(files),
		PlaintextSHA256: hex.EncodeToString(plainDigest[:]),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return Info{}, err
	}
	if len(headerJSON) > maxHeaderSize {
		return Info{}, errors.New("bundle header too large")
	}

	var prefix bytes.Buffer
	prefix.Write(bundleMagic)
	_ = binary.Write(&prefix, binary.BigEndian, uint32(len(headerJSON)))
	prefix.Write(headerJSON)
	ciphertext := gcm.Seal(nil, nonce, plain.Bytes(), prefix.Bytes())

	var signed bytes.Buffer
	signed.Write(prefix.Bytes())
	_ = binary.Write(&signed, binary.BigEndian, uint64(len(ciphertext)))
	signed.Write(ciphertext)
	signature := ed25519.Sign(opts.SigningKey, signed.Bytes())

	var envelope bytes.Buffer
	envelope.Write(signed.Bytes())
	_ = binary.Write(&envelope, binary.BigEndian, uint16(len(signature)))
	envelope.Write(signature)
	if err := atomicWriteFile(out, envelope.Bytes(), 0o600); err != nil {
		return Info{}, err
	}
	return Info{BuildID: buildID, FileCount: len(files), PlaintextSHA256: header.PlaintextSHA256}, nil
}

// Extract verifies the publisher signature before decrypting, authenticates the
// ciphertext with AES-GCM, validates every extracted hash, then atomically
// installs an exact immutable runtime tree.
func Extract(bundlePath, dest string, masterKey []byte, verifyKey ed25519.PublicKey) (Info, error) {
	return extract(bundlePath, dest, masterKey, verifyKey, "")
}

// ExtractExpected additionally binds activation to the BuildID compiled into
// the executable. A mismatch is rejected before touching the destination.
func ExtractExpected(bundlePath, dest string, masterKey []byte, verifyKey ed25519.PublicKey, expectedBuildID string) (Info, error) {
	if err := ValidateBuildID(expectedBuildID); err != nil {
		return Info{}, err
	}
	return extract(bundlePath, dest, masterKey, verifyKey, expectedBuildID)
}

func extract(bundlePath, dest string, masterKey []byte, verifyKey ed25519.PublicKey, expectedBuildID string) (Info, error) {
	if len(masterKey) != 32 {
		return Info{}, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	if len(verifyKey) != ed25519.PublicKeySize {
		return Info{}, fmt.Errorf("verify key must be %d bytes", ed25519.PublicKeySize)
	}
	st, err := os.Lstat(bundlePath)
	if err != nil {
		return Info{}, err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 || st.Size() > maxBundleEnvelopeSize {
		return Info{}, errors.New("protected bundle is not a bounded regular file")
	}
	blob, err := os.ReadFile(bundlePath)
	if err != nil {
		return Info{}, err
	}
	header, plaintext, err := openEnvelope(blob, masterKey, verifyKey)
	if err != nil {
		return Info{}, err
	}
	if expectedBuildID != "" && header.BuildID != expectedBuildID {
		return Info{}, fmt.Errorf("protected plugin build id %q does not match executable %q", header.BuildID, expectedBuildID)
	}
	manifest, entries, err := readAndVerifyArchive(plaintext, header)
	if err != nil {
		return Info{}, err
	}
	info := Info{BuildID: header.BuildID, FileCount: header.FileCount, PlaintextSHA256: header.PlaintextSHA256}
	if verifyExtracted(dest, manifest) == nil {
		return info, nil
	}

	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Info{}, err
	}
	tmp, err := os.MkdirTemp(parent, ".umbraforge-runtime-")
	if err != nil {
		return Info{}, err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = os.RemoveAll(tmp)
		}
	}()
	for _, meta := range manifest.Files {
		body := entries[meta.Path]
		out := filepath.Join(tmp, filepath.FromSlash(meta.Path))
		if err := ensureWithin(tmp, out); err != nil {
			return Info{}, err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return Info{}, err
		}
		mode := fs.FileMode(meta.Mode)
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(out, body, mode); err != nil {
			return Info{}, err
		}
	}
	marker, _ := json.Marshal(manifest)
	if err := os.MkdirAll(filepath.Join(tmp, ".umbraforge"), 0o700); err != nil {
		return Info{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, filepath.FromSlash(manifestPath)), marker, 0o600); err != nil {
		return Info{}, err
	}
	if err := replaceDirAtomically(tmp, dest); err != nil {
		return Info{}, err
	}
	keepTmp = true // tmp was renamed to dest; do not remove dest in deferred cleanup
	return info, nil
}

func openEnvelope(blob, masterKey []byte, verifyKey ed25519.PublicKey) (wireHeader, []byte, error) {
	var zero wireHeader
	if len(blob) < len(bundleMagic)+4+8+2+ed25519.SignatureSize || !bytes.Equal(blob[:len(bundleMagic)], bundleMagic) {
		return zero, nil, errors.New("invalid protected bundle magic")
	}
	off := len(bundleMagic)
	headerLen := int(binary.BigEndian.Uint32(blob[off : off+4]))
	off += 4
	if headerLen <= 0 || headerLen > maxHeaderSize || off+headerLen+8 > len(blob) {
		return zero, nil, errors.New("invalid protected bundle header length")
	}
	headerEnd := off + headerLen
	headerJSON := blob[off:headerEnd]
	var header wireHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return zero, nil, fmt.Errorf("decode protected bundle header: %w", err)
	}
	if header.Version != bundleVersion || strings.TrimSpace(header.BuildID) == "" || header.FileCount < 0 || header.FileCount > maxBundleFiles {
		return zero, nil, errors.New("unsupported protected bundle header")
	}
	off = headerEnd
	cipherLen := binary.BigEndian.Uint64(blob[off : off+8])
	off += 8
	if cipherLen > uint64(len(blob)-off) || cipherLen > uint64(maxBundleFileSize*4) {
		return zero, nil, errors.New("invalid protected bundle ciphertext length")
	}
	cipherEnd := off + int(cipherLen)
	if cipherEnd+2 > len(blob) {
		return zero, nil, errors.New("truncated protected bundle signature")
	}
	sigLen := int(binary.BigEndian.Uint16(blob[cipherEnd : cipherEnd+2]))
	if sigLen != ed25519.SignatureSize || cipherEnd+2+sigLen != len(blob) {
		return zero, nil, errors.New("invalid protected bundle signature length")
	}
	signedData := blob[:cipherEnd]
	signature := blob[cipherEnd+2:]
	if !ed25519.Verify(verifyKey, signedData, signature) {
		return zero, nil, errors.New("protected bundle signature verification failed")
	}

	salt, err := base64.RawStdEncoding.DecodeString(header.Salt)
	if err != nil || len(salt) != 32 {
		return zero, nil, errors.New("invalid protected bundle salt")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(header.Nonce)
	if err != nil {
		return zero, nil, errors.New("invalid protected bundle nonce")
	}
	block, err := aes.NewCipher(deriveKey(masterKey, salt))
	if err != nil {
		return zero, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return zero, nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return zero, nil, errors.New("invalid protected bundle nonce size")
	}
	prefixEnd := headerEnd
	plaintext, err := gcm.Open(nil, nonce, blob[off:cipherEnd], blob[:prefixEnd])
	if err != nil {
		return zero, nil, errors.New("protected bundle decryption/authentication failed")
	}
	digest := sha256.Sum256(plaintext)
	if !hmac.Equal([]byte(hex.EncodeToString(digest[:])), []byte(strings.ToLower(header.PlaintextSHA256))) {
		return zero, nil, errors.New("protected bundle plaintext hash mismatch")
	}
	return header, plaintext, nil
}

func readAndVerifyArchive(plaintext []byte, header wireHeader) (bundleManifest, map[string][]byte, error) {
	var zero bundleManifest
	zr, err := zip.NewReader(bytes.NewReader(plaintext), int64(len(plaintext)))
	if err != nil {
		return zero, nil, fmt.Errorf("open protected archive: %w", err)
	}
	if len(zr.File) > maxBundleFiles+1 {
		return zero, nil, fmt.Errorf("protected archive has too many entries: %d", len(zr.File))
	}
	entries := make(map[string][]byte, len(zr.File))
	entryFolded := make(map[string]string, len(zr.File))
	var totalExpanded int64
	for _, f := range zr.File {
		rel, err := cleanBundlePath(f.Name)
		if err != nil {
			return zero, nil, err
		}
		if f.Mode()&os.ModeSymlink != 0 || f.FileInfo().IsDir() {
			return zero, nil, fmt.Errorf("unsupported protected archive entry %q", rel)
		}
		if _, dup := entries[rel]; dup {
			return zero, nil, fmt.Errorf("duplicate protected archive entry %q", rel)
		}
		folded := strings.ToLower(rel)
		if previous, dup := entryFolded[folded]; dup {
			return zero, nil, fmt.Errorf("case-fold archive collision %q and %q", previous, rel)
		}
		entryFolded[folded] = rel
		r, err := f.Open()
		if err != nil {
			return zero, nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(r, maxBundleFileSize+1))
		closeErr := r.Close()
		if readErr != nil {
			return zero, nil, readErr
		}
		if closeErr != nil {
			return zero, nil, closeErr
		}
		if len(body) > maxBundleFileSize {
			return zero, nil, fmt.Errorf("protected archive entry %q is too large", rel)
		}
		totalExpanded += int64(len(body))
		if totalExpanded > maxBundlePlaintextSize {
			return zero, nil, fmt.Errorf("protected archive expands beyond %d bytes", maxBundlePlaintextSize)
		}
		entries[rel] = body
	}
	manifestJSON, ok := entries[manifestPath]
	if !ok {
		return zero, nil, errors.New("protected archive manifest is missing")
	}
	delete(entries, manifestPath)
	var manifest bundleManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return zero, nil, fmt.Errorf("decode protected archive manifest: %w", err)
	}
	if manifest.Version != bundleVersion || manifest.BuildID != header.BuildID || len(manifest.Files) != header.FileCount {
		return zero, nil, errors.New("protected archive manifest does not match header")
	}
	if len(entries) != len(manifest.Files) {
		return zero, nil, errors.New("protected archive entry count does not match manifest")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	manifestFolded := make(map[string]string, len(manifest.Files))
	for _, meta := range manifest.Files {
		rel, err := cleanBundlePath(meta.Path)
		if err != nil || rel != meta.Path {
			return zero, nil, fmt.Errorf("invalid manifest path %q", meta.Path)
		}
		if _, dup := seen[rel]; dup {
			return zero, nil, fmt.Errorf("duplicate manifest path %q", rel)
		}
		folded := strings.ToLower(rel)
		if previous, dup := manifestFolded[folded]; dup {
			return zero, nil, fmt.Errorf("case-fold manifest collision %q and %q", previous, rel)
		}
		seen[rel] = struct{}{}
		manifestFolded[folded] = rel
		body, ok := entries[rel]
		if !ok || int64(len(body)) != meta.Size {
			return zero, nil, fmt.Errorf("protected payload size mismatch for %q", rel)
		}
		digest := sha256.Sum256(body)
		if !hmac.Equal([]byte(hex.EncodeToString(digest[:])), []byte(strings.ToLower(meta.SHA256))) {
			return zero, nil, fmt.Errorf("protected payload hash mismatch for %q", rel)
		}
	}
	return manifest, entries, nil
}

func verifyExtracted(dest string, manifest bundleManifest) error {
	if strings.TrimSpace(dest) == "" {
		return errors.New("empty extraction destination")
	}
	expected := make(map[string]fileMeta, len(manifest.Files))
	for _, meta := range manifest.Files {
		expected[meta.Path] = meta
		path := filepath.Join(dest, filepath.FromSlash(meta.Path))
		if err := ensureWithin(dest, path); err != nil {
			return err
		}
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() {
			return errors.New("runtime payload missing, changed, or not a regular file")
		}
		body, err := os.ReadFile(path)
		if err != nil || int64(len(body)) != meta.Size {
			return errors.New("runtime payload missing or changed")
		}
		digest := sha256.Sum256(body)
		if !hmac.Equal([]byte(hex.EncodeToString(digest[:])), []byte(strings.ToLower(meta.SHA256))) {
			return errors.New("runtime payload hash mismatch")
		}
	}
	markerJSON, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	seen := 0
	err = filepath.WalkDir(dest, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dest {
			return nil
		}
		relOS, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected runtime payload entry %q", rel)
		}
		if rel == manifestPath {
			body, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(body, markerJSON) {
				return errors.New("runtime manifest marker is missing or changed")
			}
			return nil
		}
		if _, ok := expected[rel]; !ok {
			return fmt.Errorf("unexpected runtime payload file %q", rel)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(expected) {
		return errors.New("runtime payload file count mismatch")
	}
	return nil
}

// ValidateBuildID ensures a signed build identifier is also a safe single
// directory component on Windows. In particular, ".", "..", trailing dots,
// and device names must never reach filepath.Join/RemoveAll.
func ValidateBuildID(id string) error {
	id = strings.TrimSpace(id)
	if len(id) < 1 || len(id) > 128 {
		return fmt.Errorf("invalid protected build id length")
	}
	isAlphaNum := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	if !isAlphaNum(id[0]) || !isAlphaNum(id[len(id)-1]) {
		return fmt.Errorf("protected build id must start and end with an ASCII letter or digit")
	}
	for i := 0; i < len(id); i++ {
		if isAlphaNum(id[i]) || id[i] == '.' || id[i] == '_' || id[i] == '-' {
			continue
		}
		return fmt.Errorf("invalid character in protected build id")
	}
	if strings.ContainsAny(id, `/\\:`) || isReservedWindowsComponent(id) {
		return fmt.Errorf("Windows-reserved protected build id %q", id)
	}
	return nil
}

func cleanBundlePath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("invalid protected payload path %q", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || clean != raw || strings.HasPrefix(clean, "../") || strings.Contains(clean, ":") {
		return "", fmt.Errorf("invalid protected payload path %q", raw)
	}
	for _, component := range strings.Split(clean, "/") {
		if isReservedWindowsComponent(component) {
			return "", fmt.Errorf("Windows-reserved protected payload path %q", raw)
		}
	}
	return clean, nil
}

func isReservedWindowsComponent(component string) bool {
	if component == "" || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return true
	}
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

func ensureWithin(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("protected payload escapes destination: %q", candidate)
	}
	return nil
}

func deriveKey(master, salt []byte) []byte {
	extract := hmac.New(sha256.New, salt)
	_, _ = extract.Write(master)
	prk := extract.Sum(nil)
	expand := hmac.New(sha256.New, prk)
	_, _ = expand.Write([]byte("umbraforge/protected-plugin-bundle/v1"))
	_, _ = expand.Write([]byte{1})
	return expand.Sum(nil)[:32]
}

func atomicWriteFile(path string, body []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".umbraforge-bundle-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmpName, path)
}

func replaceDirAtomically(tmp, dest string) error {
	backup := dest + ".old"
	_ = os.RemoveAll(backup)
	hadDest := false
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return err
		}
		hadDest = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		if hadDest {
			_ = os.Rename(backup, dest)
		}
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}
