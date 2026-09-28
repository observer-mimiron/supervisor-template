package conversation

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// MemoryKind identifies the lifetime of a memory record.
type MemoryKind string

const MemoryWorking MemoryKind = "working"

// MemorySensitivity controls whether a record may be exposed to a worker.
type MemorySensitivity string

const (
	MemoryNormal    MemorySensitivity = "normal"
	MemorySensitive MemorySensitivity = "sensitive"
)

// MemoryScope is the trusted isolation boundary for conversation memory.
type MemoryScope struct {
	TenantID       string     `json:"tenant_id"`
	SubjectID      string     `json:"subject_id"`
	ConversationID string     `json:"conversation_id"`
	Kind           MemoryKind `json:"kind"`
}

// Valid requires all trusted scope components and the v1 working kind.
func (s MemoryScope) Valid() bool {
	return strings.TrimSpace(s.TenantID) != "" && strings.TrimSpace(s.SubjectID) != "" &&
		strings.TrimSpace(s.ConversationID) != "" && s.Kind == MemoryWorking
}

// Key returns a stable storage key for the complete scope.
func (s MemoryScope) Key() string {
	return s.TenantID + "\x00" + s.SubjectID + "\x00" + s.ConversationID + "\x00" + string(s.Kind)
}

// MemoryEntry is a bounded, non-authoritative record supplied to a worker.
type MemoryEntry struct {
	MemoryID    string            `json:"memory_id"`
	Scope       MemoryScope       `json:"scope"`
	Content     string            `json:"content"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Version     int64             `json:"version"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at,omitempty"`
	Sensitivity MemorySensitivity `json:"sensitivity"`
	SourceRunID string            `json:"source_run_id,omitempty"`
}

// Valid checks the immutable fields that every adapter must enforce.
func (e MemoryEntry) Valid() bool {
	return e.Scope.Valid() && e.MemoryID != "" && e.Version > 0 &&
		strings.TrimSpace(e.Content) != "" && utf8.ValidString(e.Content) &&
		(e.Sensitivity == "" || e.Sensitivity == MemoryNormal || e.Sensitivity == MemorySensitive)
}

// Expired reports whether the record is no longer eligible for recall.
func (e MemoryEntry) Expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt)
}

// MemoryQuery limits a read to one complete scope and bounded output.
type MemoryQuery struct {
	Scope    MemoryScope
	Query    string
	Limit    int
	MaxBytes int
	Now      time.Time
}

// Validate enforces caller-provided bounds before an adapter scans records.
func (q MemoryQuery) Validate() error {
	if !q.Scope.Valid() {
		return errors.New("记忆作用域不完整")
	}
	if q.Limit <= 0 || q.MaxBytes <= 0 {
		return errors.New("记忆召回上限必须为正数")
	}
	if !utf8.ValidString(q.Query) {
		return errors.New("记忆查询不是有效 UTF-8")
	}
	return nil
}
