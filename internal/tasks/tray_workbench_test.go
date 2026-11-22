package tasks

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTrayReceiptCheckReadsOnlyTheSubmittedItem(t *testing.T) {
	for _, test := range []struct {
		name, resolution string
		revision         int
		want             AnswerRecord
	}{
		{"same choices at a new revision", `{"state":"resolved","resolvedAt":500,"answers":[{"questionId":"q","optionIds":["b","a"]}]}`, 3, AnswerRecorded},
		{"a different reply", `{"state":"resolved","resolvedAt":500,"answers":[{"questionId":"q","optionIds":["c"]}]}`, 3, PromptAlreadySettled},
		{"a cancelled prompt", `{"state":"cancelled"}`, 3, PromptAlreadySettled},
		{"still pending", `{"state":"pending"}`, 2, AnswerUnconfirmed},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := func(method string, params any, output any) error {
				want := map[string]string{"sessionId": "session", "itemId": "prompt"}
				if method != "agentSession.item" || params.(map[string]string)["itemId"] != want["itemId"] ||
					params.(map[string]string)["sessionId"] != want["sessionId"] {
					t.Fatalf("recovery made an unrelated call: %s %#v", method, params)
				}
				payload, _ := json.Marshal(map[string]any{"item": map[string]any{
					"itemId": "prompt", "revision": test.revision,
					"body": map[string]any{"kind": "question", "resolution": json.RawMessage(test.resolution)},
				}})
				return json.Unmarshal(payload, output)
			}
			answer := Answer{QuestionID: "prompt", Revision: 2, Selections: []QuestionSelection{{QuestionID: "q", OptionIDs: []string{"a", "b"}}}}
			if got, err := checkAnswer(call, "session", answer, 450); err != nil || got != test.want {
				t.Fatalf("checkAnswer = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestTrayFailedTurnDoesNotUsePreviousResult(t *testing.T) {
	row := Row{Local: true, ConversationID: "conversation", Status: Done, Outcome: "failure"}
	detail := readTaskDetail(func(method string, params any, output any) error {
		if method != "agent.read" || params.(map[string]any)["id"] != "conversation" {
			t.Fatalf("read wrong task: %s %#v", method, params)
		}
		return json.Unmarshal([]byte(`{"orcaSessionId":"conversation","source":"chat","entries":[
			{"kind":"message","role":"assistant","text":"Previous successful result"},
			{"kind":"message","role":"user","text":"Do another task"},
			{"kind":"error","text":"Provider is unavailable"}]}`), output)
	}, row)
	if detail.Result != "Provider is unavailable" || detail.ResultLabel != "失败原因" || detail.Unavailable {
		t.Fatalf("failed turn = %+v", detail)
	}
}

func TestTrayTerminalReadRequiresExactPane(t *testing.T) {
	leaf := "leaf"
	row := Row{Local: true, TerminalAddress: "term_session",
		Target: Target{WorktreeID: "ws", TabID: "tab", LeafID: &leaf}}
	detail := readTaskDetail(func(method string, _ any, output any) error {
		if method != "terminal.list" {
			t.Fatalf("terminal preview used %s", method)
		}
		return json.Unmarshal([]byte(`{"terminals":[
			{"handle":"term_session","worktreeId":"ws","tabId":"other","leafId":"leaf","preview":"Wrong tab"},
			{"handle":"term_session","worktreeId":"ws","tabId":"tab","leafId":"leaf","preview":"Line 1\nLine 2"}]}`), output)
	}, row)
	if detail.Output != "Line 1\nLine 2" || detail.CanFollowUp {
		t.Fatalf("terminal detail = %+v", detail)
	}
}

func TestTrayFollowUpQueuesWithoutTakingOverActiveTurn(t *testing.T) {
	for _, pending := range []bool{false, true} {
		writes := 0
		receipt, err := submitFollowUp(func(method string, params any, output any) error {
			switch method {
			case "agentSession.history":
				prompts := "[]"
				if pending {
					prompts = `["prompt"]`
				}
				return json.Unmarshal([]byte(`{"ok":true,"page":{"sessionId":"session","fence":8,"actionablePromptIds":`+
					prompts+`}}`), output)
			case "agentSession.send":
				writes++
				fields := params.(map[string]any)
				if fields["delivery"] != "queue-if-active" {
					t.Fatalf("send must queue while busy: %#v", fields)
				}
				envelope := fields["envelope"].(map[string]any)
				id := envelope["clientOperationId"].(string)
				return json.Unmarshal([]byte(`{"ok":true,"value":{"clientMessageId":"`+id+
					`","queued":{"state":"waiting","messageId":"m","position":1}}}`), output)
			default:
				t.Fatalf("unexpected method %s", method)
				return nil
			}
		}, "session", "follow up")
		if pending && (!errors.Is(err, ErrFollowUpBlocked) || writes != 0) {
			t.Fatalf("follow-up bypassed a prompt: %v, %d writes", err, writes)
		}
		if !pending && (err != nil || writes != 1 || !strings.Contains(receipt.Message, "已排队")) {
			t.Fatalf("queue was not confirmed: %+v, %v, %d writes", receipt, err, writes)
		}
	}
}

func TestTrayLaunchValidatesSelectionAndReturnsExactNativeTaskKey(t *testing.T) {
	writes := 0
	call := func(method string, params any, output any) error {
		var result string
		switch method {
		case "worktree.list":
			result = `{"worktrees":[{"id":"ws","displayName":"Work","hostId":"local"},
				{"id":"remote","displayName":"Remote","hostId":"ssh:remote"},{"id":"archived","hostId":"local","isArchived":true}]}`
		case "folderWorkspace.list":
			result = `{"folderWorkspaces":[{"id":"local-folder","name":"Folder"},{"id":"remote-folder","connectionId":"ssh"}]}`
		case "preflight.installedAgents":
			result = `{"agents":["codex","claude"],"settings":{"defaultTuiAgent":"codex","disabledTuiAgents":["claude"]}}`
		case "agent.launchReplay":
			writes++
			p := params.(map[string]any)
			if p["presentation"] != "background" || p["agent"] != "codex" ||
				p["target"].(map[string]string)["worktree"] != "id:ws" ||
				p["prompt"].(map[string]string)["text"] != "run a check" {
				t.Fatalf("wrong launch parameters: %#v", p)
			}
			result = `{"worktreeId":"ws","outcome":{"kind":"structured","sessionId":"session"},
				"prompt":{"outcome":"journaled"}}`
		default:
			t.Fatalf("unexpected RPC method %s", method)
		}
		return json.Unmarshal([]byte(result), output)
	}
	if catalog, err := readLaunchCatalog(call, "runtime"); err != nil || len(catalog.Workspaces) != 2 || len(catalog.Agents) != 1 {
		t.Fatalf("launch catalog = %+v, %v", catalog, err)
	}
	for _, input := range []LaunchInput{
		{WorkspaceID: "remote", Agent: "codex", Text: "run a check"},
		{WorkspaceID: "ws", Agent: "claude", Text: "run a check"},
	} {
		if _, err := launchTask(call, "runtime", input); !errors.Is(err, ErrLaunchSelection) || writes != 0 {
			t.Fatalf("invalid destination launched: %v, %d writes", err, writes)
		}
	}
	launch, err := launchTask(call, "runtime", LaunchInput{WorkspaceID: "ws", Agent: "codex", Text: "run a check"})
	key, _ := rowID("ws", nil, structuredTabPrefix+"session")
	if err != nil || launch.Key != key || launch.Prompt != "journaled" || writes != 1 {
		t.Fatalf("launch = %+v, %v (%d writes)", launch, err, writes)
	}
}

func TestTrayFollowUpRecoveryChecksOnlyOriginalMessage(t *testing.T) {
	const op = "1700000000000-0123456789abcdef0123456789abcdef"
	for _, recorded := range []bool{false, true} {
		got, err := checkFollowUp(func(method string, params any, output any) error {
			p := params.(map[string]string)
			if method != "agentSession.sendRecorded" || p["sessionId"] != "session" || p["clientMessageId"] != op {
				t.Fatalf("recovery did not read the original message: %s %#v", method, params)
			}
			payload, _ := json.Marshal(map[string]bool{"recorded": recorded})
			return json.Unmarshal(payload, output)
		}, "session", op)
		if err != nil || got != recorded {
			t.Fatalf("recorded = %t, err = %v, want %t", got, err, recorded)
		}
	}
}

func TestTrayLaunchRecoveryMatchesTheReservedIdentityInTheOriginalWorkspace(t *testing.T) {
	leaf := "leaf"
	lookup := LaunchLookup{WorkspaceID: "wanted", PaneKey: "tab:leaf", SessionID: "session_12345678"}
	summary := Summary{Rows: []Row{
		{ID: "wrong-workspace", Local: true, Target: Target{WorktreeID: "other", TabID: "tab", LeafID: &leaf}},
		{ID: "remote", Target: Target{WorktreeID: "wanted", TabID: "tab", LeafID: &leaf}},
		{ID: "right", Local: true, LaunchKey: "key", Target: Target{WorktreeID: "wanted", TabID: "tab", LeafID: &leaf}},
	}}
	got := recoveredLaunch(summary, lookup)
	if got == nil || got.Key != "key" || got.Kind != "terminal" || got.Prompt != "unconfirmed" {
		t.Fatalf("unexpected launch recovery = %+v", got)
	}
	if got := recoveredLaunch(Summary{Rows: summary.Rows[:2]}, lookup); got != nil {
		t.Fatalf("unrelated launch was matched: %+v", got)
	}
}

func TestTrayLaunchRecoveryCanConfirmAChatBeforeItsAgentStarts(t *testing.T) {
	lookup := LaunchLookup{WorkspaceID: "wanted", PaneKey: "tab:leaf", SessionID: "session_12345678"}
	got, err := readLaunchSurface(func(method string, params any, output any) error {
		if method != "session.tabs.list" || params.(map[string]string)["worktree"] != "id:wanted" {
			t.Fatalf("invalid launch lookup: %s %#v", method, params)
		}
		return json.Unmarshal([]byte(`{"worktree":"wanted","tabs":[
			{"type":"terminal","parentTabId":"tab","leafId":"another"},
			{"type":"agent-session","sessionId":"session_other"},
			{"type":"agent-session","sessionId":"session_12345678"}]}`), output)
	}, lookup)
	if err != nil || got == nil || got.Kind != "structured" || got.Prompt != "unconfirmed" || got.Key == "" {
		t.Fatalf("surface recovery = %+v, %v", got, err)
	}
}

func TestTrayDiffReadsOnlyAListedWorkspacePathAndArea(t *testing.T) {
	diffs := 0
	call := func(method string, params any, output any) error {
		switch method {
		case "git.status":
			return json.Unmarshal([]byte(`{"entries":[{"path":"src/new.go","status":"renamed",
				"area":"staged","oldPath":"src/old.go"}]}`), output)
		case "git.diff":
			diffs++
			p := params.(map[string]any)
			if p["worktree"] != "id:workspace" || p["filePath"] != "src/new.go" ||
				p["oldPath"] != "src/old.go" || p["staged"] != true {
				t.Fatalf("unexpected diff target: %#v", p)
			}
			return json.Unmarshal([]byte(`{"kind":"text","originalContent":"before","modifiedContent":"after"}`), output)
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	}
	if _, err := readTrayDiff(call, "workspace", "../outside", "staged"); err == nil || diffs != 0 {
		t.Fatalf("diff escaped the worktree scope: %v", err)
	}
	if _, err := readTrayDiff(call, "workspace", "src/new.go", "unstaged"); err == nil || diffs != 0 {
		t.Fatalf("diff read the wrong staging area: %v", err)
	}
	got, err := readTrayDiff(call, "workspace", "src/new.go", "staged")
	if err != nil || diffs != 1 || got.Before != "before" || got.After != "after" {
		t.Fatalf("diff = %+v, err = %v, calls = %d", got, err, diffs)
	}
}
