package observability

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TraceSnapshotSpan is the redacted, low-cardinality representation persisted
// for local diagnosis. It deliberately has no event or Tool payload field.
type TraceSnapshotSpan struct {
	Name       string            `json:"name"`
	TraceID    string            `json:"trace_id,omitempty"`
	SpanID     string            `json:"span_id,omitempty"`
	Status     string            `json:"status,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// WriteTraceSnapshot atomically replaces a compact snapshot with mode 0600.
// It is a standalone sink so exporter/file failures stay outside business flow.
func WriteTraceSnapshot(path string, spans []TraceSnapshotSpan) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("trace snapshot path 不能为空")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for i := range spans {
		spans[i].Name = safeValue("span.name", spans[i].Name)
		spans[i].TraceID = safeValue("trace_id", spans[i].TraceID)
		spans[i].SpanID = safeValue("span_id", spans[i].SpanID)
		spans[i].Status = safeValue("status", spans[i].Status)
		for key, value := range spans[i].Attributes {
			if strings.Contains(strings.ToLower(key), "payload") || strings.Contains(strings.ToLower(key), "prompt") || strings.Contains(strings.ToLower(key), "authorization") || strings.Contains(strings.ToLower(key), "sql") {
				delete(spans[i].Attributes, key)
				continue
			}
			spans[i].Attributes[key] = safeValue(key, value)
		}
	}
	payload, err := json.Marshal(struct {
		GeneratedAt time.Time           `json:"generated_at"`
		Spans       []TraceSnapshotSpan `json:"spans"`
	}{GeneratedAt: time.Now().UTC(), Spans: spans})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trace-snapshot-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func rotateTraceSnapshotIfNeeded(path string, maxBytes int64, rotateDaily bool, retention int) error {
	if maxBytes <= 0 && !rotateDaily {
		return nil
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	rotate := maxBytes > 0 && info.Size() >= maxBytes
	if rotateDaily {
		rotate = rotate || info.ModTime().Format("20060102") != time.Now().Format("20060102")
	}
	if !rotate {
		return nil
	}
	if retention < 1 {
		retention = 1
	}
	_ = os.Remove(path + "." + strconv.Itoa(retention))
	for index := retention - 1; index >= 1; index-- {
		oldPath := path + "." + strconv.Itoa(index)
		if _, statErr := os.Stat(oldPath); statErr == nil {
			if renameErr := os.Rename(oldPath, path+"."+strconv.Itoa(index+1)); renameErr != nil {
				return renameErr
			}
		}
	}
	return os.Rename(path, path+".1")
}

// ReadTraceSnapshot reads the last bounded local snapshot after a process restart.
func ReadTraceSnapshot(path string) ([]TraceSnapshotSpan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Spans []TraceSnapshotSpan `json:"spans"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload.Spans, nil
}
