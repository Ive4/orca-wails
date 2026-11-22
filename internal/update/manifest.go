package update

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Manifest describes a core package. File paths are relative to the ZIP root.
type Manifest struct {
	SchemaVersion    uint32            `json:"schema_version"`
	Version          string            `json:"version"`
	PackageRevision  string            `json:"package_revision,omitempty"`
	Target           string            `json:"target"`
	Executable       string            `json:"executable"`
	WorkingDirectory string            `json:"working_directory"`
	HealthPath       string            `json:"health_path"`
	UIPath           string            `json:"ui_path"`
	DesktopProtocol  uint32            `json:"desktop_protocol,omitempty"`
	ShellSettings    bool              `json:"shell_settings,omitempty"`
	Args             []string          `json:"args"`
	Environment      map[string]string `json:"environment"`
}

func (m Manifest) RuntimeVersion() string { return m.Environment["ORCA_VERSION"] }

func (m Manifest) DisplayVersion() string { return displayVersion(m.Version, m.PackageRevision) }

// OlderThan compares upstream versions first, then their packaging revisions.
func (m Manifest) OlderThan(other Manifest) bool {
	left, err := parseVersionKey(m.Version, m.PackageRevision)
	if err != nil {
		return false
	}
	right, err := parseVersionKey(other.Version, other.PackageRevision)
	return err == nil && left.compare(right) < 0
}

func displayVersion(version, revision string) string {
	if revision == "" {
		revision = "0.0.0"
	}
	return revision + "-" + strings.TrimLeft(version, "v")
}

func decodeManifest(data []byte) (Manifest, error) {
	manifest := Manifest{
		WorkingDirectory: ".",
		HealthPath:       "/web-index.html",
		UIPath:           "/web-index.html",
		Args:             []string{},
		Environment:      map[string]string{},
	}
	var fields struct {
		*Manifest
		Revision *string            `json:"package_revision"`
		Args     []*string          `json:"args"`
		Env      map[string]*string `json:"environment"`
	}
	fields.Manifest = &manifest
	var raw map[string]json.RawMessage
	if err := decodeJSON(data, &raw); err != nil {
		return Manifest{}, err
	}
	for _, name := range []string{"working_directory", "health_path", "ui_path", "args", "environment"} {
		if string(raw[name]) == "null" {
			return Manifest{}, fmt.Errorf("manifest %s cannot be null", name)
		}
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return Manifest{}, err
	}
	if fields.Revision != nil {
		manifest.PackageRevision = *fields.Revision
		if manifest.PackageRevision == "" {
			return Manifest{}, fmt.Errorf("empty core packaging revision")
		}
	}
	if manifest.SchemaVersion != 1 {
		return Manifest{}, fmt.Errorf("unsupported core manifest schema")
	}
	if _, err := parseVersionKey(manifest.Version, manifest.PackageRevision); err != nil {
		return Manifest{}, err
	}
	if err := validateTarget(manifest.Target); err != nil {
		return Manifest{}, err
	}
	for _, path := range []string{manifest.HealthPath, manifest.UIPath} {
		if len(path) > 2048 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
			strings.ContainsAny(path, "\\\r\n\x00#") {
			return Manifest{}, fmt.Errorf("manifest health/UI path must be a local absolute URL path")
		}
	}
	if len(fields.Args) > 256 || len(fields.Env) > 128 {
		return Manifest{}, fmt.Errorf("manifest argument or environment count exceeds limit")
	}
	for _, arg := range fields.Args {
		if arg == nil || len(*arg) > 8192 || strings.ContainsRune(*arg, 0) {
			return Manifest{}, fmt.Errorf("invalid core argument")
		}
		manifest.Args = append(manifest.Args, *arg)
	}
	for name, value := range fields.Env {
		if name == "" || len(name) > 256 || strings.ContainsAny(name, "=\x00") ||
			value == nil || len(*value) > 32768 || strings.ContainsRune(*value, 0) {
			return Manifest{}, fmt.Errorf("invalid core environment entry")
		}
		manifest.Environment[name] = *value
	}
	if err := validateRelativePath(manifest.Executable); err != nil {
		return Manifest{}, fmt.Errorf("invalid core executable: %w", err)
	}
	if manifest.WorkingDirectory != "." {
		if err := validateRelativePath(manifest.WorkingDirectory); err != nil {
			return Manifest{}, fmt.Errorf("invalid core working_directory: %w", err)
		}
	}
	return manifest, nil
}

func decodeJSON(data []byte, destination any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("metadata is not UTF-8")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("missing JSON metadata")
	}
	return json.Unmarshal(data, destination)
}

type semVersion struct {
	numbers [3]uint64
	pre     string
	build   string
}

var semVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*))?(?:\+([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*))?$`)

func parseSemVersion(value string) (semVersion, bool) {
	match := semVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return semVersion{}, false
	}
	version := semVersion{pre: match[4], build: match[5]}
	for i := range version.numbers {
		number, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return semVersion{}, false
		}
		version.numbers[i] = number
	}
	for id := range strings.SplitSeq(version.pre, ".") {
		if len(id) > 1 && id[0] == '0' && digits(id) {
			return semVersion{}, false
		}
	}
	return version, true
}

func parseVersion(value string) (semVersion, error) {
	bare, prefix := strings.CutPrefix(value, "v")
	version, ok := parseSemVersion(bare)
	if !prefix || len(value) > 80 || !ok {
		return semVersion{}, fmt.Errorf("invalid core version %q (expected v-prefixed semantic version)", value)
	}
	return version, nil
}

type versionKey struct {
	version  semVersion
	revision [3]uint64
}

func parseVersionKey(version, revision string) (versionKey, error) {
	upstream, err := parseVersion(version)
	if err != nil {
		return versionKey{}, err
	}
	key := versionKey{version: upstream}
	if revision != "" {
		rev, ok := parseSemVersion(revision)
		if len(revision) > 64 || !ok || rev.pre != "" || rev.build != "" || rev.numbers == [3]uint64{} {
			return versionKey{}, fmt.Errorf("core packaging revision must be a positive stable version")
		}
		key.revision = rev.numbers
	}
	return key, nil
}

func (key versionKey) compare(other versionKey) int {
	for i, number := range key.version.numbers {
		if order := cmp.Compare(number, other.version.numbers[i]); order != 0 {
			return order
		}
	}
	if key.version.pre != other.version.pre {
		if key.version.pre == "" {
			return 1
		}
		if other.version.pre == "" {
			return -1
		}
		return compareIdentifiers(key.version.pre, other.version.pre)
	}
	if order := compareIdentifiers(key.version.build, other.version.build); order != 0 {
		return order
	}
	for i, number := range key.revision {
		if order := cmp.Compare(number, other.revision[i]); order != 0 {
			return order
		}
	}
	return 0
}

// Rust's semver ordering also compares build metadata, before packaging revisions.
func compareIdentifiers(left, right string) int {
	if left == right {
		return 0
	}
	lhs, rhs := strings.Split(left, "."), strings.Split(right, ".")
	for i := range min(len(lhs), len(rhs)) {
		a, b := lhs[i], rhs[i]
		if a == b {
			continue
		}
		aNumeric, bNumeric := digits(a), digits(b)
		switch {
		case aNumeric && bNumeric:
			x, y := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
			if order := cmp.Compare(len(x), len(y)); order != 0 {
				return order
			}
			if order := cmp.Compare(x, y); order != 0 {
				return order
			}
			return cmp.Compare(len(a), len(b))
		case aNumeric:
			return -1
		case bNumeric:
			return 1
		default:
			return cmp.Compare(a, b)
		}
	}
	return cmp.Compare(len(lhs), len(rhs))
}

func digits(value string) bool {
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

var targetPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func validateTarget(target string) error {
	if !targetPattern.MatchString(target) {
		return fmt.Errorf("invalid core target triple %q", target)
	}
	return nil
}
