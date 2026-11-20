package tasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

type runtimeMetadata struct {
	pid        uint32
	runtimeID  string
	authToken  string
	transports []transport
}

func (m *runtimeMetadata) UnmarshalJSON(raw []byte) error {
	return decodeObject(raw,
		required("pid", &m.pid),
		required("runtimeId", &m.runtimeID),
		required("authToken", &m.authToken),
		required("transports", &m.transports),
	)
}

type transport struct {
	kind     string
	endpoint string
}

func (t *transport) UnmarshalJSON(raw []byte) error {
	return decodeObject(raw, required("kind", &t.kind), required("endpoint", &t.endpoint))
}

type response struct {
	id     string
	ok     bool
	meta   responseMeta
	result *listing
}

func (r *response) UnmarshalJSON(raw []byte) error {
	return decodeObject(raw,
		required("id", &r.id),
		required("ok", &r.ok),
		required("_meta", &r.meta),
		optional("result", &r.result),
	)
}

type responseMeta struct {
	runtimeID string
}

func (m *responseMeta) UnmarshalJSON(raw []byte) error {
	return decodeObject(raw, required("runtimeId", &m.runtimeID))
}

type listing struct {
	worktrees  []worktree
	totalCount uint
	truncated  bool
	hostScope  *hostScope
}

func (l *listing) UnmarshalJSON(raw []byte) error {
	return decodeObject(raw,
		required("worktrees", &l.worktrees),
		required("totalCount", &l.totalCount),
		required("truncated", &l.truncated),
		optional("hostScope", &l.hostScope),
	)
}

type hostScope struct {
	omittedHostIDs []string
}

func (h *hostScope) UnmarshalJSON(raw []byte) error {
	var ids []*string
	if err := decodeObject(raw, required("omittedHostIds", &ids)); err != nil {
		return err
	}
	for _, id := range ids {
		if id == nil {
			return errors.New("null omitted host ID")
		}
		h.omittedHostIDs = append(h.omittedHostIDs, *id)
	}
	return nil
}

type worktree struct {
	worktreeID  string
	displayName *string
	hostID      *string
	agents      []agent
}

func (w *worktree) UnmarshalJSON(raw []byte) error {
	return decodeObject(raw,
		defaulted("worktreeId", &w.worktreeID),
		optional("displayName", &w.displayName),
		optional("hostId", &w.hostID),
		required("agents", &w.agents),
	)
}

type agent struct {
	paneKey             string
	state               string
	taskTitle           *string
	conversationName    *string
	displayName         *string
	agentType           *string
	stateStartedAt      *uint64
	updatedAt           *uint64
	workingMode         *string
	restoredUnconfirmed bool
}

func (a *agent) UnmarshalJSON(raw []byte) error {
	return decodeObject(raw,
		defaulted("paneKey", &a.paneKey),
		required("state", &a.state),
		optional("taskTitle", &a.taskTitle),
		optional("conversationName", &a.conversationName),
		optional("displayName", &a.displayName),
		optional("agentType", &a.agentType),
		optional("stateStartedAt", &a.stateStartedAt),
		optional("updatedAt", &a.updatedAt),
		optional("workingMode", &a.workingMode),
		defaulted("restoredUnconfirmed", &a.restoredUnconfirmed),
	)
}

type wireField struct {
	name     string
	dest     any
	required bool
	nullable bool
}

func required(name string, dest any) wireField {
	return wireField{name: name, dest: dest, required: true}
}

func optional(name string, dest any) wireField {
	return wireField{name: name, dest: dest, nullable: true}
}

func defaulted(name string, dest any) wireField {
	return wireField{name: name, dest: dest}
}

func unmarshalWire(raw []byte, dest any) error {
	if !utf8.Valid(raw) {
		return errors.New("expected UTF-8 JSON")
	}
	return json.Unmarshal(raw, dest)
}

// Match field names exactly, reject duplicate known fields, and permit extensions.
func decodeObject(raw []byte, fields ...wireField) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("expected JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		for i := range fields {
			field := &fields[i]
			if field.name != token {
				continue
			}
			if field.dest == nil {
				return fmt.Errorf("duplicate field %q", field.name)
			}
			if !field.nullable && bytes.Equal(value, []byte("null")) {
				return fmt.Errorf("null field %q", field.name)
			}
			if err := json.Unmarshal(value, field.dest); err != nil {
				return fmt.Errorf("field %q: %w", field.name, err)
			}
			field.dest = nil
			break
		}
	}
	for _, field := range fields {
		if field.required && field.dest != nil {
			return fmt.Errorf("missing field %q", field.name)
		}
	}
	return nil
}
