package runner

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/composition"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	httpapi "github.com/observer-mimiron/supervisor-template/internal/interfaces/http"

	eval "github.com/observer-mimiron/supervisor-template/eval"
)

const DefaultToken = "demo-token"
const EvaluatorVersion = "1"

type Evidence struct {
	RunID               string              `json:"run_id"`
	CaseID              string              `json:"case_id"`
	CaseVersion         string              `json:"case_version"`
	CodeVersion         string              `json:"code_version"`
	EvaluatorVersion    string              `json:"evaluator_version"`
	Fixture             string              `json:"fixture,omitempty"`
	Subject             string              `json:"subject,omitempty"`
	TraceID             string              `json:"trace_id,omitempty"`
	Events              []Event             `json:"events"`
	Terminal            string              `json:"terminal,omitempty"`
	Error               string              `json:"error,omitempty"`
	HTTPStatus          []int               `json:"http_statuses"`
	ElapsedMS           int64               `json:"elapsed_ms"`
	Retries             int                 `json:"retries"`
	Result              map[string]any      `json:"result,omitempty"`
	FailedStepID        string              `json:"failed_step_id,omitempty"`
	VerdictDigest       string              `json:"verdict_digest,omitempty"`
	RepeatChecked       bool                `json:"repeat_checked,omitempty"`
	RepeatVerdictStable bool                `json:"repeat_verdict_stable,omitempty"`
	FakeWriteCount      int                 `json:"fake_write_count"`
	CleanupResult       string              `json:"cleanup_result,omitempty"`
	RegisteredTools     []string            `json:"registered_tools,omitempty"`
	WorkerToolAllowList map[string][]string `json:"worker_tool_allow_list,omitempty"`
}

type Event struct {
	EventID  string            `json:"event_id"`
	RunID    string            `json:"run_id"`
	TraceID  string            `json:"trace_id,omitempty"`
	Sequence int64             `json:"sequence"`
	Type     string            `json:"type"`
	Data     map[string]string `json:"data,omitempty"`
}

type Runner struct {
	ConfigPath  string
	CodeVersion string
	Token       string
}

type preCallError struct{ err error }

func (e *preCallError) Error() string { return e.err.Error() }
func (e *preCallError) Unwrap() error { return e.err }

func retryPreCall[T any](budget int, retries *int, call func() (T, int, error)) (T, int, error) {
	for attempt := 0; ; attempt++ {
		result, status, err := call()
		if err == nil {
			return result, status, nil
		}
		var preCall *preCallError
		if !errors.As(err, &preCall) || attempt >= budget {
			return result, status, err
		}
		(*retries)++
	}
}

func New(configPath, codeVersion, token string) *Runner {
	if configPath == "" {
		configPath = "config.example.toml"
	}
	if codeVersion == "" {
		codeVersion = "dev"
	}
	if token == "" {
		token = DefaultToken
	}
	return &Runner{ConfigPath: configPath, CodeVersion: codeVersion, Token: token}
}

func (r *Runner) RunCase(ctx context.Context, item eval.Case) (evidence Evidence, runErr error) {
	if r == nil {
		return Evidence{}, errors.New("runner is nil")
	}
	caseTimeout, err := time.ParseDuration(item.Timeout)
	if err != nil || caseTimeout <= 0 {
		return Evidence{}, fmt.Errorf("case timeout: %w", err)
	}
	caseCtx, cancel := context.WithTimeout(ctx, caseTimeout)
	defer cancel()
	started := time.Now()
	if item.Preconditions.Fixture != "synthetic_audience_v1" {
		return evidence, fmt.Errorf("unsupported fixture %q", item.Preconditions.Fixture)
	}
	subject := strings.TrimSpace(item.Preconditions.Subject)
	if subject == "" {
		subject = "demo-user"
	}
	evidence = Evidence{CaseID: item.ID, CaseVersion: item.Version, CodeVersion: r.CodeVersion, EvaluatorVersion: EvaluatorVersion, Fixture: item.Preconditions.Fixture, Subject: subject, RepeatVerdictStable: true}
	headers := evalHeaders(item, r.CodeVersion)
	server, closeServer, writeCount, snapshot, err := newLocalServer(r.ConfigPath, r.Token)
	if err != nil {
		return evidence, err
	}
	startWriteCount := writeCount()
	evidence.RegisteredTools = append([]string(nil), snapshot.ToolIDs...)
	evidence.WorkerToolAllowList = snapshot.WorkerTools
	defer func() {
		evidence.FakeWriteCount = writeCount() - startWriteCount
		if cleanupErr := closeServer(); cleanupErr != nil {
			evidence.CleanupResult = cleanupErr.Error()
			if runErr == nil {
				runErr = cleanupErr
			}
			return
		}
		evidence.CleanupResult = "ok"
	}()
	client := server.Client()
	seen := map[string]bool{}
	for index, step := range item.RequestSteps {
		if err := caseCtx.Err(); err != nil {
			return evidence, err
		}
		runID := item.ID
		if step.RunID != "" {
			runID = step.RunID
		}
		switch step.Action {
		case "chat":
			token := tokenForSubject(stepSubject(step, subject), r.Token)
			events, status, requestErr := retryPreCall(item.RetryBudget, &evidence.Retries, func() ([]Event, int, error) {
				return postChat(caseCtx, client, server.URL, token, runID, step, headers)
			})
			evidence.HTTPStatus = append(evidence.HTTPStatus, status)
			if requestErr != nil {
				evidence.Error = requestErr.Error()
			}
			mergeEvents(&evidence, events, seen)
		case "approval":
			token := tokenForSubject(stepSubject(step, subject), r.Token)
			_, status, requestErr := retryPreCall(item.RetryBudget, &evidence.Retries, func() (struct{}, int, error) {
				status, err := postJSON(caseCtx, client, server.URL+"/api/runs/"+runID+"/approval", token, map[string]string{"decision": step.Decision}, headers)
				return struct{}{}, status, err
			})
			evidence.HTTPStatus = append(evidence.HTTPStatus, status)
			if requestErr != nil {
				evidence.Error = requestErr.Error()
			}
			if requestErr == nil && step.Decision == "reject" {
				events, replayStatus, replayErr := retryPreCall(item.RetryBudget, &evidence.Retries, func() ([]Event, int, error) {
					return postResume(caseCtx, client, server.URL, token, runID, headers)
				})
				evidence.HTTPStatus = append(evidence.HTTPStatus, replayStatus)
				if replayErr != nil {
					evidence.Error = replayErr.Error()
				}
				mergeEvents(&evidence, events, seen)
			}
		case "resume", "repeat":
			token := tokenForSubject(stepSubject(step, subject), r.Token)
			before := evidenceVerdictDigest(evidence)
			events, status, requestErr := retryPreCall(item.RetryBudget, &evidence.Retries, func() ([]Event, int, error) {
				return postResume(caseCtx, client, server.URL, token, runID, headers)
			})
			evidence.HTTPStatus = append(evidence.HTTPStatus, status)
			if requestErr != nil {
				evidence.Error = requestErr.Error()
			}
			mergeEvents(&evidence, events, seen)
			if step.Action == "repeat" {
				evidence.RepeatChecked = true
				after := evidenceVerdictDigest(evidence)
				evidence.RepeatVerdictStable = before == "" || before == after
			}
		case "cancel":
			token := tokenForSubject(stepSubject(step, subject), r.Token)
			_, status, requestErr := retryPreCall(item.RetryBudget, &evidence.Retries, func() (struct{}, int, error) {
				status, err := postJSON(caseCtx, client, server.URL+"/api/runs/"+runID+"/cancel", token, nil, headers)
				return struct{}{}, status, err
			})
			evidence.HTTPStatus = append(evidence.HTTPStatus, status)
			if requestErr != nil {
				evidence.Error = requestErr.Error()
			}
			if requestErr == nil {
				events, replayStatus, replayErr := postResume(caseCtx, client, server.URL, token, runID, headers)
				evidence.HTTPStatus = append(evidence.HTTPStatus, replayStatus)
				if replayErr != nil {
					evidence.Error = replayErr.Error()
				}
				mergeEvents(&evidence, events, seen)
			}
		default:
			return evidence, fmt.Errorf("unsupported action at step %d", index)
		}
	}
	for _, event := range evidence.Events {
		if event.TraceID != "" {
			evidence.TraceID = event.TraceID
			break
		}
	}
	for _, event := range evidence.Events {
		switch event.Type {
		case "completed", "failed", "canceled":
			evidence.Terminal = event.Type
		}
	}
	captureResult(&evidence, item.ExpectedResults.Result)
	evidence.VerdictDigest = evidenceVerdictDigest(evidence)
	evidence.RunID = item.ID
	evidence.ElapsedMS = time.Since(started).Milliseconds()
	return evidence, nil
}

func newLocalServer(configPath, token string) (*httptest.Server, func() error, func() int, composition.RuntimeSnapshot, error) {
	if absolute, err := filepath.Abs(configPath); err == nil {
		configPath = absolute
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, nil, composition.RuntimeSnapshot{}, err
	}
	for _, credential := range cfg.Auth.Credentials {
		credentialToken := tokenForSubject(credential.SubjectID, token)
		digest := sha256.Sum256([]byte(credentialToken))
		if os.Getenv(credential.TokenSHA256Env) == "" {
			if err := os.Setenv(credential.TokenSHA256Env, hex.EncodeToString(digest[:])); err != nil {
				return nil, nil, nil, composition.RuntimeSnapshot{}, err
			}
		}
	}
	cfg.Model.Provider = "fake"
	cfg.Observability.Enabled = false
	cfg.Observability.LangfuseEnabled = false
	cfg.Observability.Endpoint = ""
	cfg.Observability.LangfuseEndpoint = ""
	cfg.Observability.LogFile = ""
	cfg.Observability.TraceFile = ""
	app, err := composition.New(cfg)
	if err != nil {
		return nil, nil, nil, composition.RuntimeSnapshot{}, err
	}
	server := httptest.NewServer(httpapi.NewRouter(app.Run, app.Health, app.Authenticator))
	return server, func() error {
		server.Close()
		if app.Close != nil {
			return app.Close(context.Background())
		}
		return nil
	}, app.FakeWriteCount, app.RuntimeSnapshot(), nil
}

func evalHeaders(item eval.Case, codeVersion string) map[string]string {
	return map[string]string{
		"X-Eval-Case-ID": item.ID, "X-Eval-Case-Version": item.Version,
		"X-Eval-Code-Version": codeVersion, "X-Eval-Evaluator-Version": EvaluatorVersion,
	}
}

func stepSubject(step eval.RequestStep, fallback string) string {
	if subject := strings.TrimSpace(step.Subject); subject != "" {
		return subject
	}
	return fallback
}

func tokenForSubject(subject, base string) string {
	if strings.TrimSpace(base) == "" {
		base = DefaultToken
	}
	if subject == "" || subject == "demo-user" {
		return base
	}
	return base + "-" + subject
}

func captureResult(evidence *Evidence, expected map[string]any) {
	if len(expected) == 0 {
		return
	}
	projected := make(map[string]any, len(expected))
	for key := range expected {
		if key == "http_status" && len(evidence.HTTPStatus) > 0 {
			projected[key] = evidence.HTTPStatus[len(evidence.HTTPStatus)-1]
		}
	}
	for index := len(evidence.Events) - 1; index >= 0; index-- {
		event := evidence.Events[index]
		if event.Type != "text" || strings.TrimSpace(event.Data["content"]) == "" {
			continue
		}
		var raw map[string]any
		decoder := json.NewDecoder(strings.NewReader(event.Data["content"]))
		if err := decoder.Decode(&raw); err != nil || raw == nil {
			continue
		}
		for key := range expected {
			if value, ok := raw[key]; ok {
				projected[key] = value
			}
		}
		break
	}
	evidence.Result = projected
}

func evidenceVerdictDigest(evidence Evidence) string {
	type verdict struct {
		Events   []Event        `json:"events"`
		Terminal string         `json:"terminal"`
		Result   map[string]any `json:"result,omitempty"`
	}
	data, err := json.Marshal(verdict{Events: evidence.Events, Terminal: terminalFromEvents(evidence.Events), Result: evidence.Result})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func terminalFromEvents(events []Event) string {
	terminal := ""
	for _, event := range events {
		switch event.Type {
		case "completed", "failed", "canceled":
			terminal = event.Type
		}
	}
	return terminal
}

func postChat(ctx context.Context, client *http.Client, base, token, runID string, step eval.RequestStep, headers map[string]string) ([]Event, int, error) {
	body := map[string]string{"conversation_id": step.ConversationID, "run_id": runID, "message": step.Message}
	var events []Event
	status, err := postSSE(ctx, client, base+"/api/chat", token, body, &events, headers)
	return events, status, err
}

func postResume(ctx context.Context, client *http.Client, base, token, runID string, headers map[string]string) ([]Event, int, error) {
	var events []Event
	status, err := postSSE(ctx, client, base+"/api/runs/"+runID+"/resume", token, nil, &events, headers)
	return events, status, err
}

func postSSE(ctx context.Context, client *http.Client, url, token string, body any, events *[]Event, headers map[string]string) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, &preCallError{err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return 0, &preCallError{err: err}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if traceID := response.Header.Get("X-Trace-ID"); traceID != "" {
			*events = append(*events, Event{TraceID: traceID})
		}
		return response.StatusCode, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			dataLine := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var event httpapi.EventEnvelope
			if err := json.Unmarshal([]byte(dataLine), &event); err == nil {
				*events = append(*events, Event{EventID: event.EventID, RunID: event.RunID, TraceID: event.TraceID, Sequence: event.Sequence, Type: string(event.Type), Data: event.Data})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return response.StatusCode, err
	}
	return response.StatusCode, nil
}

func postJSON(ctx context.Context, client *http.Client, url, token string, body any, headers map[string]string) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, &preCallError{err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return 0, &preCallError{err: err}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return response.StatusCode, nil
}

func mergeEvents(evidence *Evidence, events []Event, seen map[string]bool) {
	for _, event := range events {
		if event.Type == "" {
			if event.TraceID != "" {
				evidence.TraceID = event.TraceID
			}
			continue
		}
		if event.EventID != "" && seen[event.EventID] {
			continue
		}
		seen[event.EventID] = true
		evidence.Events = append(evidence.Events, event)
		if stepID := event.Data["step_id"]; stepID != "" {
			evidence.FailedStepID = stepID
		}
	}
}
