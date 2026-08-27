package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R2-15: the updater used to check only the ELF magic before replacing the
// running executable. It now requires a checksums manifest, verifies the
// downloaded binary's SHA-256 against the entry for the exact asset name, and
// verifies the manifest's ed25519 signature when a key is pinned.

func writeTempBinary(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evilcode-linux-amd64")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func sumOf(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestVerifyChecksumManifestMatchesTheAssetEntry(t *testing.T) {
	bin := []byte("\x7fELF-fake-binary")
	path := writeTempBinary(t, bin)
	manifest := []byte("sha256  evilcode-linux-amd64 " + sumOf(t, bin) + "\n" +
		"sha256  other-asset deadbeef\n")
	if err := verifyChecksumManifest(manifest, nil, nil, "evilcode-linux-amd64", path); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyChecksumManifestRequiresTheManifest(t *testing.T) {
	// An empty manifest has no entry: the binary is refused either way, and
	// the fetch-level refusal for a release with no manifest is separate.
	path := writeTempBinary(t, []byte("anything"))
	err := verifyChecksumManifest(nil, nil, nil, "evilcode-linux-amd64", path)
	if err == nil || !strings.Contains(err.Error(), "no checksum entry") {
		t.Fatalf("err = %v, want a missing-entry refusal", err)
	}
}

func TestVerifyChecksumManifestRefusesAMismatchedBinary(t *testing.T) {
	manifest := []byte("sha256  evilcode-linux-amd64 " + strings.Repeat("a", 64) + "\n")
	path := writeTempBinary(t, []byte("not that binary"))
	err := verifyChecksumManifest(manifest, nil, nil, "evilcode-linux-amd64", path)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
}

func TestVerifyChecksumManifestRefusesAnUnknownAsset(t *testing.T) {
	manifest := []byte("sha256  some-other-asset " + strings.Repeat("b", 64) + "\n")
	path := writeTempBinary(t, []byte("x"))
	err := verifyChecksumManifest(manifest, nil, nil, "evilcode-linux-amd64", path)
	if err == nil || !strings.Contains(err.Error(), "no checksum entry") {
		t.Fatalf("err = %v, want a missing-entry refusal", err)
	}
}

func TestVerifyChecksumManifestEnforcesTheSignatureWhenAKeyIsPinned(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("sha256  evilcode-linux-amd64 " + strings.Repeat("c", 64) + "\n")
	path := writeTempBinary(t, []byte("x"))

	// An unsigned manifest is refused when a key is pinned.
	if err := verifyChecksumManifest(manifest, nil, pub, "evilcode-linux-amd64", path); err == nil ||
		!strings.Contains(err.Error(), "no checksums.txt.sig") {
		t.Fatalf("err = %v, want a missing-signature refusal", err)
	}

	// A forged signature is refused even though the manifest has the entry.
	if err := verifyChecksumManifest(manifest, []byte("forged"), pub, "evilcode-linux-amd64", path); err == nil ||
		!strings.Contains(err.Error(), "signature verification") {
		t.Fatalf("err = %v, want a signature failure", err)
	}

	// A good signature admits the manifest — and the checksum check still runs.
	if err := verifyChecksumManifest(manifest, ed25519.Sign(priv, manifest), pub, "evilcode-linux-amd64", path); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want the checksum stage after signature success", err)
	}
}

func TestSyncDirSurvivesAMissingDirectory(t *testing.T) {
	if err := syncDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("a missing directory synced without error")
	}
}
