// Package update prepares and activates versioned Orca core packages.
// The desktop owns serialization from preparation through candidate startup,
// health verification and Commit (or recovery).
// GitHub repositories and their publishers are trusted by checksum verification.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const activeName = "active.json"

// Store describes the on-disk core store and its GitHub release source.
type Store struct {
	Root       string
	Repository string
	Proxy      string
	Target     string
}

// Installed identifies a prepared or activated core.
type Installed struct {
	Dir      string
	SHA256   string
	Manifest Manifest
}

type reference struct {
	Directory string `json:"directory"`
	Version   string `json:"version"`
	SHA256    string `json:"archive_sha256"`
}

type activeState struct {
	Schema   int        `json:"schema_version"`
	Current  reference  `json:"current"`
	Previous *reference `json:"previous"`
}

type receipt struct {
	Schema  int    `json:"schema_version"`
	Version string `json:"version"`
	Target  string `json:"target"`
	SHA256  string `json:"archive_sha256"`
}

// Current returns nil when the store has never activated a core.
func (s Store) Current() (*Installed, error) {
	state, err := s.readState()
	if err != nil || state == nil {
		return nil, err
	}
	return s.loadReference(state.Current)
}

// Previous returns the core to which the caller can roll back, if any.
func (s Store) Previous() (*Installed, error) {
	state, err := s.readState()
	if err != nil || state == nil || state.Previous == nil {
		return nil, err
	}
	return s.loadReference(*state.Previous)
}

// PrepareArchive imports a local ZIP using a trusted publisher SHA-256.
// It leaves activation to Commit after the caller has checked core readiness.
func (s Store) PrepareArchive(path, digest string) (*Installed, error) {
	return s.PrepareArchiveContext(context.Background(), path, digest)
}

// PrepareArchiveContext cancels copying and extraction without activating the candidate.
func (s Store) PrepareArchiveContext(ctx context.Context, path, digest string) (*Installed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSHA256(digest) {
		return nil, fmt.Errorf("invalid SHA-256 digest")
	}
	digest = strings.ToLower(digest)
	input, size, err := openArchiveFile(path)
	if err != nil {
		return nil, fmt.Errorf("open offline core archive: %w", err)
	}
	defer input.Close()
	staging, err := s.initialize()
	if err != nil {
		return nil, err
	}
	// Hash and unpack the same private snapshot, even if the source is replaced.
	snapshot, err := os.CreateTemp(staging, "import-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(snapshot.Name())
	defer snapshot.Close()
	if err := copyVerified(snapshot, contextReader{ctx, input}, size, digest); err != nil {
		return nil, fmt.Errorf("verify offline core archive: %w", err)
	}
	return s.install(ctx, snapshot, size, digest, "", "")
}

func (s Store) install(ctx context.Context, file io.ReaderAt, size int64, digest, expectedVersion, expectedRevision string) (*Installed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan, err := readArchive(ctx, file, size)
	if err != nil {
		return nil, err
	}
	manifest := plan.manifest
	if manifest.Target != s.Target {
		return nil, fmt.Errorf("core manifest platform differs from shell")
	}
	if expectedVersion != "" && manifest.Version != expectedVersion {
		return nil, fmt.Errorf("core manifest version differs from release tag")
	}
	if expectedRevision != "" && manifest.PackageRevision != expectedRevision {
		return nil, fmt.Errorf("core packaging revision differs from release tag")
	}
	versions := filepath.Join(s.Root, "versions")
	dir := filepath.Join(versions, slotName(manifest.Version, s.Target, digest))
	reuse := func() (*Installed, error) {
		old, err := s.inspectInstalled(dir)
		if err != nil {
			return nil, err
		}
		if old.SHA256 != digest || !reflect.DeepEqual(old.Manifest, manifest) {
			return nil, fmt.Errorf("immutable core slot already exists with different content")
		}
		return old, nil
	}
	if _, err := os.Lstat(dir); err == nil {
		return reuse()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	temp, err := os.MkdirTemp(filepath.Join(s.Root, "staging"), "extract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	if err := plan.extract(ctx, temp); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(temp, receiptName), receipt{1, manifest.Version, s.Target, digest}); err != nil {
		return nil, fmt.Errorf("write core install receipt: %w", err)
	}
	if err := os.Rename(temp, dir); err != nil {
		// Another prepare can publish the same slot while extraction runs.
		if _, statErr := os.Lstat(dir); statErr == nil {
			return reuse()
		}
		return nil, fmt.Errorf("publish immutable core slot: %w", err)
	}
	if err := syncDirectory(versions); err != nil {
		return nil, fmt.Errorf("flush core slots directory: %w", err)
	}
	return &Installed{Dir: dir, SHA256: digest, Manifest: manifest}, nil
}

// Commit atomically activates a healthy candidate and remembers the prior core.
// Recommitting the active core preserves its rollback reference.
func (s Store) Commit(core *Installed) error {
	if core == nil || !validSHA256(core.SHA256) {
		return fmt.Errorf("invalid prepared core")
	}
	if _, err := s.initialize(); err != nil {
		return err
	}
	if _, err := parseVersion(core.Manifest.Version); err != nil {
		return err
	}
	ref := reference{
		Directory: slotName(core.Manifest.Version, s.Target, core.SHA256),
		Version:   core.Manifest.Version,
		SHA256:    core.SHA256,
	}
	expected := filepath.Join(s.Root, "versions", ref.Directory)
	if core.Dir != expected {
		return fmt.Errorf("cannot activate a core outside the expected versions slot")
	}
	installed, err := s.loadReference(ref)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(installed.Manifest, core.Manifest) {
		return fmt.Errorf("prepared core changed before activation")
	}
	old, err := s.readState()
	if err != nil {
		return err
	}
	state := activeState{Schema: 1, Current: ref}
	if old != nil {
		state.Previous = &old.Current
		if old.Current == ref {
			state.Previous = old.Previous
		}
	}
	if err := writeJSON(filepath.Join(s.Root, activeName), state); err != nil {
		return fmt.Errorf("commit healthy core: %w", err)
	}
	return nil
}

func (s Store) validate() error {
	if s.Root == "" {
		return fmt.Errorf("core store root is required")
	}
	return validateTarget(s.Target)
}

func (s Store) initialize() (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	root := filepath.Clean(s.Root)
	staging := filepath.Join(root, "staging")
	for _, path := range []string{root, filepath.Join(root, "versions"), staging} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", err
		}
		if err := requireDirectory(path); err != nil {
			return "", err
		}
		if err := privateDirectory(path); err != nil {
			return "", err
		}
	}
	return staging, nil
}

func (s Store) readState() (*activeState, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := requireDirectory(filepath.Clean(s.Root)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	data, err := readMetadata(filepath.Join(s.Root, activeName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read active core pointer: %w", err)
	}
	var state activeState
	if err := decodeJSON(data, &state); err != nil {
		return nil, fmt.Errorf("decode active core pointer: %w", err)
	}
	if state.Schema != 1 || state.Current.Directory == "" || state.Current.Version == "" || state.Current.SHA256 == "" {
		return nil, fmt.Errorf("invalid active core pointer schema or current reference")
	}
	return &state, nil
}

func (s Store) loadReference(ref reference) (*Installed, error) {
	if _, err := parseVersion(ref.Version); err != nil {
		return nil, err
	}
	if !validSHA256(ref.SHA256) || ref.Directory != slotName(ref.Version, s.Target, ref.SHA256) {
		return nil, fmt.Errorf("invalid content-addressed slot in core pointer")
	}
	versions := filepath.Join(s.Root, "versions")
	if err := requireDirectory(versions); err != nil {
		return nil, err
	}
	installed, err := s.inspectInstalled(filepath.Join(versions, ref.Directory))
	if err != nil {
		return nil, err
	}
	if installed.Manifest.Version != ref.Version || installed.SHA256 != ref.SHA256 {
		return nil, fmt.Errorf("active core pointer differs from manifest or receipt")
	}
	return installed, nil
}

func (s Store) inspectInstalled(dir string) (*Installed, error) {
	if err := requireDirectory(dir); err != nil {
		return nil, err
	}
	data, err := readMetadata(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestName, err)
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return nil, err
	}
	if manifest.Target != s.Target {
		return nil, fmt.Errorf("installed core platform differs from shell")
	}
	if err := checkPackagePath(dir, manifest.Executable, false); err != nil {
		return nil, err
	}
	if manifest.WorkingDirectory != "." {
		if err := checkPackagePath(dir, manifest.WorkingDirectory, true); err != nil {
			return nil, err
		}
	}
	data, err = readMetadata(filepath.Join(dir, receiptName))
	if err != nil {
		return nil, fmt.Errorf("read install receipt: %w", err)
	}
	var r receipt
	if err := decodeJSON(data, &r); err != nil {
		return nil, fmt.Errorf("decode install receipt: %w", err)
	}
	if r.Schema != 1 || r.Version != manifest.Version || r.Target != manifest.Target || !validSHA256(r.SHA256) {
		return nil, fmt.Errorf("installed core receipt does not match manifest")
	}
	return &Installed{Dir: dir, SHA256: r.SHA256, Manifest: manifest}, nil
}

func checkPackagePath(dir, relative string, wantDir bool) error {
	parts := strings.Split(relative, "/")
	for i, part := range parts {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		isDir := wantDir || i < len(parts)-1
		if isLinkOrReparse(info) || (isDir && !info.IsDir()) || (!isDir && !info.Mode().IsRegular()) {
			return fmt.Errorf("invalid installed package path %q", dir)
		}
	}
	return nil
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || isLinkOrReparse(info) {
		return fmt.Errorf("expected regular core directory at %q", path)
	}
	return nil
}

func slotName(version, target, digest string) string {
	return version + "-" + target + "-" + digest[:16]
}

func validSHA256(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	var buffer [32]byte
	_, err := hex.Decode(buffer[:], []byte(digest))
	return err == nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func copyVerified(output io.Writer, input io.Reader, size int64, digest string) error {
	hash := sha256.New()
	length, err := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, size+1))
	if err != nil {
		return err
	}
	if length != size {
		return fmt.Errorf("archive length differs from expected size")
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("archive SHA-256 mismatch")
	}
	return nil
}

func readMetadata(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || isLinkOrReparse(info) || info.Size() > maxManifestBytes {
		return nil, fmt.Errorf("expected bounded regular metadata file at %q", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, maxManifestBytes)
}

func readBounded(input io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("metadata exceeds size limit")
	}
	return data, nil
}

func writeJSON(path string, value any) error {
	parent := filepath.Dir(path)
	file, err := os.CreateTemp(parent, ".metadata-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	// The rename committed the transaction. Report success even if the
	// filesystem cannot flush the directory entry.
	_ = syncDirectory(parent)
	return nil
}
