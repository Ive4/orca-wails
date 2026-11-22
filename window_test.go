package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orca-wails/internal/core"
)

func TestDesktopAssetsUseCurrentWindowDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wails-index.html"), []byte("desktop"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &desktop{}
	for _, check := range []struct {
		path       string
		withWindow bool
		status     int
	}{
		{"/desktop/index.html", false, http.StatusNotFound},
		{"/desktop/index.html", true, http.StatusOK},
		{"/desktop/rpc", true, http.StatusForbidden},
		{"/desktop/", true, http.StatusNotFound},
	} {
		if check.withWindow {
			app.window.Store(&orcaWindow{desktopDir: dir, desktopToken: "local-window-fixture"})
		}
		request := httptest.NewRequest(http.MethodGet, check.path, nil)
		reply := httptest.NewRecorder()
		app.serveAssets(reply, request)
		if reply.Code != check.status {
			t.Fatalf("%s = %d, want %d", check.path, reply.Code, check.status)
		}
	}
}

func TestOrcaWindowPageWithPairingDisabled(t *testing.T) {
	status := core.Status{Phase: "running", Port: 26768}
	local, err := orcaWindowPage(status, "", true, false)
	if err != nil || local.String() != "/desktop/index.html" {
		t.Fatalf("desktop page = %v, %v", local, err)
	}
	paired, err := orcaWindowPage(status, "", true, true)
	if err != nil || paired.String() != "http://localhost:26768/web-index.html" {
		t.Fatalf("paired page = %v, %v", paired, err)
	}
}

func TestOrcaWindowPageKeepsLegacyCoreAndRejectsStoppedCore(t *testing.T) {
	const offer = "http://127.0.0.1:26768/web-index.html#pairing=fixture"
	status := core.Status{Phase: "running", Port: 26768}
	local, err := orcaWindowPage(status, offer, false, false)
	if err != nil || local.String() != offer {
		t.Fatalf("legacy page = %v, %v", local, err)
	}
	status.Phase = "failed"
	if _, err := orcaWindowPage(status, offer, true, false); err == nil {
		t.Fatal("desktop opened against a stopped Core")
	}
}

func TestValidatedOfferKeepsPairingOnOwnedWebPage(t *testing.T) {
	const secret = "not-for-logs"
	valid := "http://127.0.0.1:26768/web-index.html#pairing=" + secret
	if _, err := validatedOffer(valid, 26768); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		strings.Replace(valid, "127.0.0.1", "[::1]", 1),
		strings.Replace(valid, "26768", "26769", 1),
		strings.Replace(valid, "web-index.html", "other.html", 1),
		strings.Replace(valid, "#", "?override=1#", 1),
	} {
		if _, err := validatedOffer(candidate, 26768); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("invalid offer accepted or pairing credential exposed")
		}
	}
}

func TestShellSettingsRequiresTheLocalCapableWindow(t *testing.T) {
	for _, test := range []struct {
		name, token, body string
		enabled           bool
		want              int
	}{
		{"unauthenticated", "", `{"method":"native.shellState"}`, true, http.StatusForbidden},
		{"old core", "window-token", `{"method":"native.shellState"}`, false, http.StatusForbidden},
		{"no subscription", "window-token", `{"action":"subscribe","method":"native.shellAction"}`, true, http.StatusForbidden},
		{"unknown action", "window-token", `{"method":"native.shellAction","params":{"kind":"not_a_setting"}}`, true, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &desktop{}
			window := &orcaWindow{desktopToken: "window-token", shellSettings: test.enabled}
			request := httptest.NewRequest(http.MethodPost, desktopRPCPath, strings.NewReader(test.body))
			request.Header.Set("X-Orca-Desktop", test.token)
			reply := httptest.NewRecorder()
			app.serveDesktopRPC(reply, request, window)
			if reply.Code != test.want {
				t.Fatalf("status = %d, want %d", reply.Code, test.want)
			}
		})
	}
}
