// Package langfuse provides an optional, trace-linked score sink for eval reports.
// It deliberately uses the public HTTP API so the Go runtime does not depend on
// a third-party Langfuse SDK or make Langfuse a local test prerequisite.
package langfuse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/observer-mimiron/supervisor-template/eval/evaluator"
)

type Client struct {
	baseURL    string
	publicKey  string
	secretKey  string
	httpClient *http.Client
}

type score struct {
	TraceID  string            `json:"traceId"`
	Name     string            `json:"name"`
	Value    float64           `json:"value"`
	DataType string            `json:"dataType"`
	Comment  string            `json:"comment,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func New(baseURL, publicKey, secretKey string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("LANGFUSE_API_URL 未设置")
	}
	if publicKey == "" || secretKey == "" {
		return nil, fmt.Errorf("LANGFUSE_PUBLIC_KEY/LANGFUSE_SECRET_KEY 未设置")
	}
	return &Client{baseURL: baseURL, publicKey: publicKey, secretKey: secretKey, httpClient: http.DefaultClient}, nil
}

// UploadReport uploads boolean evaluator scores only when a report has a
// trace_id. Local fake runs have no external trace and are intentionally skipped.
func (c *Client) UploadReport(ctx context.Context, report evaluator.Report) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("Langfuse client 未装配")
	}
	uploaded := 0
	for _, item := range report.Cases {
		traceID := item.Evidence.TraceID
		if traceID == "" {
			continue
		}
		for _, result := range item.Results {
			metadata := map[string]string{
				"case_id": item.CaseID, "case_version": item.CaseVersion,
				"code_version": report.CodeVersion, "evaluator_version": result.Version,
			}
			if report.EvaluationProfile.Scenario != "" {
				metadata["evaluation_plane"] = report.EvaluationProfile.EvaluationPlane
				metadata["scenario"] = report.EvaluationProfile.Scenario
				metadata["tier"] = report.EvaluationProfile.Tier
			}
			if err := c.uploadScore(ctx, score{
				TraceID: traceID, Name: result.Evaluator, Value: boolValue(result.Passed), DataType: "BOOLEAN",
				Comment:  scoreComment(result),
				Metadata: metadata,
			}); err != nil {
				return uploaded, err
			}
			uploaded++
		}
	}
	return uploaded, nil
}

// scoreComment renders the upload-safe comment for one evaluator result.
//
// It is built only from closed-set contract fields (the evaluator outcome, the
// failed assertion and its taxonomy), never from evaluator prose or evidence
// values. FR-008 forbids this integration from receiving prompts, tool payloads,
// raw SQL parameters, credentials or host paths, so forwarding the free-form
// Result.Reason would make compliance depend on every evaluator remembering not
// to interpolate evidence into it -- which business_correctness does not. The
// detailed reason stays in the local JSON report; the sink gets structure only.
func scoreComment(result evaluator.Result) string {
	if result.Passed {
		return ""
	}
	fields := make([]string, 0, 2)
	if result.FailedAssertion != "" {
		fields = append(fields, "failed_assertion="+result.FailedAssertion)
	}
	if result.FailureTaxonomy != "" {
		fields = append(fields, "taxonomy="+result.FailureTaxonomy)
	}
	return strings.Join(fields, " ")
}

func (c *Client) uploadScore(ctx context.Context, payload score) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/public/scores", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.publicKey, c.secretKey)
	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("Langfuse score API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
