package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const listingFixture = `{
  "id":"tray-tasks","ok":true,"_meta":{"runtimeId":"rt_test"},
  "result":{"truncated":false,"totalCount":1,"hostScope":{"omittedHostIds":["runtime:other"]},
    "worktrees":[{"worktreeId":"ws-id","displayName":"Build","agents":[
      {"paneKey":"tab:leaf","state":"working","taskTitle":"Build task","agentType":"codex","stateStartedAt":100,"updatedAt":2000},
      {"paneKey":"tab2:leaf","state":"waiting","conversationName":"orca%253A%252F%252Fpair%253Fcode%253Dsecret","displayName":"Review","prompt":"never shown"},
      {"state":"blocked","stateStartedAt":1},
      {"state":"working","workingMode":"monitoring","updatedAt":101},
      {"state":"working","restoredUnconfirmed":true,"stateStartedAt":100},
      {"state":"future-state","updatedAt":0},
      {"state":"done","stateStartedAt":500}
    ]}]}
}`

func TestSummaryProjection(t *testing.T) {
	summary, err := parseResponse([]byte(listingFixture), "rt_test")
	if err != nil {
		t.Fatal(err)
	}
	want := []Status{Blocked, Waiting, Monitoring, Working, Unknown, Unknown, Done}
	if summary.Partial || len(summary.Rows) != len(want) {
		t.Fatalf("expected a complete listing of %d rows, got %+v", len(want), summary)
	}
	for i, status := range want {
		if summary.Rows[i].Status != status {
			t.Errorf("row %d status = %q, want %q", i, summary.Rows[i].Status, status)
		}
	}
	if summary.Rows[1].Title != "Review" {
		t.Errorf("unsafe label did not fall back: %q", summary.Rows[1].Title)
	}
	row := summary.Rows[3]
	if row.Target.WorktreeID != "ws-id" || row.Target.TabID != "tab" || row.Target.LeafID == nil || *row.Target.LeafID != "leaf" {
		t.Errorf("wrong reveal target: %+v", row.Target)
	}
	payload, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := rowID("ws-id", nil, "")
	wantJSON := `{"id":"523566371f123ecf53b1471e46a8331981788c5934c7843ca79a8183853a983b","title":"Build task","workspace":"Build","provider":"codex","status":"working","startedAt":100,"updatedAt":2000,"launchKey":"523566371f123ecf53b1471e46a8331981788c5934c7843ca79a8183853a983b","workspaceKey":"` + key + `"}`
	if string(payload) != wantJSON {
		t.Fatalf("row wire JSON = %s, want %s", payload, wantJSON)
	}
}

func TestCompletedDurationProjection(t *testing.T) {
	fixture := strings.NewReplacer(
		`"state":"done","stateStartedAt":500`, `"state":"done","stateStartedAt":500,"completedAt":750,"completedDurationSeconds":0`,
		`"state":"working","taskTitle"`, `"state":"working","completedAt":750,"completedDurationSeconds":522,"taskTitle"`,
	).Replace(listingFixture)
	summary, err := parseResponse([]byte(fixture), "rt_test")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range summary.Rows {
		payload, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(payload), `"completedDurationSeconds":0`); got != (row.Status == Done) {
			t.Errorf("duration projection for %s: %s", row.Status, payload)
		}
		if strings.Contains(string(payload), `"completedDurationSeconds":522`) {
			t.Errorf("unexpected duration on a running task: %s", payload)
		}
		if got := strings.Contains(string(payload), `"startedAt":750`); got != (row.Status == Done) {
			t.Errorf("completion clock projection for %s: %s", row.Status, payload)
		}
	}
}

func TestActiveToolInTraySnapshot(t *testing.T) {
	fixture := strings.NewReplacer(
		`"taskTitle":"Build task"`, `"taskTitle":"Build task","toolName":" git status "`,
		`"workingMode":"monitoring"`, `"workingMode":"monitoring","toolName":"orca://pair?code=secret"`,
		`"state":"done"`, `"state":"done","toolName":"old tool"`,
	).Replace(listingFixture)
	summary, err := parseResponse([]byte(fixture), "rt_test")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range summary.Rows {
		payload, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == Working {
			if !strings.Contains(string(payload), `"currentTool":"git status"`) {
				t.Errorf("missing current tool: %s", payload)
			}
		} else if strings.Contains(string(payload), `"currentTool"`) {
			t.Errorf("unexpected current tool on %s: %s", row.Status, payload)
		}
	}
}

func TestOutputMetricsInTraySnapshot(t *testing.T) {
	const valid = `{"turnOutputTokens":1200,"visibleTokensPerSecond":18,"visibleUntil":4102444800000}`
	owned := `"paneKey":"tab:` + structuredLeafID("session") + `","orcaSessionId":"session","structuredHostOwned":true`
	for _, tc := range []struct {
		name, metrics, want string
		agent, state, host  string
	}{
		{name: "known", metrics: valid, want: valid},
		{name: "settled request rate", metrics: `{"turnOutputTokens":1200,"visibleTokensPerSecond":18,"visibleUntil":4102444800000,"requestRate":true}`, want: `{"turnOutputTokens":1200,"visibleTokensPerSecond":18,"visibleUntil":4102444800000,"requestRate":true}`},
		{name: "running tool", metrics: `{"turnOutputTokens":1200,"toolActive":true}`, want: `{"turnOutputTokens":1200,"toolActive":true}`},
		{name: "settled tool", metrics: `{"turnOutputTokens":1200,"toolActive":false}`, want: `{"turnOutputTokens":1200}`},
		{name: "invalid tool activity", metrics: `{"turnOutputTokens":1200,"toolActive":"true"}`, want: `{"turnOutputTokens":1200}`},
		{name: "old Core"},
		{name: "malformed object", metrics: `"unavailable"`},
		{name: "malformed token count", metrics: `{"turnOutputTokens":"1200","visibleTokensPerSecond":18,"visibleUntil":4102444800000}`, want: `{"visibleTokensPerSecond":18,"visibleUntil":4102444800000}`},
		{name: "malformed rate", metrics: `{"turnOutputTokens":9,"visibleTokensPerSecond":1.5,"visibleUntil":4102444800000}`, want: `{"turnOutputTokens":9}`},
		{name: "legacy character rate", metrics: `{"turnOutputTokens":9,"visibleCharsPerSecond":18,"visibleUntil":4102444800000}`, want: `{"turnOutputTokens":9}`},
		{name: "missing expiration", metrics: `{"visibleTokensPerSecond":18}`},
		{name: "invalid expiration", metrics: `{"visibleTokensPerSecond":18,"visibleUntil":-1}`},
		{name: "expired", metrics: `{"turnOutputTokens":9,"visibleTokensPerSecond":18,"visibleUntil":1,"requestRate":true}`, want: `{"turnOutputTokens":9}`},
		{name: "nonpositive", metrics: `{"turnOutputTokens":0,"visibleTokensPerSecond":0,"visibleUntil":4102444800000}`},
		{name: "terminal", agent: `"paneKey":"tab:leaf"`, metrics: valid},
		{name: "unmatched leaf", agent: strings.Replace(owned, structuredLeafID("session"), "leaf", 1), metrics: valid},
		{name: "unowned", agent: strings.Replace(owned, `"structuredHostOwned":true`, `"structuredHostOwned":false`, 1), metrics: valid},
		{name: "remote", host: "remote:other", metrics: valid},
		{name: "monitoring", agent: owned + `,"workingMode":"monitoring"`, metrics: valid},
		{name: "unconfirmed", agent: owned + `,"restoredUnconfirmed":true`, metrics: valid},
		{name: "waiting", state: "waiting", metrics: valid},
		{name: "blocked", state: "blocked", metrics: valid},
		{name: "done", state: "done", metrics: valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, state := tc.agent, tc.state
			if agent == "" {
				agent = owned
			}
			if state == "" {
				state = "working"
			}
			fields := agent + `,"state":"` + state + `"`
			fixture := listingFixture
			if tc.host != "" {
				fixture = strings.Replace(fixture, `"worktreeId":"ws-id"`, `"worktreeId":"ws-id","hostId":"`+tc.host+`"`, 1)
			}
			if tc.metrics != "" {
				fields += `,"outputMetrics":` + tc.metrics
			}
			fixture = strings.Replace(fixture, `"paneKey":"tab:leaf","state":"working"`, fields, 1)
			summary, err := parseResponse([]byte(fixture), "rt_test")
			if err != nil || len(summary.Rows) != 7 {
				t.Fatalf("expected all task rows, got %+v, %v", summary, err)
			}
			for _, row := range summary.Rows {
				payload, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				want := tc.want
				if row.Title != "Build task" {
					want = ""
				}
				var projected struct{ OutputMetrics json.RawMessage }
				if err := json.Unmarshal(payload, &projected); err != nil {
					t.Fatal(err)
				}
				if got := string(projected.OutputMetrics); got != want {
					t.Errorf("%s outputMetrics = %q, want %q", row.Title, got, want)
				}
			}
		})
	}
}

func TestResponseValidation(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{`"ok":true`, `"ok":false`},
		{`"id":"tray-tasks"`, `"id":"other"`},
		{`"runtimeId":"rt_test"`, `"runtimeId":"other"`},
		{`"totalCount":1`, `"totalCount":null`},
		{`"totalCount":1`, `"TotalCount":1`},
		{`"totalCount":1`, `"totalCount":1,"totalCount":1`},
		{`"state":"done"`, `"state":null`},
		{`"paneKey":"tab:leaf"`, `"paneKey":null`},
		{`["runtime:other"]`, `[null]`},
	} {
		if _, err := parseResponse([]byte(strings.Replace(listingFixture, tc.from, tc.to, 1)), "rt_test"); err == nil {
			t.Errorf("accepted %s", tc.to)
		}
	}
}

func TestPartialListing(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{`"truncated":false`, `"truncated":true`},
		{`"totalCount":1`, `"totalCount":2`},
		{`"runtime:other"`, `"remote:other"`},
	} {
		summary, err := parseResponse([]byte(strings.Replace(listingFixture, tc.from, tc.to, 1)), "rt_test")
		if err != nil || !summary.Partial {
			t.Errorf("expected partial summary for %s, got %+v, %v", tc.to, summary, err)
		}
	}
}

func TestLabelsCheckDecodedSecretsBeforeClipping(t *testing.T) {
	longTitle := strings.Repeat("界", 119) + "\x7f" + "trailing text"
	if got := label(&longTitle); got == nil || *got != strings.Repeat("界", 119)+" …" {
		t.Fatalf("wrong clipped label: %v", got)
	}
	secret := strings.Repeat("界", 121) + "%z&check=ORCA%3A%2F%2Fpair"
	if label(&secret) != nil {
		t.Fatal("accepted an escaped credential URL after the display limit")
	}
}

func TestEmptyListingJSON(t *testing.T) {
	summary, err := parseResponse([]byte(`{"id":"tray-tasks","ok":true,"_meta":{"runtimeId":"x"},"result":{"truncated":false,"totalCount":0,"worktrees":[]}}`), "x")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(summary)
	if err != nil || string(payload) != `{"rows":[],"partial":false}` {
		t.Fatalf("empty summary JSON = %s, %v", payload, err)
	}
}

func TestMetadataRejections(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"different process", `{"pid":42,"runtimeId":"rt_test","authToken":"token","transports":[]}`, "another process"},
		{"over limit", strings.Repeat(" ", 16*1024+1), "metadata too large"},
		{"missing token", `{"pid":43,"runtimeId":"rt_test","transports":[]}`, "authToken"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMetadata(t, dir, tc.content)
			if _, err := Read(t.Context(), dir, 43); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Read error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Read(ctx, t.TempDir(), 43); err != context.Canceled {
		t.Fatalf("Read error = %v, want canceled", err)
	}
}

func writeMetadata(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "orca-runtime.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
