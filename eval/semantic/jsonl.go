package semantic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Local semantic records are strict JSON/JSONL files. They are the source of
// truth for materials, human labels and calibration evidence, so a record that
// cannot be read exactly is rejected rather than interpreted: a mis-read label
// is indistinguishable from a wrong one once it reaches a statistic.
//
// Every reader here refuses duplicate object keys, unknown fields, malformed
// lines, trailing JSON values and invalid UTF-8. Every writer creates files with
// mode 0600 inside a mode-0700 directory, because these files hold human
// judgements and evaluation material that must not be published.

// DecodeJSONL reads a strict JSONL file whose lines are single JSON objects of T.
//
// Blank lines are skipped. Anything else — a JSON array, a scalar, a truncated
// object, two values on one line, a duplicate key, an unknown field or invalid
// UTF-8 — is an error naming the line number.
func DecodeJSONL[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeJSONLBytes[T](data)
}

func decodeJSONLBytes[T any](data []byte) ([]T, error) {
	if !utf8.Valid(data) {
		return nil, Reject(ReasonInvalidMaterial, "file is not valid UTF-8")
	}
	records := make([]T, 0)
	for index, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var record T
		if err := decodeStrict([]byte(trimmed), &record); err != nil {
			return nil, Reject(ReasonInvalidMaterial, "line %d: %v", index+1, err)
		}
		records = append(records, record)
	}
	return records, nil
}

// DecodeJSONStrict reads one strict JSON document into target. It is the
// single-value sibling of DecodeJSONL and applies the same refusals.
func DecodeJSONStrict(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := decodeStrict(data, target); err != nil {
		return Reject(ReasonInvalidMaterial, "%v", err)
	}
	return nil
}

// EncodeJSONStrict renders one record as canonical, HTML-unescaped JSON.
func EncodeJSONStrict(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// WriteJSONLNew writes records as strict JSONL to a path that must not exist.
// Candidate prediction files are evidence: silently overwriting an earlier
// collection would destroy the denominator it recorded.
func WriteJSONLNew[T any](path string, records []T) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	if err := EnsureParentDir(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Reject(ReasonInvalidMaterial, "refusing to overwrite existing file")
		}
		return err
	}
	if _, err := file.Write(buffer.Bytes()); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// WriteJSONSecret writes one JSON document with mode 0600, replacing any
// previous local report. The calibration report is a rebuildable local artifact,
// so re-running the offline replay must be able to refresh it.
func WriteJSONSecret(path string, value any) error {
	data, err := EncodeJSONStrict(value)
	if err != nil {
		return err
	}
	if err := EnsureParentDir(path); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// EnsureParentDir creates the parent directory of path with mode 0700.
func EnsureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

func decodeStrict(data []byte, target any) error {
	if !utf8.Valid(data) {
		return errors.New("not valid UTF-8")
	}
	if first := firstToken(data); first != '{' {
		return errors.New("expected a JSON object")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func firstToken(data []byte) byte {
	for _, value := range data {
		switch value {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return value
		}
	}
	return 0
}

// rejectDuplicateKeys walks the raw token stream because encoding/json silently
// keeps the last duplicate key. A record with two `label` fields has no single
// reading, and picking one would fabricate a human judgement.
func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(json.Token) error
	walk = func(token json.Token) error {
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = true
				value, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := walk(value); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				value, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := walk(value); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		}
		return nil
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := walk(token); err != nil {
			return err
		}
	}
}
