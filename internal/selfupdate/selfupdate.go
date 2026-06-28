// Package selfupdate checks GitHub Releases for newer VaultS3 versions and,
// optionally, downloads + verifies + installs them in place. It only ever
// replaces the running binary — object data, metadata, and config are never
// touched. Installation is checksum-verified (it refuses to run an unverified
// download) and opt-in.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultRepo = "Kodiqa-Solutions/VaultS3"

// Status is the last known update state, surfaced to the dashboard via the API.
type Status struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	CheckedAt       int64  `json:"checkedAt,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Updater checks for and applies updates.
type Updater struct {
	repo    string
	apiBase string // GitHub API base; overridable in tests
	current string
	client  *http.Client
	mu      sync.RWMutex
	status  Status
}

// New creates an updater for the given running version (e.g. "v4.2.6").
func New(currentVersion string) *Updater {
	return &Updater{
		repo:    defaultRepo,
		apiBase: "https://api.github.com",
		current: currentVersion,
		client:  &http.Client{Timeout: 30 * time.Second},
		status:  Status{Current: currentVersion},
	}
}

// LastStatus returns the most recent check result.
func (u *Updater) LastStatus() Status {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.status
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (u *Updater) fetchLatest(ctx context.Context) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", u.apiBase, u.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases API returned %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// Check queries the latest release and records the result. It never errors out
// of band — failures are stored in Status.Error so the caller can keep polling.
func (u *Updater) Check(ctx context.Context) Status {
	st := Status{Current: u.current, CheckedAt: time.Now().Unix()}
	rel, err := u.fetchLatest(ctx)
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Latest = rel.TagName
		st.UpdateAvailable = compareVersions(u.current, rel.TagName) < 0
	}
	u.mu.Lock()
	u.status = st
	u.mu.Unlock()
	return st
}

// Apply downloads the latest release for this platform, verifies its checksum,
// installs it over the running binary, and re-execs into the new version. It
// returns an error (and changes nothing) if anything is off; on success it does
// not return (the process is replaced).
func (u *Updater) Apply(ctx context.Context, allowMajor bool) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("self-update is not supported on Windows; update manually or via Docker")
	}
	rel, err := u.fetchLatest(ctx)
	if err != nil {
		return err
	}
	if compareVersions(u.current, rel.TagName) >= 0 {
		return fmt.Errorf("already up to date (%s)", u.current)
	}
	if !allowMajor && majorOf(u.current) != majorOf(rel.TagName) {
		return fmt.Errorf("refusing to auto-cross a major version (%s → %s); update manually", u.current, rel.TagName)
	}

	want := assetBaseName()
	var assetURL, checksumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			assetURL = a.URL
		case "checksums.txt":
			checksumsURL = a.URL
		}
	}
	if assetURL == "" {
		return fmt.Errorf("no release asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if checksumsURL == "" {
		return fmt.Errorf("release has no checksums.txt; refusing to install an unverified binary")
	}

	checksums, err := u.download(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	wantSum, err := checksumFor(string(checksums), want)
	if err != nil {
		return err
	}

	archive, err := u.download(ctx, assetURL)
	if err != nil {
		return fmt.Errorf("download release: %w", err)
	}
	gotSum := sha256.Sum256(archive)
	if hex.EncodeToString(gotSum[:]) != wantSum {
		return fmt.Errorf("checksum mismatch — refusing to install (expected %s, got %x)", wantSum, gotSum)
	}

	bin, err := extractBinary(archive, serverBinaryName())
	if err != nil {
		return err
	}

	slog.Info("self-update: verified new binary, installing and restarting", "version", rel.TagName)
	return replaceAndRestart(bin)
}

func (u *Updater) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s returned %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 500<<20)) // cap 500MB
}

// IsDocker reports whether we're running inside a container (where self-update is
// pointless — the change is lost on restart; use Watchtower instead).
func IsDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func assetBaseName() string {
	return fmt.Sprintf("vaults3-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

func serverBinaryName() string {
	n := fmt.Sprintf("vaults3-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// checksumFor finds the sha256 hash for a filename in `sha256sum`-format text
// ("<hex>␠␠<name>").
func checksumFor(checksums, name string) (string, error) {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum entry for %s", name)
}

// extractBinary pulls a single file out of a .tar.gz archive.
func extractBinary(targz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		if filepath.Base(hdr.Name) == name {
			return io.ReadAll(io.LimitReader(tr, 500<<20))
		}
	}
	return nil, fmt.Errorf("binary %s not found in archive", name)
}

// replaceAndRestart atomically swaps the running executable and re-execs it.
func replaceAndRestart(newBin []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	tmp := exe + ".update.tmp"
	if err := os.WriteFile(tmp, newBin, 0755); err != nil {
		return fmt.Errorf("write new binary: %w", err)
	}
	// On Unix, renaming over the running executable is safe — the running
	// process keeps its open inode; new starts use the new file.
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install new binary: %w", err)
	}
	// Replace the process image with the new binary, preserving args and env.
	return syscall.Exec(exe, os.Args, os.Environ())
}

// compareVersions returns -1 if a<b, 0 if equal, 1 if a>b. Non-semver inputs
// (e.g. "dev") sort lowest so a dev build is always "older" than any release.
func compareVersions(a, b string) int {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 { // drop pre-release/build metadata
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func majorOf(v string) int {
	if p, ok := parseVersion(v); ok {
		return p[0]
	}
	return -1
}
