package tasks

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReadQuickQuestionUsesLiveActionableItems(t *testing.T) {
	const history = `{"ok":true,"page":{"sessionId":"session","fence":4,"actionablePromptIds":["prompt"]}}`
	const item = `{"item":{"itemId":"prompt","revision":2,"body":{"kind":"question","question":"确认灰度范围","questions":[{"id":"q","question":"确认灰度范围","multiSelect":false,"options":[{"id":"internal","label":"仅内部测试"},{"id":"5%","label":"5% 灰度"}],"freeTextQuestionId":"q"}],"resolution":{"state":"pending"}}}}`
	for _, tc := range []struct {
		name, history, item string
		want                bool
	}{
		{"supported", history, item, true},
		{"stale prompt", history, strings.Replace(item, "pending", "resolved", 1), false},
		{"multiple prompts", strings.Replace(history, `["prompt"]`, `["prompt","later"]`, 1), item, true},
		{"multi-select", history, strings.Replace(item, `"multiSelect":false`, `"multiSelect":true`, 1), true},
		{"option preview", history, strings.Replace(item, `"id":"internal"`, `"id":"internal","preview":"mockup"`, 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := readQuestion(func(method string, _ any, output any) error {
				switch method {
				case "agentSession.history":
					return json.Unmarshal([]byte(tc.history), output)
				case "agentSession.item":
					return json.Unmarshal([]byte(tc.item), output)
				default:
					return errors.New("unexpected method")
				}
			}, "session", "")
			if tc.want {
				if err != nil || q == nil || q.Revision != 2 || len(q.Parts) != 1 ||
					!q.Parts[0].AcceptsText || len(q.Parts[0].Options) != 2 {
					t.Fatalf("question = %+v, error = %v", q, err)
				}
			} else if q != nil || !errors.Is(err, ErrNoQuickQuestion) {
				t.Fatalf("unsupported question = %+v, error = %v", q, err)
			}
		})
	}
}

func TestQuickAnswerNamesQuestionAndMatchesCoreFingerprint(t *testing.T) {
	question := &Question{ID: "prompt", Revision: 2, Parts: []QuestionPart{{
		ID: "q", AcceptsText: true, Options: []QuestionOption{{ID: "5%", Label: "5% 灰度"}},
	}}}
	other := "先按区域灰度 2% <试验>"
	fields, err := questionAnswer(question, []QuestionSelection{{QuestionID: "q", Other: &other}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := questionFingerprint("session", fields)
	if err != nil {
		t.Fatal(err)
	}
	const want = "8bd9252cef1dac73beeadfd8795820daf263e6ea57b07e76b3110fc1b4c87bb2"
	if got != want {
		t.Fatalf("fingerprint = %s, want %s", got, want)
	}
	if _, err := questionAnswer(question, []QuestionSelection{{QuestionID: "q", OptionIDs: []string{"unknown"}}}); err == nil {
		t.Fatal("accepted an unoffered option")
	}
	if _, err := questionAnswer(question, []QuestionSelection{{QuestionID: "q", OptionIDs: []string{"5%"}, Other: &other}}); err == nil {
		t.Fatal("accepted an option and free text in a single-choice question")
	}
}

func TestBotPickerKeepsLocalRosterAndDisabledState(t *testing.T) {
	bots, err := listBots(func(method string, params any, output any) error {
		body, _ := json.Marshal(params)
		if method != "bots.roster" || string(body) != `{"withoutChats":true}` {
			t.Fatal("did not read the local Bot roster")
		}
		return json.Unmarshal([]byte(`{"bots":[{"id":"release","name":"发布助手","paused":false},
			{"id":"review","name":"代码助手","paused":true},
			{"id":"remote","name":"远端","server":{"name":"Other","on":"other"}}]}`), output)
	})
	if err != nil || len(bots) != 2 || bots[0].ID != "release" || !bots[1].Paused {
		t.Fatalf("bots = %+v, error = %v", bots, err)
	}
}

func TestStructuredStatusPaneAllowsQuickReplyOnlyOnLocalHost(t *testing.T) {
	const raw = `{"id":"tray-tasks","ok":true,"_meta":{"runtimeId":"core"},"result":{"totalCount":4,"truncated":false,"worktrees":[
		{"worktreeId":"local","hostId":"local","agents":[{"paneKey":"structured-agent-session-session:3f3af1ec-ebbd-4410-ab41-7ec0d27bbfcb","state":"blocked"}]},
		{"worktreeId":"local-waiting","hostId":"runtime:core","agents":[{"paneKey":"structured-agent-session-session:3f3af1ec-ebbd-4410-ab41-7ec0d27bbfcb","state":"waiting"}]},
		{"worktreeId":"remote","hostId":"ssh:other","agents":[{"paneKey":"structured-agent-session-session:3f3af1ec-ebbd-4410-ab41-7ec0d27bbfcb","state":"waiting"}]},
		{"worktreeId":"fake","hostId":"local","agents":[{"paneKey":"structured-agent-session-session:unrelated-leaf","state":"waiting"}]}
	]}}`
	summary, err := parseResponse([]byte(raw), "core")
	if err != nil || len(summary.Rows) != 4 {
		t.Fatalf("parse: %v, rows: %d", err, len(summary.Rows))
	}
	if !summary.Rows[0].QuickReply || summary.Rows[0].Target.StructuredSessionID() != "session" ||
		!summary.Rows[1].QuickReply || summary.Rows[2].QuickReply || summary.Rows[3].QuickReply {
		t.Fatalf("unexpected quick reply eligibility: %+v", summary.Rows)
	}
}

func TestAnswerQuestionReportsStaleAndUncertainSeparately(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revision uint64
		result   string
		want     error
		writes   int
	}{
		{"acknowledged", 2, `{"ok":true,"value":{"itemId":"prompt","revision":3,"resolution":{"state":"resolved"}}}`, nil, 1},
		{"old revision", 1, "", ErrQuestionChanged, 0},
		{"uncertain", 2, `{"ok":false,"refusal":{"code":"agent_session_operation_unknown"}}`, ErrAnswerUncertain, 1},
		{"provider refused", 2, `{"ok":false,"refusal":{"code":"agent_session_ownership_unknown"}}`, ErrAnswerRefused, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			err := answerQuestion(func(method string, params any, output any) error {
				var wire string
				switch method {
				case "agentSession.history":
					wire = `{"ok":true,"page":{"sessionId":"session","fence":4,"actionablePromptIds":["prompt"]}}`
				case "agentSession.item":
					wire = `{"item":{"itemId":"prompt","revision":2,"body":{"kind":"question","question":"确认范围","options":[{"id":"5%","label":"5%"}],"resolution":{"state":"pending"}}}}`
				case "agentSession.respondToQuestion":
					writes++
					envelope := params.(map[string]any)["envelope"].(map[string]any)
					if envelope["sessionId"] != "session" || envelope["expectedRuntimeFence"] != uint64(4) {
						t.Fatalf("wrong mutation envelope: %#v", envelope)
					}
					wire = tc.result
				default:
					t.Fatalf("unexpected method: %s", method)
				}
				return json.Unmarshal([]byte(wire), output)
			}, "session", Answer{QuestionID: "prompt", Revision: tc.revision,
				Selections: []QuestionSelection{{QuestionID: "q1", OptionIDs: []string{"5%"}}}})
			if !errors.Is(err, tc.want) || writes != tc.writes {
				t.Fatalf("err = %v, writes = %d", err, writes)
			}
		})
	}
}

func TestGroupedQuestionAnswerTargetsTheDisplayedLivePrompt(t *testing.T) {
	const group = `{"item":{"itemId":"group","revision":4,"body":{
		"kind":"question","question":"2 grouped questions from Claude","options":[],
		"questions":[
			{"id":"targets","question":"Which targets?","multiSelect":true,"options":[
				{"id":"web","label":"Web","preview":"A web preview"},{"id":"mobile","label":"Mobile"}]},
			{"id":"host","question":"Where should it run?","multiSelect":false,"options":[],"freeTextQuestionId":"host"}
		],"resolution":{"state":"pending"}}}}`
	const history = `{"ok":true,"page":{"sessionId":"session","fence":8,"actionablePromptIds":["approval","group","another"]}}`
	writes := 0
	call := func(method string, params any, output any) error {
		var result string
		switch method {
		case "agentSession.history":
			result = history
		case "agentSession.item":
			switch params.(map[string]string)["itemId"] {
			case "approval":
				result = `{"item":{"itemId":"approval","revision":1,"body":{"kind":"approval","resolution":{"state":"pending"}}}}`
			case "group":
				result = group
			default:
				t.Fatalf("read the wrong prompt: %v", params)
			}
		case "agentSession.respondToQuestion":
			writes++
			fields := params.(map[string]any)
			got, _ := coreJSON(fields["answers"])
			const want = `[{"optionIds":["web","mobile"],"questionId":"targets"},{"optionIds":[],"other":"localhost","questionId":"host"}]`
			if fields["itemId"] != "group" || fields["expectedRevision"] != uint64(4) || got != want {
				t.Fatalf("incorrect response to group: %#v", fields)
			}
			result = `{"ok":true,"value":{"itemId":"group","revision":5,"resolution":{"state":"resolved"}}}`
		default:
			t.Fatalf("unexpected call %s", method)
		}
		return json.Unmarshal([]byte(result), output)
	}

	q, err := readQuestion(call, "session", "")
	if err != nil || q.ID != "group" || len(q.Parts) != 2 || !q.Parts[0].MultiSelect ||
		q.Parts[0].Options[0].Preview != "A web preview" || !q.Parts[1].AcceptsText {
		t.Fatalf("incorrect grouped question: %+v, %v", q, err)
	}
	text := "localhost"
	choices := []QuestionSelection{
		{QuestionID: "host", Other: &text},
		{QuestionID: "targets", OptionIDs: []string{"web", "mobile"}},
	}
	if err := answerQuestion(call, "session", Answer{QuestionID: "group", Revision: 4, Selections: choices[:1]}); err == nil || writes != 0 {
		t.Fatalf("incomplete group was sent: %v, %d writes", err, writes)
	}
	if err := answerQuestion(call, "session", Answer{QuestionID: "gone", Revision: 4, Selections: choices}); !errors.Is(err, ErrQuestionChanged) || writes != 0 {
		t.Fatalf("stale group was sent: %v, %d writes", err, writes)
	}
	if err := answerQuestion(call, "session", Answer{QuestionID: "group", Revision: 4, Selections: choices}); err != nil || writes != 1 {
		t.Fatalf("group did not receive acknowledgement: %v, %d writes", err, writes)
	}
}

func TestTrayApprovalRevalidatesExactScope(t *testing.T) {
	writes := 0
	clipped := false
	call := func(method string, params any, output any) error {
		var result string
		switch method {
		case "agentSession.history":
			result = `{"ok":true,"page":{"sessionId":"session","fence":8,"actionablePromptIds":["approval"]}}`
		case "agentSession.item":
			result = `{"item":{"itemId":"approval","revision":3,"body":{"kind":"approval","title":"审核操作",
				"toolName":"Read","detail":"Read notes.md","options":[{"id":"allow","label":"Allow","intent":"allow_once"},
				{"id":"always","label":"Always","intent":"allow_always"},{"id":"deny","label":"Deny","intent":"reject_once"}],
				"resolution":{"state":"pending"}}}}`
			if clipped {
				result = strings.Replace(result, "Read notes.md", "Read notes.md…", 1)
			}
		case "agentSession.respondToApproval":
			writes++
			if params.(map[string]any)["optionId"] != "allow" {
				t.Fatalf("unexpected approval: %#v", params)
			}
			result = `{"ok":true,"value":{"itemId":"approval","revision":4,"resolution":{"state":"resolved"}}}`
		default:
			t.Fatalf("unexpected method %s", method)
		}
		return json.Unmarshal([]byte(result), output)
	}
	prompt, err := readPrompt(call, "session", "", true)
	if err != nil || prompt.Approval == nil || len(prompt.Approval.Options) != 2 || prompt.Approval.Detail != "Read notes.md" {
		t.Fatalf("approval = %+v, err = %v", prompt, err)
	}
	answer := Answer{QuestionID: "approval", Revision: 3, ApprovalOption: "always"}
	if err := answerQuestion(call, "session", answer); !errors.Is(err, ErrInvalidAnswer) || writes != 0 {
		t.Fatalf("persistent approval was sent: %v, %d calls", err, writes)
	}
	answer.ApprovalOption = "allow"
	clipped = true
	if err := answerQuestion(call, "session", answer); !errors.Is(err, ErrQuestionChanged) || writes != 0 {
		t.Fatalf("approval with incomplete scope was sent: %v, %d calls", err, writes)
	}
	clipped = false
	if err := answerQuestion(call, "session", answer); err != nil || writes != 1 {
		t.Fatalf("one-time approval was not recorded: %v, %d calls", err, writes)
	}
}
