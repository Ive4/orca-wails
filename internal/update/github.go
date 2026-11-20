package update

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	macARM64Target   = "aarch64-apple-darwin"
	windowsX64Target = "x86_64-pc-windows-msvc"
	maxMetadataBytes = 8 << 20
	maxChecksumBytes = 16 << 10
	maxReleasePages  = 10
	githubAPI        = "https://api.github.com/repos/"
	githubDownload   = "https://github.com/"
)

// Release identifies a compatible, stable core release from LatestCore.
type Release struct {
	Version         string
	PackageRevision string
	Tag             string
	archive         releaseAsset
	checksum        releaseAsset
	repository      string
}

func (r Release) DisplayVersion() string { return displayVersion(r.Version, r.PackageRevision) }

func (r Release) NewerThan(installed Manifest) bool {
	return installed.OlderThan(Manifest{Version: r.Version, PackageRevision: r.PackageRevision})
}

type releaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type githubRelease struct {
	Tag        string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

// LatestCore returns the newest compatible stable core release, or nil if none
// was published. It considers upstream versions before packaging revisions.
func (s Store) LatestCore(ctx context.Context) (*Release, error) {
	client, err := newReleaseClient(s.Proxy)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	return s.latestCore(ctx, client)
}

func (s Store) latestCore(ctx context.Context, client *http.Client) (*Release, error) {
	if err := validateRepository(s.Repository); err != nil {
		return nil, err
	}
	if err := validateTarget(s.Target); err != nil {
		return nil, err
	}
	var best *Release
	var bestKey versionKey
	for page := 1; page <= maxReleasePages; page++ {
		endpoint := githubAPI + s.Repository + "/releases?page=" + strconv.Itoa(page) + "&per_page=100"
		var releases []githubRelease
		if err := getJSON(ctx, client, endpoint, &releases); err != nil {
			return nil, fmt.Errorf("read GitHub core releases: %w", err)
		}
		for _, candidate := range releases {
			release, key, err := s.compatibleCore(candidate)
			if err != nil {
				return nil, err
			}
			if release != nil && (best == nil || key.compare(bestKey) > 0) {
				best, bestKey = release, key
			}
		}
		if len(releases) < 100 {
			return best, nil
		}
	}
	return nil, fmt.Errorf("core release feed exceeds %d pages", maxReleasePages)
}

func (s Store) compatibleCore(candidate githubRelease) (*Release, versionKey, error) {
	if candidate.Draft || candidate.Prerelease {
		return nil, versionKey{}, nil
	}
	version, revision, ok := coreTagIdentity(candidate.Tag, s.Target)
	if !ok {
		return nil, versionKey{}, nil
	}
	key, err := parseVersionKey(version, revision)
	if err != nil || key.version.pre != "" || key.version.build != "" {
		return nil, versionKey{}, nil
	}
	name := coreArchiveName(version, revision, s.Target)
	archive, archives := findAsset(candidate.Assets, name)
	checksum, checksums := findAsset(candidate.Assets, name+".sha256")
	if archives == 0 || checksums == 0 {
		return nil, versionKey{}, nil
	}
	if archives != 1 || checksums != 1 {
		return nil, versionKey{}, fmt.Errorf("release %s has duplicate core assets", candidate.Tag)
	}
	return &Release{
		Version: version, PackageRevision: revision, Tag: candidate.Tag,
		archive: archive, checksum: checksum, repository: s.Repository,
	}, key, nil
}

// PrepareRelease downloads and verifies a release selected by LatestCore and
// prepares its core slot. Call Commit after the candidate passes health checks.
func (s Store) PrepareRelease(ctx context.Context, release *Release) (*Installed, error) {
	client, err := newReleaseClient(s.Proxy)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	return s.prepareRelease(ctx, client, release)
}

func (s Store) prepareRelease(ctx context.Context, client *http.Client, release *Release) (*Installed, error) {
	if err := validateRepository(s.Repository); err != nil {
		return nil, err
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	if release == nil || release.repository != s.Repository {
		return nil, fmt.Errorf("expected a core release selected for this repository")
	}
	version, revision, ok := coreTagIdentity(release.Tag, s.Target)
	if !ok || version != release.Version || revision != release.PackageRevision {
		return nil, fmt.Errorf("unexpected core release tag")
	}
	key, err := parseVersionKey(version, revision)
	if err != nil || key.version.pre != "" || key.version.build != "" {
		return nil, fmt.Errorf("invalid stable core release version")
	}
	name := coreArchiveName(version, revision, s.Target)
	if release.archive.Name != name || release.checksum.Name != name+".sha256" ||
		!sizeWithin(release.archive, maxArchiveBytes) || !sizeWithin(release.checksum, maxChecksumBytes) {
		return nil, fmt.Errorf("unexpected name or size for core release assets")
	}
	staging, err := s.initialize()
	if err != nil {
		return nil, err
	}
	base := releaseBase(s.Repository, release.Tag)
	checksums, err := getBytes(ctx, client, base+release.checksum.Name, maxChecksumBytes)
	if err != nil {
		return nil, fmt.Errorf("download core checksum: %w", err)
	}
	digest, err := assetChecksum(checksums, name, true)
	if err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(staging, "download-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := downloadAsset(ctx, client, base+name, release.archive.Size, digest, temp); err != nil {
		return nil, fmt.Errorf("download core archive: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.install(ctx, temp, release.archive.Size, digest, version, revision)
}

// LatestOfficial returns the v-prefixed stable tag of the official Orca release.
func (s Store) LatestOfficial(ctx context.Context) (string, error) {
	client, err := newReleaseClient(s.Proxy)
	if err != nil {
		return "", err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	release, err := latestGitHubRelease(ctx, client, "stablyai/orca")
	if err != nil {
		return "", fmt.Errorf("read official Orca release: %w", err)
	}
	version, err := parseVersion(release.Tag)
	if err != nil || release.Draft || release.Prerelease || version.pre != "" {
		return "", fmt.Errorf("official Orca release is not stable")
	}
	return release.Tag, nil
}

func coreTagIdentity(tag, target string) (version, revision string, ok bool) {
	if identity, mac := strings.CutPrefix(tag, "core-macos-v"); mac {
		if target != macARM64Target {
			return "", "", false
		}
		revision, upstream, hasRevision := strings.Cut(identity, "-")
		if !hasRevision {
			return "v" + identity, "", true
		}
		return "v" + upstream, revision, revision != ""
	}
	if identity, windows := strings.CutPrefix(tag, "core-windows-v"); windows {
		if target != windowsX64Target {
			return "", "", false
		}
		revision, upstream, hasRevision := strings.Cut(identity, "-")
		return "v" + upstream, revision, hasRevision && revision != ""
	}
	version, ok = strings.CutPrefix(tag, "core-")
	return version, "", ok
}

func coreArchiveName(version, revision, target string) string {
	if revision != "" {
		version = "v" + displayVersion(version, revision)
	}
	return "orca-core-" + version + "-" + target + ".zip"
}

var repositoryPart = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func validateRepository(repository string) error {
	owner, name, separated := strings.Cut(repository, "/")
	if !separated || !repositoryPart.MatchString(owner) || !repositoryPart.MatchString(name) ||
		owner == "." || owner == ".." || name == "." || name == ".." {
		return fmt.Errorf("release repository must be a literal GitHub OWNER/REPO")
	}
	return nil
}

func findAsset(assets []releaseAsset, name string) (match releaseAsset, count int) {
	for _, asset := range assets {
		if asset.Name == name {
			match = asset
			count++
		}
	}
	return match, count
}

func sizeWithin(asset releaseAsset, limit int64) bool {
	return asset.Size > 0 && asset.Size <= limit
}

func releaseBase(repository, tag string) string {
	return githubDownload + repository + "/releases/download/" + url.PathEscape(tag) + "/"
}

func latestGitHubRelease(ctx context.Context, client *http.Client, repository string) (githubRelease, error) {
	var release githubRelease
	err := getJSON(ctx, client, githubAPI+repository+"/releases/latest", &release)
	return release, err
}

func newReleaseClient(proxy string) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext,
		ForceAttemptHTTP2: true, DisableCompression: true,
		TLSHandshakeTimeout: 15 * time.Second, IdleConnTimeout: 90 * time.Second,
	}
	if proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("download proxy must be an http:// or https:// URL")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &http.Client{
		Transport: transport, Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many GitHub release redirects")
			}
			return checkReleaseURL(req.URL)
		},
	}, nil
}

func checkReleaseURL(dest *url.URL) error {
	if dest.Scheme == "https" && dest.User == nil && (dest.Port() == "" || dest.Port() == "443") {
		switch dest.Hostname() {
		case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
			return nil
		}
	}
	return fmt.Errorf("release URL is outside the GitHub HTTPS host allowlist")
}

func getRelease(ctx context.Context, client *http.Client, endpoint string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := checkReleaseURL(request.URL); err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "orca-wails")
	if request.URL.Hostname() == "api.github.com" {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("GitHub release returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, destination any) error {
	data, err := getBytes(ctx, client, endpoint, maxMetadataBytes)
	if err != nil {
		return err
	}
	return decodeJSON(data, destination)
}

func getBytes(ctx context.Context, client *http.Client, endpoint string, limit int64) ([]byte, error) {
	response, err := getRelease(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > limit {
		return nil, fmt.Errorf("GitHub release metadata exceeds size limit")
	}
	return readBounded(response.Body, limit)
}

func assetChecksum(data []byte, filename string, single bool) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("checksum asset is not UTF-8")
	}
	var digest string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !validSHA256(fields[0]) {
			return "", fmt.Errorf("invalid checksum entry")
		}
		if strings.TrimPrefix(fields[1], "*") != filename {
			if single {
				return "", fmt.Errorf("checksum filename differs from release asset")
			}
			continue
		}
		if digest != "" {
			return "", fmt.Errorf("duplicate checksums for %s", filename)
		}
		digest = strings.ToLower(fields[0])
	}
	if digest == "" {
		return "", fmt.Errorf("missing checksum for %s", filename)
	}
	return digest, nil
}

func downloadAsset(ctx context.Context, client *http.Client, endpoint string, size int64, digest string, file *os.File) error {
	response, err := getRelease(ctx, client, endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength >= 0 && response.ContentLength != size {
		return fmt.Errorf("archive content length differs from release metadata")
	}
	if err := copyVerified(file, response.Body, size, digest); err != nil {
		return err
	}
	return file.Sync()
}
