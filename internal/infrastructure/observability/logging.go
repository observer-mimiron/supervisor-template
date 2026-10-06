package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type contextKey string

const (
	contextFieldsKey contextKey = "observability.fields"
	requestIDKey     contextKey = "observability.request_id"
)

// WithFields adds only low-cardinality diagnostic fields to a context.
func WithFields(ctx context.Context, fields map[string]string) context.Context {
	copyFields := make(map[string]string, len(fields))
	for key, value := range fields {
		copyFields[key] = safeValue(key, value)
	}
	return context.WithValue(ctx, contextFieldsKey, copyFields)
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, strings.TrimSpace(requestID))
}

func requestFields(ctx context.Context) []any {
	fields := make([]any, 0, 16)
	if values, ok := ctx.Value(contextFieldsKey).(map[string]string); ok {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := values[key]
			fields = append(fields, slog.String(key, safeValue(key, value)))
		}
	}
	if requestID, _ := ctx.Value(requestIDKey).(string); requestID != "" {
		fields = append(fields, slog.String("request_id", requestID))
	}
	return fields
}

// Log emits a structured record with bounded, redacted diagnostics.
func Log(ctx context.Context, logger *slog.Logger, level slog.Level, msg string, attrs ...any) {
	if logger == nil {
		return
	}
	all := append(requestFields(ctx), redactAttrs(attrs)...)
	logger.Log(ctx, level, stableMessage(msg), all...)
}

func redactAttrs(attrs []any) []any {
	redacted := append([]any(nil), attrs...)
	for i, value := range redacted {
		if attr, ok := value.(slog.Attr); ok && attr.Value.Kind() == slog.KindString {
			attr.Value = slog.StringValue(safeValue(attr.Key, attr.Value.String()))
			redacted[i] = attr
			continue
		}
		if key, ok := value.(string); ok && i+1 < len(redacted) {
			if text, ok := redacted[i+1].(string); ok {
				redacted[i+1] = safeValue(key, text)
			}
		}
	}
	return redacted
}

func stableMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 160 {
		return message[:160]
	}
	return message
}

func safeValue(key, value string) string {
	lowerKey := strings.ToLower(key)
	lowerValue := strings.ToLower(value)
	for _, marker := range []string{"authorization", "token", "secret", "password", "api_key", "prompt", "bearer ", "sk-", "/home/", "/workspace/", "/tmp/", "/app/", "/var/", "/etc/", "c:\\"} {
		if strings.Contains(lowerKey, marker) || strings.Contains(lowerValue, marker) {
			return "[REDACTED]"
		}
	}
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

func ErrorAttrs(err error) []any {
	if err == nil {
		return nil
	}
	message := stableMessage(err.Error())
	digest := sha256.Sum256([]byte(message))
	return []any{slog.String("error", safeValue("error", message)), slog.String("error_fingerprint", hex.EncodeToString(digest[:8]))}
}

// DiagnosticFields is the bounded, low-cardinality diagnostic projection shared
// by logs, spans, and metrics. User text and payloads do not belong here.
type DiagnosticFields struct {
	ErrorCode     string
	ErrorClass    string
	Phase         string
	RetryDecision string
	Fingerprint   string
	Attempt       int
	Duration      time.Duration
}

func (d DiagnosticFields) Attrs() []any {
	attrs := make([]any, 0, 7)
	if d.ErrorCode != "" {
		attrs = append(attrs, slog.String("error_code", safeValue("error_code", d.ErrorCode)))
	}
	if d.ErrorClass != "" {
		attrs = append(attrs, slog.String("error_class", safeValue("error_class", d.ErrorClass)))
	}
	if d.Phase != "" {
		attrs = append(attrs, slog.String("phase", safeValue("phase", d.Phase)))
	}
	if d.RetryDecision != "" {
		attrs = append(attrs, slog.String("retry_decision", safeValue("retry_decision", d.RetryDecision)))
	}
	if d.Fingerprint != "" {
		attrs = append(attrs, slog.String("fingerprint", safeValue("fingerprint", d.Fingerprint)))
	}
	if d.Attempt >= 0 {
		attrs = append(attrs, slog.Int("attempt", d.Attempt))
	}
	if d.Duration >= 0 {
		attrs = append(attrs, slog.Int64("duration_ms", d.Duration.Milliseconds()))
	}
	return attrs
}

// RotatingFileWriter is a bounded JSONL sink. Rotation is intentionally local and single-process.
// ponytail: single-process mutex; use a shared append service if multi-process aggregation is required.
type RotatingFileWriter struct {
	mu          sync.Mutex
	path        string
	maxBytes    int64
	maxFiles    int
	rotateDaily bool
	mode        os.FileMode
	file        *os.File
	day         string
	size        int64
}

// NewRotatingFileWriter 创建带轮转的观测文件写入器。
// mode 来自 observability.file_mode；零值使用 0600。
func NewRotatingFileWriter(path string, maxBytes int64, rotateDaily bool, maxFiles int, mode os.FileMode) (*RotatingFileWriter, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("观测文件路径不能为空")
	}
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	if maxFiles < 1 {
		maxFiles = 1
	}
	if mode == 0 {
		mode = DefaultFileMode
	}
	w := &RotatingFileWriter{path: path, maxBytes: maxBytes, maxFiles: maxFiles, rotateDaily: rotateDaily, mode: mode}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingFileWriter) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, w.mode)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file, w.size, w.day = file, info.Size(), time.Now().Format("20060102")
	return nil
}

func (w *RotatingFileWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	today := time.Now().Format("20060102")
	if (w.maxBytes > 0 && w.size+int64(len(data)) > w.maxBytes) || (w.rotateDaily && today != w.day) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	return n, err
}

func (w *RotatingFileWriter) rotate() error {
	if w.file != nil {
		_ = w.file.Close()
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.maxFiles))
	for i := w.maxFiles - 1; i >= 1; i-- {
		old, next := fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(old); err == nil {
			_ = os.Rename(old, next)
		}
	}
	_ = os.Rename(w.path, w.path+".1")
	return w.open()
}

func (w *RotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

var _ io.WriteCloser = (*RotatingFileWriter)(nil)
