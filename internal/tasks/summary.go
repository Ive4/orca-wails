package tasks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

func parseResponse(raw []byte, runtimeID string) (Summary, error) {
	var response response
	if err := unmarshalWire(raw, &response); err != nil {
		return Summary{}, err
	}
	if !response.ok || response.id != requestID || response.meta.runtimeID != runtimeID {
		return Summary{}, errors.New("unexpected Orca task response")
	}
	listing := response.result
	if listing == nil {
		return Summary{}, errors.New("missing Orca task listing")
	}

	summary := Summary{
		Rows:    []Row{},
		Partial: listing.truncated || listing.totalCount > uint(len(listing.worktrees)),
	}
	if listing.hostScope != nil {
		for _, id := range listing.hostScope.omittedHostIDs {
			if !strings.HasPrefix(id, "runtime:") {
				summary.Partial = true
				break
			}
		}
	}

	for _, worktree := range listing.worktrees {
		workspace := firstLabel("工作区", worktree.displayName)
		for _, agent := range worktree.agents {
			id, err := rowID(worktree.worktreeID, worktree.hostID, agent.paneKey)
			if err != nil {
				return Summary{}, err
			}
			target := Target{WorktreeID: worktree.worktreeID, TabID: agent.paneKey}
			if i := strings.LastIndexByte(agent.paneKey, ':'); i >= 0 {
				leaf := agent.paneKey[i+1:]
				target.TabID = agent.paneKey[:i]
				target.LeafID = &leaf
			}
			summary.Rows = append(summary.Rows, Row{
				ID:        id,
				Title:     firstLabel("未命名任务", agent.taskTitle, agent.conversationName, agent.displayName),
				Workspace: workspace,
				Provider:  label(agent.agentType),
				Status:    rowStatus(agent),
				StartedAt: agent.stateStartedAt,
				UpdatedAt: agent.updatedAt,
				Target:    target,
			})
		}
	}

	// Within each group, prefer when the state began over later output.
	sort.SliceStable(summary.Rows, func(i, j int) bool {
		left, right := summary.Rows[i], summary.Rows[j]
		if a, b := statusGroup(left.Status), statusGroup(right.Status); a != b {
			return a < b
		}
		a, b := sortTime(left), sortTime(right)
		return a != nil && (b == nil || *a > *b)
	})
	return summary, nil
}

func rowStatus(agent agent) Status {
	if agent.restoredUnconfirmed {
		return Unknown
	}
	switch status := Status(agent.state); status {
	case Working:
		if agent.workingMode != nil && *agent.workingMode == "monitoring" {
			return Monitoring
		}
		return Working
	case Waiting, Blocked, Done:
		return status
	default:
		return Unknown
	}
}

func statusGroup(status Status) int {
	switch status {
	case Waiting, Blocked:
		return 0
	case Done:
		return 2
	default:
		return 1
	}
}

func sortTime(row Row) *uint64 {
	if row.StartedAt != nil {
		return row.StartedAt
	}
	return row.UpdatedAt
}

var serdeLineSeparators = strings.NewReplacer(`\\`, `\\`, `\u2028`, "\u2028", `\u2029`, "\u2029")

func rowID(worktreeID string, hostID *string, paneKey string) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([3]*string{&worktreeID, hostID, &paneKey}); err != nil {
		return "", err
	}
	// Serde emits the separator runes literally. Preserve escaped backslashes.
	key := serdeLineSeparators.Replace(strings.TrimSuffix(buf.String(), "\n"))
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]), nil
}

func firstLabel(fallback string, candidates ...*string) string {
	for _, candidate := range candidates {
		if text := label(candidate); text != nil {
			return *text
		}
	}
	return fallback
}

func label(candidate *string) *string {
	if candidate == nil {
		return nil
	}
	value := strings.TrimSpace(*candidate)
	if value == "" {
		return nil
	}

	// An offer can contain a URL encoded inside another URL's fragment.
	decoded := asciiLower(value)
	for range 4 {
		for _, needle := range [...]string{"pairing=", "orca-pair:", "orcapair:", "orca://", "bearertoken"} {
			if strings.Contains(decoded, needle) {
				return nil
			}
		}
		if !strings.Contains(decoded, "%") {
			break
		}
		next := decodeForm(decoded)
		if next == decoded || next == decoded+"=" {
			break
		}
		decoded = next
	}
	if strings.Contains(decoded, "%25") || strings.Contains(decoded, "orca://") || strings.Contains(decoded, "pairing=") {
		return nil
	}

	var title strings.Builder
	count := 0
	for _, ch := range value {
		if count == 120 {
			title.WriteRune('…')
			break
		}
		if unicode.IsControl(ch) {
			ch = ' '
		}
		title.WriteRune(ch)
		count++
	}
	text := title.String()
	return &text
}

func asciiLower(value string) string {
	return strings.Map(func(ch rune) rune {
		if ch >= 'A' && ch <= 'Z' {
			return ch + ('a' - 'A')
		}
		return ch
	}, value)
}

func decodeForm(value string) string {
	var decoded strings.Builder
	for pair := range strings.SplitSeq(value, "&") {
		if pair == "" {
			continue
		}
		if decoded.Len() > 0 {
			decoded.WriteByte('&')
		}
		key, value, _ := strings.Cut(pair, "=")
		writeFormComponent(&decoded, key)
		decoded.WriteByte('=')
		writeFormComponent(&decoded, value)
	}
	return asciiLower(strings.ToValidUTF8(decoded.String(), "\uFFFD"))
}

func writeFormComponent(decoded *strings.Builder, component string) {
	for i := 0; i < len(component); i++ {
		switch ch := component[i]; ch {
		case '%':
			if i+2 < len(component) {
				if b, err := strconv.ParseUint(component[i+1:i+3], 16, 8); err == nil {
					decoded.WriteByte(byte(b))
					i += 2
					continue
				}
			}
			decoded.WriteByte(ch)
		case '+':
			decoded.WriteByte(' ')
		default:
			decoded.WriteByte(ch)
		}
	}
}
