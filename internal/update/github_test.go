package update

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func fakeGitHub(t *testing.T, responses map[string][]byte) *http.Client {
	t.Helper()
	return &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		data, ok := responses[request.URL.String()]
		if !ok {
			t.Errorf("unexpected GitHub request: %s", request.URL)
			return nil, fmt.Errorf("unexpected request")
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(data)),
			ContentLength: int64(len(data)),
		}, nil
	})}
}

func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCoreReleaseLookupAndPreparation(t *testing.T) {
	s := Store{Root: t.TempDir(), Target: macTarget, Repository: "test/orca-tauri"}
	zipPath, digest := fixture(t, "v1.4.266", "0.0.4", macTarget)
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	const tag = "core-macos-v0.0.4-1.4.266"
	const name = "orca-core-v0.0.4-1.4.266-aarch64-apple-darwin.zip"
	sidecar := []byte(fmt.Sprintf("%s  *%s\r\n", strings.ToUpper(digest), name))
	release := func(tag, filename string, prerelease bool) map[string]any {
		return map[string]any{"tag_name": tag, "draft": false, "prerelease": prerelease, "assets": []map[string]any{
			{"name": filename, "size": len(archive), "browser_download_url": "https://example.org/ignored"},
			{"name": filename + ".sha256", "size": len(sidecar), "browser_download_url": "https://example.org/ignored"},
		}}
	}
	page1 := []map[string]any{
		release("v9.0.0", name, false),
		release("core-v9.0.0", "orca-core-v9.0.0-aarch64-apple-darwin.zip", true),
		release("core-windows-v9.0.0-9.0.0", "orca-core-v9.0.0-9.0.0-x86_64-pc-windows-msvc.zip", false),
		release("core-macos-v0.9.0-1.4.265", "orca-core-v0.9.0-1.4.265-aarch64-apple-darwin.zip", false),
	}
	for len(page1) < 100 {
		page1 = append(page1, release("v1.0.0", name, false))
	}
	base := "https://github.com/test/orca-tauri/releases/download/" + tag + "/"
	client := fakeGitHub(t, map[string][]byte{
		"https://api.github.com/repos/test/orca-tauri/releases?page=1&per_page=100": jsonBytes(t, page1),
		"https://api.github.com/repos/test/orca-tauri/releases?page=2&per_page=100": jsonBytes(t, []any{release(tag, name, false)}),
		base + name:             archive,
		base + name + ".sha256": sidecar,
	})

	latest, err := s.latestCore(context.Background(), client)
	if err != nil || latest == nil || latest.Tag != tag || latest.DisplayVersion() != "0.0.4-1.4.266" {
		t.Fatalf("latest core = %+v, %v", latest, err)
	}
	if !latest.NewerThan(Manifest{Version: "v1.4.266", PackageRevision: "0.0.3"}) ||
		latest.NewerThan(Manifest{Version: "v1.4.266", PackageRevision: "0.0.4"}) ||
		latest.NewerThan(Manifest{Version: "v1.4.267"}) {
		t.Fatal("incorrect package revision comparison")
	}
	installed, err := s.prepareRelease(context.Background(), client, latest)
	if err != nil || installed == nil || installed.SHA256 != digest {
		t.Fatalf("prepare release = %+v, %v", installed, err)
	}
	if current, err := s.Current(); err != nil || current != nil {
		t.Fatalf("remote preparation changed Current = %+v, %v", current, err)
	}
}
