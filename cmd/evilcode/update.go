package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"evilcode/internal/daemon"
	"evilcode/internal/tuicmd"
)

// releaseLatest is Forgejo's "latest release" endpoint for the canonical
// evilcode repository. `update` downloads a prebuilt binary from here rather
// than building from a local checkout, so it works from any directory and
// needs no Go toolchain — only a network connection and a writable install
// path.
const (
	releaseLatest       = "https://git.evileko.dev/api/v1/repos/evileko/evilcode/releases/latest"
	releaseHost         = "git.evileko.dev"
	maxReleaseJSONBytes = 2 << 20
	maxBinaryBytes      = 512 << 20
)

// httpClient bounds every request `update` makes so a hung connection can
// never wedge it indefinitely. Two minutes is generous for a ~30 MB binary on
// a slow link and short enough that a dead server fails fast.
var httpClient = &http.Client{
	Timeout: 2 * time.Minute,
	CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("update redirect uses insecure scheme %q", req.URL.Scheme)
		}
		// A release download may legitimately redirect to object storage, but a
		// Forgejo credential belongs only to Forgejo.
		if !strings.EqualFold(req.URL.Hostname(), releaseHost) {
			req.Header.Del("Authorization")
		}
		return nil
	},
}

// release is the subset of Forgejo's release JSON that update acts on.
type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

// asset is one downloadable file attached to a release.
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func (r release) findAsset(name string) *asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func (r release) assetNames() string {
	names := make([]string, len(r.Assets))
	for i, a := range r.Assets {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

// runUpdate fetches the newest Forgejo release, downloads the binary built
// for this OS/arch, and atomically swaps it in over the running executable.
// It never touches the installed binary before the download completes.
func runUpdate() error {
	exe, mode, dir, err := updateTarget()
	if err != nil {
		return err
	}
	rel, err := latestRelease()
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	tag := strings.TrimSpace(rel.TagName)
	if tag != "" && tag == tuicmd.Version {
		fmt.Printf("already up to date (%s)\n", tuicmd.Version)
		return nil
	}
	want := "evilcode-" + runtime.GOOS + "-" + runtime.GOARCH
	a := rel.findAsset(want)
	if a == nil {
		return fmt.Errorf("update: release %s has no binary for %s/%s (assets: %s)", tag, runtime.GOOS, runtime.GOARCH, rel.assetNames())
	}
	tmpFile, err := os.CreateTemp(dir, ".evilcode-update-*")
	if err != nil {
		return fmt.Errorf("update: creating temporary binary: %w", err)
	}
	tmp := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmp)
	}()
	if err := download(a.URL, tmpFile); err != nil {
		return fmt.Errorf("update: downloading %s: %w", a.Name, err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("update: syncing downloaded binary: %w", err)
	}
	if err := tmpFile.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("update: preserving executable mode: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("update: closing downloaded binary: %w", err)
	}
	// A running daemon holds the old binary in memory; swapping the file on
	// disk does not change the version it serves. Stop it first so the next
	// `evilcode serve` picks up the new binary. Done after the download
	// verifies, so a failed download never disturbs a running daemon.
	stopDaemonIfRunning()
	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("update: installing %s: %w\nmanual: curl -fL -o %s %s", exe, err, shellQuote(exe), a.URL)
	}
	fmt.Printf("updated %s: %s -> %s\n", exe, tuicmd.Version, tag)
	return nil
}

// latestRelease fetches and decodes the newest release from Forgejo.
func latestRelease() (release, error) {
	var r release
	resp, err := httpGet(releaseLatest)
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return r, fmt.Errorf("latest release: HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseJSONBytes+1))
	if err != nil {
		return r, err
	}
	if len(body) > maxReleaseJSONBytes {
		return r, fmt.Errorf("latest release response exceeds %d bytes", maxReleaseJSONBytes)
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, err
	}
	if r.TagName == "" {
		return r, fmt.Errorf("latest release: no tag_name in response")
	}
	return r, nil
}

// download writes rawURL into a newly-created temporary file, bounding the
// response and checking the executable header before it can replace anything.
func download(rawURL string, dst *os.File) error {
	resp, err := httpGet(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, maxBinaryBytes+1))
	if err != nil {
		return err
	}
	if n > maxBinaryBytes {
		return fmt.Errorf("download exceeds %d bytes", maxBinaryBytes)
	}
	if n < 4 {
		return fmt.Errorf("download is empty or truncated")
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var magic [4]byte
	if _, err := io.ReadFull(dst, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != "\x7fELF" {
		return fmt.Errorf("download is not a Linux executable")
	}
	return nil
}

// httpGet issues a GET. A public repo answers without credentials; a private
// mirror or a gated download returns 401/403, in which case we retry once with
// a Basic header drawn from `git credential fill` for releaseHost. The prompt
// is disabled so a missing credential fails fast instead of hanging.
func httpGet(rawURL string) (*http.Response, error) {
	req, err := newGet(rawURL)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return resp, nil
	}
	resp.Body.Close()
	auth, err := credentialHeader(releaseHost)
	if err != nil {
		return nil, err
	}
	req, err = newGet(rawURL)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	return httpClient.Do(req)
}

func newGet(rawURL string) (*http.Request, error) {
	if err := validateReleaseURL(rawURL); err != nil {
		return nil, err
	}
	return http.NewRequest(http.MethodGet, rawURL, nil)
}

// validateReleaseURL prevents a release response from turning its asset URL
// into a credential exfiltration request. Credentials are fetched for and sent
// only to the canonical HTTPS Forgejo host.
func validateReleaseURL(rawURL string) error {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid update URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("update URL must use HTTPS")
	}
	if u.User != nil {
		return fmt.Errorf("update URL must not contain user information")
	}
	if !strings.EqualFold(u.Hostname(), releaseHost) || u.Port() != "" {
		return fmt.Errorf("update URL host %q is not %s", u.Host, releaseHost)
	}
	return nil
}

// credentialHeader returns an "Authorization: Basic ..." header for host using
// the credential git itself stores for HTTPS pushes to the same host.
func credentialHeader(host string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("protocol=https\nhost=%s\n\n", host))
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("credential lookup for %s timed out: %w", host, ctx.Err())
		}
		return "", fmt.Errorf("no stored credential for %s: %w", host, err)
	}
	user, pass := "", ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "username="):
			user = strings.TrimPrefix(line, "username=")
		case strings.HasPrefix(line, "password="):
			pass = strings.TrimPrefix(line, "password=")
		}
	}
	if user == "" || pass == "" {
		return "", fmt.Errorf("credential for %s has no username or password", host)
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)), nil
}

// updateTarget resolves the running executable and confirms its directory is
// writable by creating and removing a probe file. The mode is returned so the
// replacement keeps the same permissions rather than imposing 0755.
func updateTarget() (exe string, mode os.FileMode, dir string, err error) {
	exe, err = os.Executable()
	if err != nil {
		return "", 0, "", fmt.Errorf("update: finding executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", 0, "", fmt.Errorf("update: resolving executable: %w", err)
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", 0, "", fmt.Errorf("update: stating executable: %w", err)
	}
	dir = filepath.Dir(exe)
	probe, err := os.CreateTemp(dir, ".evilcode-update-probe-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("update: install path is not writable; manual: curl -fL -o %s <download url>", shellQuote(exe))
	}
	probeName := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probeName)
	return exe, info.Mode(), dir, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// stopDaemonIfRunning requests a graceful shutdown of the per-user daemon when
// one is reachable. A failed dial is not an error — it means no daemon is
// running (or its socket is stale), in which case there is nothing to stop. A
// failed stop is reported as a warning rather than aborting the update: the
// binary still swaps in, and the user can restart the daemon manually to pick
// up the new version.
func stopDaemonIfRunning() {
	stopDaemonIfRunningAt(daemon.SocketPath())
}

// stopDaemonIfRunningAt is the testable form: it stops whatever daemon answers
// at path, or is a no-op when nothing does.
func stopDaemonIfRunningAt(path string) {
	client, err := daemon.DialPath(path)
	if err != nil {
		return
	}
	defer client.Close()
	// Bound the shutdown so a wedged daemon cannot hang the update.
	_ = client.SetDeadline(10 * time.Second)
	if err := client.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "update: could not stop the running daemon (it will keep the old binary until restarted): %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "update: stopped the running daemon")
}
