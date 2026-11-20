package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanelAPIRequiresLivePanelToken(t *testing.T) {
	d := new(desktop)
	panel := &trayPanel{token: "current-panel-token"}
	d.panel.Store(panel)

	for _, tc := range []struct {
		path, token, body string
		status            int
	}{
		{"/wails/runtime", panel.token, "", http.StatusNotFound},
		{"/tray/api?page=tasks", "", "", http.StatusForbidden},
		{"/tray/api?page=tasks", "prior-panel-token", "", http.StatusForbidden},
		{"/tray/api?page=tasks", panel.token, "", http.StatusOK},
		{"/tray/api", panel.token, `{"kind":"unknown","unexpected":true}`, http.StatusBadRequest},
		{"/tray/api", panel.token, `{"kind":"unknown"} {}`, http.StatusBadRequest},
	} {
		method := http.MethodGet
		if tc.body != "" {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Orca-Panel", tc.token)
		res := httptest.NewRecorder()
		d.serveAssets(res, req)
		if res.Code != tc.status {
			t.Errorf("%s %s returned %d, want %d", method, tc.path, res.Code, tc.status)
		}
	}

	d.panel.Store(nil)
	req := httptest.NewRequest(http.MethodGet, "/tray/api?page=tasks", nil)
	req.Header.Set("X-Orca-Panel", panel.token)
	res := httptest.NewRecorder()
	d.serveAssets(res, req)
	if res.Code != http.StatusForbidden {
		t.Errorf("closed panel token returned %d", res.Code)
	}
}
