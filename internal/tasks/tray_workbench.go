package tasks

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

var (
	ErrFollowUpBlocked = errors.New("会话已有待处理事项，请先处理")
	ErrLaunchUncertain = errors.New("启动结果待确认")
	ErrLaunchSelection = errors.New("工作区或 Agent 已不可用，请刷新后重新选择")
)

type TaskDetail struct {
	Task           Row           `json:"task"`
	Source         string        `json:"source,omitempty"`
	Entries        []DetailEntry `json:"entries"`
	Result         string        `json:"result,omitempty"`
	ResultLabel    string        `json:"resultLabel,omitempty"`
	Output         string        `json:"output,omitempty"`
	CurrentTool    *string       `json:"currentTool,omitempty"`
	Limited        bool          `json:"limited"`
	Unavailable    bool          `json:"unavailable"`
	CanFollowUp    bool          `json:"canFollowUp"`
	CanReadChanges bool          `json:"canReadChanges"`
	LaunchPreset   *LaunchInput  `json:"launchPreset,omitempty"`
	ReadAt         int64         `json:"readAt,omitempty"`
}

type DetailEntry struct {
	Kind  string `json:"kind"`
	Role  string `json:"role,omitempty"`
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`
	State string `json:"state,omitempty"`
	Tools []struct {
		Name  string `json:"name"`
		State string `json:"state,omitempty"`
	} `json:"tools,omitempty"`
}

func ReadTaskDetail(ctx context.Context, data string, pid uint32, row Row) TaskDetail {
	detail, err := withRuntime(ctx, data, pid, func(call rpcCall) (TaskDetail, error) {
		return readTaskDetail(call, row), nil
	})
	if err != nil {
		return taskDetailSummary(row)
	}
	return detail
}

func taskDetailSummary(row Row) TaskDetail {
	detail := TaskDetail{Task: row, Entries: []DetailEntry{}, Unavailable: true,
		CanFollowUp: row.CanFollowUp(), CanReadChanges: row.CanReadChanges()}
	if row.Local && row.Target.WorktreeID != "" && row.Provider != nil {
		detail.LaunchPreset = &LaunchInput{WorkspaceID: row.Target.WorktreeID, Agent: *row.Provider}
	}
	if row.LastResult != nil {
		detail.Result = panelText(*row.LastResult, 12000)
		detail.ResultLabel = "最近一条 Agent 回复"
	}
	if row.Status == Working || row.Status == Monitoring {
		detail.CurrentTool = row.CurrentTool
	}
	return detail
}

func (row Row) CanFollowUp() bool {
	sessionID := row.Target.StructuredSessionID()
	return row.Local && sessionID != "" && !strings.HasPrefix(sessionID, "bot-group-")
}

func readTaskDetail(call rpcCall, row Row) TaskDetail {
	detail := taskDetailSummary(row)
	if !row.Local {
		return detail
	}
	if row.TerminalAddress != "" {
		if output, ok := readTerminalPreview(call, row); ok {
			detail.Output = output
			detail.ReadAt = time.Now().UnixMilli()
		}
	}
	conversationID := row.ConversationID
	if conversationID == "" {
		conversationID = row.Target.StructuredSessionID()
	}
	if conversationID == "" {
		return detail
	}
	var history struct {
		SessionID string        `json:"orcaSessionId"`
		Source    string        `json:"source"`
		Entries   []DetailEntry `json:"entries"`
		Limited   bool          `json:"limited"`
	}
	if err := call("agent.read", map[string]any{"id": conversationID, "limit": 16}, &history); err != nil ||
		history.SessionID != conversationID || (history.Source != "chat" && history.Source != "transcript") {
		return detail
	}
	detail.Source, detail.Limited, detail.Unavailable = history.Source, history.Limited, false
	detail.ReadAt = time.Now().UnixMilli()
	var latestReply, latestError string
	for _, entry := range history.Entries {
		if entry.Kind != "message" && entry.Kind != "tool" && entry.Kind != "approval" &&
			entry.Kind != "question" && entry.Kind != "error" {
			continue
		}
		entry.Text = panelText(entry.Text, 12000)
		if entry.Kind == "message" {
			switch {
			case entry.Role == "user":
				latestReply, latestError = "", ""
			case entry.Role == "assistant" && entry.Text != "":
				latestReply = entry.Text
			}
		}
		if entry.Kind == "error" && entry.Text != "" {
			latestError = entry.Text
		}
		detail.Entries = append(detail.Entries, entry)
	}
	detail.Result, detail.ResultLabel = latestReply, "本轮最后一条回复"
	if row.Outcome == "failure" && latestError != "" {
		detail.Result, detail.ResultLabel = latestError, "失败原因"
	}
	return detail
}

func readTerminalPreview(call rpcCall, row Row) (string, bool) {
	var response struct {
		Terminals []struct {
			Handle     string `json:"handle"`
			WorktreeID string `json:"worktreeId"`
			TabID      string `json:"tabId"`
			LeafID     string `json:"leafId"`
			Preview    string `json:"preview"`
		} `json:"terminals"`
	}
	if err := call("terminal.list", map[string]any{
		"handles": []string{row.TerminalAddress}, "includeVisualLayouts": false,
	}, &response); err != nil {
		return "", false
	}
	for _, terminal := range response.Terminals {
		if terminal.Handle == row.TerminalAddress && terminal.WorktreeID == row.Target.WorktreeID &&
			terminal.TabID == row.Target.TabID && row.Target.LeafID != nil && terminal.LeafID == *row.Target.LeafID {
			return panelText(terminal.Preview, 12000), true
		}
	}
	return "", false
}

func panelText(value string, limit int) string {
	if label(&value) == nil {
		return ""
	}
	runes := []rune(strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) && ch != '\n' && ch != '\t' {
			return ' '
		}
		return ch
	}, value))
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return string(runes)
}

func SendFollowUp(ctx context.Context, data string, pid uint32, sessionID, text string) (string, error) {
	receipt, err := SubmitFollowUp(ctx, data, pid, sessionID, text)
	return receipt.Message, err
}

type FollowUpReceipt struct {
	Message     string
	OperationID string
}

func SubmitFollowUp(ctx context.Context, data string, pid uint32, sessionID, text string) (FollowUpReceipt, error) {
	return withRuntime(ctx, data, pid, func(call rpcCall) (FollowUpReceipt, error) {
		return submitFollowUp(call, sessionID, text)
	})
}

func CheckFollowUp(ctx context.Context, data string, pid uint32, sessionID, operationID string) (bool, error) {
	return withRuntime(ctx, data, pid, func(call rpcCall) (bool, error) {
		return checkFollowUp(call, sessionID, operationID)
	})
}

func checkFollowUp(call rpcCall, sessionID, operationID string) (bool, error) {
	if sessionID == "" || len(operationID) != 46 {
		return false, ErrInvalidAnswer
	}
	var response struct {
		Recorded bool `json:"recorded"`
	}
	err := call("agentSession.sendRecorded", map[string]string{
		"sessionId": sessionID, "clientMessageId": operationID,
	}, &response)
	return response.Recorded, err
}

func submitFollowUp(call rpcCall, sessionID, text string) (FollowUpReceipt, error) {
	if strings.HasPrefix(sessionID, "bot-group-") {
		return FollowUpReceipt{}, ErrAnswerRefused
	}
	if strings.TrimSpace(text) == "" {
		return FollowUpReceipt{}, ErrInvalidAnswer
	}
	if len(text) > maxAnswerBytes {
		return FollowUpReceipt{}, ErrAnswerTooLarge
	}
	history, err := readPromptHistory(call, sessionID)
	if err != nil {
		return FollowUpReceipt{}, ErrAnswerRefused
	}
	if len(history.Prompts) != 0 {
		return FollowUpReceipt{}, ErrFollowUpBlocked
	}
	op := newOperationID()
	fields := map[string]any{
		"body": map[string]any{
			"kind": "message", "role": "user", "blocks": []map[string]string{{"type": "text", "text": text}},
		},
		"delivery": "queue-if-active",
	}
	envelope, err := mutationEnvelope("agentSession.send", sessionID, history.Fence, op, fields)
	if err != nil {
		return FollowUpReceipt{}, err
	}
	fields["envelope"] = envelope
	var reply struct {
		OK      *bool `json:"ok"`
		Refusal struct {
			Code string `json:"code"`
		} `json:"refusal"`
		Value struct {
			MessageID  string `json:"clientMessageId"`
			Submission struct {
				State string `json:"dispatchState"`
			} `json:"submission"`
			Queued struct {
				State string `json:"state"`
			} `json:"queued"`
		} `json:"value"`
	}
	receipt := FollowUpReceipt{OperationID: op}
	if err := call("agentSession.send", fields, &reply); err != nil || reply.OK == nil ||
		(!*reply.OK && (reply.Refusal.Code == "" || reply.Refusal.Code == "agent_session_operation_unknown")) {
		return receipt, ErrAnswerUncertain
	}
	if !*reply.OK || reply.Value.Submission.State == "rejected" {
		return FollowUpReceipt{}, ErrAnswerRefused
	}
	if reply.Value.MessageID != op {
		return receipt, ErrAnswerUncertain
	}
	if reply.Value.Queued.State == "waiting" {
		receipt.Message = "后续消息已排队，当前轮结束后发送"
		return receipt, nil
	}
	switch reply.Value.Submission.State {
	case "accepted":
		receipt.Message = "后续消息已发送"
	case "pending":
		receipt.Message = "后续消息已记录，等待 Agent 接收"
	default:
		return receipt, ErrAnswerUncertain
	}
	return receipt, nil
}

type LaunchWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type LaunchCatalog struct {
	Workspaces   []LaunchWorkspace `json:"workspaces"`
	Agents       []string          `json:"agents"`
	DefaultAgent string            `json:"defaultAgent,omitempty"`
	Partial      bool              `json:"partial"`
}

func ReadLaunchCatalog(ctx context.Context, data string, pid uint32) (LaunchCatalog, error) {
	return withRuntimeHost(ctx, data, pid, 8*time.Second, readLaunchCatalog)
}

func readLaunchCatalog(call rpcCall, runtimeID string) (LaunchCatalog, error) {
	var git struct {
		Worktrees []struct {
			LaunchWorkspace
			DisplayName string `json:"displayName"`
			HostID      string `json:"hostId"`
			Archived    bool   `json:"isArchived"`
			Bare        bool   `json:"isBare"`
		} `json:"worktrees"`
		Truncated bool `json:"truncated"`
	}
	if err := call("worktree.list", map[string]int{"limit": 256}, &git); err != nil {
		return LaunchCatalog{}, err
	}
	catalog := LaunchCatalog{Workspaces: []LaunchWorkspace{}, Agents: []string{}, Partial: git.Truncated}
	for _, git := range git.Worktrees {
		if git.ID != "" && !git.Archived && !git.Bare && localHost(git.HostID, runtimeID) {
			git.Name = firstLabel("工作区", &git.DisplayName, &git.Path)
			catalog.Workspaces = append(catalog.Workspaces, git.LaunchWorkspace)
		}
	}
	var folders struct {
		Workspaces []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Path         string `json:"folderPath"`
			HostID       string `json:"executionHostId"`
			ConnectionID string `json:"connectionId"`
			Archived     bool   `json:"isArchived"`
		} `json:"folderWorkspaces"`
	}
	if err := call("folderWorkspace.list", nil, &folders); err != nil {
		catalog.Partial = true
	}
	for _, folder := range folders.Workspaces {
		if folder.ID != "" && !folder.Archived && folder.ConnectionID == "" && localHost(folder.HostID, runtimeID) {
			catalog.Workspaces = append(catalog.Workspaces, LaunchWorkspace{
				ID: "folder:" + folder.ID, Name: firstLabel("文件夹", &folder.Name, &folder.Path), Path: folder.Path,
			})
		}
	}
	var detection struct {
		Agents   []string `json:"agents"`
		Settings struct {
			Default  string   `json:"defaultTuiAgent"`
			Disabled []string `json:"disabledTuiAgents"`
		} `json:"settings"`
	}
	if err := call("preflight.installedAgents", map[string]string{}, &detection); err != nil {
		return LaunchCatalog{}, err
	}
	for _, agent := range detection.Agents {
		if agent != "" && !slices.Contains(detection.Settings.Disabled, agent) {
			catalog.Agents = append(catalog.Agents, agent)
		}
	}
	catalog.DefaultAgent = detection.Settings.Default
	return catalog, nil
}

type LaunchInput struct {
	WorkspaceID string `json:"workspaceID"`
	Agent       string `json:"agent"`
	Text        string `json:"text"`
}

type LaunchReceipt struct {
	Key        string        `json:"key,omitempty"`
	Kind       string        `json:"kind"`
	Prompt     string        `json:"prompt"`
	Warning    string        `json:"warning,omitempty"`
	ModeDetail string        `json:"modeDetail,omitempty"`
	TaskID     string        `json:"taskID,omitempty"`
	Lookup     *LaunchLookup `json:"lookup,omitempty"`
	OpenTarget *Target       `json:"-"`
}

type LaunchLookup struct {
	WorkspaceID string `json:"workspaceID"`
	PaneKey     string `json:"paneKey"`
	SessionID   string `json:"sessionID"`
}

func launchUUID() string {
	var id [16]byte
	rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}

func CheckLaunch(ctx context.Context, data string, pid uint32, lookup LaunchLookup) (*LaunchReceipt, error) {
	summary, err := Read(ctx, data, pid)
	if err == nil {
		if receipt := recoveredLaunch(summary, lookup); receipt != nil {
			return receipt, nil
		}
	}
	return withRuntime(ctx, data, pid, func(call rpcCall) (*LaunchReceipt, error) {
		return readLaunchSurface(call, lookup)
	})
}

func recoveredLaunch(summary Summary, lookup LaunchLookup) *LaunchReceipt {
	if lookup.WorkspaceID == "" || lookup.PaneKey == "" || lookup.SessionID == "" {
		return nil
	}
	for _, row := range summary.Rows {
		if !row.Local || row.Target.WorktreeID != lookup.WorkspaceID {
			continue
		}
		sessionID := row.Target.StructuredSessionID()
		if sessionID == lookup.SessionID {
			return &LaunchReceipt{Key: row.LaunchKey, Kind: "structured", Prompt: "unconfirmed", TaskID: row.ID, OpenTarget: &row.Target}
		}
		if sessionID == "" && row.Target.LeafID != nil &&
			row.Target.TabID+":"+*row.Target.LeafID == lookup.PaneKey {
			return &LaunchReceipt{Key: row.LaunchKey, Kind: "terminal", Prompt: "unconfirmed", TaskID: row.ID, OpenTarget: &row.Target}
		}
	}
	return nil
}

func readLaunchSurface(call rpcCall, lookup LaunchLookup) (*LaunchReceipt, error) {
	if lookup.WorkspaceID == "" || lookup.PaneKey == "" || lookup.SessionID == "" {
		return nil, ErrLaunchSelection
	}
	var result struct {
		WorkspaceID string `json:"worktree"`
		Tabs        []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			SessionID string `json:"sessionId"`
			TabID     string `json:"parentTabId"`
			LeafID    string `json:"leafId"`
		} `json:"tabs"`
	}
	if err := call("session.tabs.list", map[string]string{"worktree": "id:" + lookup.WorkspaceID}, &result); err != nil {
		return nil, err
	}
	if result.WorkspaceID != lookup.WorkspaceID {
		return nil, ErrLaunchSelection
	}
	for _, tab := range result.Tabs {
		receipt := LaunchReceipt{Prompt: "unconfirmed", Lookup: &lookup}
		switch {
		case tab.Type == "agent-session" && tab.SessionID == lookup.SessionID:
			receipt.Kind = "structured"
			receipt.Key, _ = rowID(lookup.WorkspaceID, nil, structuredTabPrefix+lookup.SessionID)
			tabID := tab.ID
			if tabID == "" {
				tabID = structuredTabPrefix + lookup.SessionID
			}
			receipt.OpenTarget = &Target{WorktreeID: lookup.WorkspaceID, TabID: tabID, ChatSessionID: lookup.SessionID}
		case tab.Type == "terminal" && tab.TabID+":"+tab.LeafID == lookup.PaneKey:
			receipt.Kind = "terminal"
			receipt.Key, _ = rowID(lookup.WorkspaceID, nil, lookup.PaneKey)
			receipt.OpenTarget = &Target{WorktreeID: lookup.WorkspaceID, TabID: tab.TabID, LeafID: &tab.LeafID}
		default:
			continue
		}
		return &receipt, nil
	}
	return nil, nil
}

func LaunchTask(ctx context.Context, data string, pid uint32, input LaunchInput) (LaunchReceipt, error) {
	return withRuntimeHost(ctx, data, pid, 75*time.Second, func(call rpcCall, runtimeID string) (LaunchReceipt, error) {
		return launchTask(call, runtimeID, input)
	})
}

func launchTask(call rpcCall, runtimeID string, input LaunchInput) (LaunchReceipt, error) {
	if strings.TrimSpace(input.Text) == "" {
		return LaunchReceipt{}, ErrInvalidAnswer
	}
	if len(input.Text) > maxAnswerBytes {
		return LaunchReceipt{}, ErrAnswerTooLarge
	}
	catalog, err := readLaunchCatalog(call, runtimeID)
	if err != nil || !slices.Contains(catalog.Agents, input.Agent) ||
		!slices.ContainsFunc(catalog.Workspaces, func(w LaunchWorkspace) bool { return w.ID == input.WorkspaceID }) {
		return LaunchReceipt{}, ErrLaunchSelection
	}
	var response struct {
		WorktreeID string `json:"worktreeId"`
		Warning    string `json:"warning"`
		Outcome    struct {
			Kind      string `json:"kind"`
			TabID     string `json:"tabId"`
			SessionID string `json:"sessionId"`
			PaneKey   string `json:"paneKey"`
		} `json:"outcome"`
		Prompt struct {
			Outcome string `json:"outcome"`
		} `json:"prompt"`
		Mode struct {
			Mode      string `json:"mode"`
			Preferred string `json:"preferred"`
			Detail    string `json:"detail"`
		} `json:"receipt"`
	}
	lookup := LaunchLookup{
		WorkspaceID: input.WorkspaceID,
		PaneKey:     launchUUID() + ":" + launchUUID(),
		SessionID:   "session_" + launchUUID(),
	}
	if err := call("agent.launchReplay", map[string]any{
		"agent": input.Agent, "operationId": newOperationID(), "presentation": "background",
		"sessionId": lookup.SessionID, "paneKey": lookup.PaneKey,
		"target": map[string]string{"kind": "existing", "worktree": "id:" + input.WorkspaceID},
		"prompt": map[string]string{"text": input.Text, "delivery": "submit"},
	}, &response); err != nil {
		return LaunchReceipt{Lookup: &lookup}, ErrLaunchUncertain
	}
	receipt := LaunchReceipt{Kind: response.Outcome.Kind, Prompt: response.Prompt.Outcome, Warning: panelText(response.Warning, 800)}
	if response.Mode.Mode != response.Mode.Preferred {
		receipt.ModeDetail = panelText(response.Mode.Detail, 800)
	}
	var identity string
	switch receipt.Kind {
	case "structured":
		identity = response.Outcome.TabID
		if response.Outcome.SessionID != "" {
			identity = structuredTabPrefix + response.Outcome.SessionID
		}
	case "terminal":
		identity = response.Outcome.PaneKey
	default:
		return LaunchReceipt{Lookup: &lookup}, ErrLaunchUncertain
	}
	if identity != "" && response.WorktreeID == input.WorkspaceID {
		receipt.Key, _ = rowID(response.WorktreeID, nil, identity)
		receipt.Lookup = &lookup
	}
	return receipt, nil
}
