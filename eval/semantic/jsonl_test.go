package semantic

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type record struct {
	SampleID string `json:"sample_id"`
	Label    string `json:"label"`
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// A record file with two readings of the same key has no single meaning.
// encoding/json would silently keep the last one and fabricate a judgement.
func TestDecodeJSONLRejectsDuplicateKeys(t *testing.T) {
	path := writeTemp(t, "duplicate.jsonl",
		"{\"sample_id\":\"s-1\",\"label\":\"relevant\"}\n"+
			"{\"sample_id\":\"s-2\",\"label\":\"relevant\",\"label\":\"irrelevant\"}\n")
	_, err := DecodeJSONL[record](path)
	if err == nil {
		t.Fatal("expected duplicate JSON key to be rejected")
	}
	if !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("expected a duplicate-key error, got %v", err)
	}
	if reason := ReasonOf(err); reason != ReasonInvalidMaterial {
		t.Fatalf("expected invalid_material reason, got %s", reason)
	}
}

func TestDecodeJSONLRejectsUnknownFields(t *testing.T) {
	path := writeTemp(t, "unknown.jsonl", "{\"sample_id\":\"s-1\",\"label\":\"relevant\",\"hard_passed\":true}\n")
	_, err := DecodeJSONL[record](path)
	if err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected an unknown-field error, got %v", err)
	}
}

func TestDecodeJSONLRejectsMalformedLines(t *testing.T) {
	cases := map[string]string{
		"truncated object": "{\"sample_id\":\"s-1\",\"label\":\"relevant\"\n",
		"array line":       "[{\"sample_id\":\"s-1\"}]\n",
		"scalar line":      "\"just a string\"\n",
		"empty object ok":  "{}\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, "malformed.jsonl", content)
			_, err := DecodeJSONL[record](path)
			if name == "empty object ok" {
				// `{}` is a well-formed object; closed-value validation, not the
				// JSONL reader, is what rejects an empty record.
				if err != nil {
					t.Fatalf("well-formed empty object must decode, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

func TestDecodeJSONLRejectsTrailingJSONValues(t *testing.T) {
	path := writeTemp(t, "trailing.jsonl",
		"{\"sample_id\":\"s-1\",\"label\":\"relevant\"} {\"sample_id\":\"s-2\",\"label\":\"irrelevant\"}\n")
	_, err := DecodeJSONL[record](path)
	if err == nil {
		t.Fatal("expected two JSON values on one line to be rejected")
	}
	if !strings.Contains(err.Error(), "trailing JSON value") {
		t.Fatalf("expected a trailing-value error, got %v", err)
	}
}

func TestDecodeJSONLRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.jsonl")
	if err := os.WriteFile(path, []byte("{\"sample_id\":\"s-1\",\"label\":\"\xff\xfe\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJSONL[record](path); err == nil {
		t.Fatal("expected invalid UTF-8 to be rejected")
	}
}

func TestDecodeJSONLAcceptsStrictRecordsAndReportsLineNumbers(t *testing.T) {
	path := writeTemp(t, "ok.jsonl",
		"\n"+
			"{\"sample_id\":\"s-1\",\"label\":\"relevant\"}\n"+
			"   \n"+
			"{\"sample_id\":\"s-2\",\"label\":\"irrelevant\"}\n")
	records, err := DecodeJSONL[record](path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 || records[0].SampleID != "s-1" || records[1].Label != "irrelevant" {
		t.Fatalf("unexpected records: %+v", records)
	}

	bad := writeTemp(t, "bad-line.jsonl",
		"{\"sample_id\":\"s-1\",\"label\":\"relevant\"}\n"+
			"{not json}\n")
	_, err = DecodeJSONL[record](bad)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected a line-2 error, got %v", err)
	}
}

func TestWriteJSONLNewUsesMode0600AndRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "records.jsonl")
	records := []record{{SampleID: "s-1", Label: "relevant"}}
	if err := WriteJSONLNew(path, records); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %o", info.Mode().Perm())
	}
	reloaded, err := DecodeJSONL[record](path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0] != records[0] {
		t.Fatalf("round trip mismatch: %+v", reloaded)
	}
	// Candidate files are evidence; a second collection must not silently
	// replace the denominator the first one recorded.
	err = WriteJSONLNew(path, records)
	if err == nil {
		t.Fatal("expected overwrite to be refused")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("unexpected overwrite error: %v", err)
	}
}

func TestWriteJSONSecretUsesMode0600AndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "calibration.json")
	if err := WriteJSONSecret(path, map[string]string{"state": "first"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %o", info.Mode().Perm())
	}
	if err := WriteJSONSecret(path, map[string]string{"state": "second"}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	var decoded map[string]string
	if err := DecodeJSONStrict(path, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["state"] != "second" {
		t.Fatalf("expected replacement, got %v", decoded)
	}
}

func TestDecodeJSONStrictRejectsDuplicateKeysAndTrailingValues(t *testing.T) {
	duplicate := writeTemp(t, "dup.json", "{\"sample_id\":\"s-1\",\"sample_id\":\"s-2\"}")
	var target record
	if err := DecodeJSONStrict(duplicate, &target); err == nil {
		t.Fatal("expected duplicate key to be rejected")
	}
	trailing := writeTemp(t, "trail.json", "{\"sample_id\":\"s-1\"}{\"sample_id\":\"s-2\"}")
	if err := DecodeJSONStrict(trailing, &target); err == nil {
		t.Fatal("expected trailing value to be rejected")
	}
}

func TestRejectionKeepsClosedReasonWithoutEchoingContent(t *testing.T) {
	err := Reject(ReasonSensitiveInput, "material %s refused", "s-1")
	if ReasonOf(err) != ReasonSensitiveInput {
		t.Fatalf("expected sensitive_input, got %s", ReasonOf(err))
	}
	if strings.Contains(err.Error(), "BEGIN PRIVATE KEY") {
		t.Fatal("rejection must not echo rejected content")
	}
	if ReasonOf(errors.New("plain error")) != ReasonInvalidMaterial {
		t.Fatal("an unclassified error must fall back to invalid_material")
	}
}
