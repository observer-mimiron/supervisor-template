// Package runner is the local Runtime adapter of the single Executor boundary:
// it turns one Case into bounded, redacted Evidence by starting the project's
// HTTP/SSE entrypoint in-process and driving the declared request steps.
//
// The package owns transport, the declared timeout/retry budget, cleanup,
// profile/executor matching and evidence collection. It does not own Runtime
// state, Policy, Approval, Idempotency, Tool execution or terminal-event
// decisions: those remain in the application, and the Runner only observes the
// events they produce. It never asserts a verdict — that is eval/evaluator — and
// it always forces the local fake model, so it may only report the
// `runtime-fake` profile.
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/composition"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	httpapi "github.com/observer-mimiron/supervisor-template/internal/interfaces/http"

	eval "github.com/observer-mimiron/supervisor-template/eval"
)

const DefaultToken = "demo-token"
const EvaluatorVersion = "2"

type Evidence struct {
	Profile                eval.EvaluationProfile              `json:"evaluation_profile"`
	RunID                  string                              `json:"run_id"`
	CaseID                 string                              `json:"case_id"`
	CaseVersion            string                              `json:"case_version"`
	CodeVersion            string                              `json:"code_version"`
	EvaluatorVersion       string                              `json:"evaluator_version"`
	Fixture                string                              `json:"fixture,omitempty"`
	Subject                string                              `json:"subject,omitempty"`
	TraceID                string                              `json:"trace_id,omitempty"`
	Events                 []Event                             `json:"events"`
	Terminal               string                              `json:"terminal,omitempty"`
	Error                  string                              `json:"error,omitempty"`
	HTTPStatus             []int                               `json:"http_statuses"`
	ElapsedMS              int64                               `json:"elapsed_ms"`
	Retries                int                                 `json:"retries"`
	Result                 map[string]any                      `json:"result,omitempty"`
	FailedStepID           string                              `json:"failed_step_id,omitempty"`
	VerdictDigest          string                              `json:"verdict_digest,omitempty"`
	RepeatChecked          bool                                `json:"repeat_checked,omitempty"`
	RepeatVerdictStable    bool                                `json:"repeat_verdict_stable,omitempty"`
	FakeWriteCount         int                                 `json:"fake_write_count"`
	CleanupResult          string                              `json:"cleanup_result,omitempty"`
	RegisteredTools        []string                            `json:"registered_tools,omitempty"`
	WorkerToolAllowList    map[string][]string                 `json:"worker_tool_allow_list,omitempty"`
	RegisteredToolMetadata map[string]composition.ToolMetadata `json:"registered_tool_metadata,omitempty"`
	Database               DatabaseEvidence                    `json:"database,omitempty"`
	Logs                   DiagnosticLogEvidence               `json:"logs,omitempty"`
}

// DatabaseEvidence contains only the bounded summaries needed by a case.
// Individual rows are never written to the report.
type DatabaseEvidence struct {
	Available         bool   `json:"available"`
	Backend           string `json:"backend,omitempty"`
	BeforeOrderCount  int    `json:"before_order_count,omitempty"`
	AfterOrderCount   int    `json:"after_order_count,omitempty"`
	BeforeOrderDigest string `json:"before_order_digest,omitempty"`
	AfterOrderDigest  string `json:"after_order_digest,omitempty"`
}

// DiagnosticLogEvidence is a low-cardinality projection of local JSON logs.
// It intentionally excludes messages, SQL, payloads and file paths.
type DiagnosticLogEvidence struct {
	Available       bool     `json:"available"`
	RecordCount     int      `json:"record_count,omitempty"`
	Phases          []string `json:"phases,omitempty"`
	ErrorCodes      []string `json:"error_codes,omitempty"`
	RedactionPassed bool     `json:"redaction_passed"`
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
	ConfigPath          string
	CodeVersion         string
	Token               string
	EnableObservability bool
	Profile             eval.EvaluationProfile
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
	return &Runner{ConfigPath: configPath, CodeVersion: codeVersion, Token: token, Profile: eval.RuntimeFakeProfile(eval.TierPR)}
}

// NewWithObservability keeps the local fake runner but preserves configured
// OTel/Langfuse sinks for an explicit diagnostic or score-upload run.
func NewWithObservability(configPath, codeVersion, token string) *Runner {
	runner := New(configPath, codeVersion, token)
	runner.EnableObservability = true
	return runner
}

// ValidateProfile reports whether the profile this Runner stamps onto Evidence
// describes the executor that actually runs the Case. This Runtime Runner always
// forces the local fake model, so it may only report `runtime-fake`; a coding or
// real-model profile is an evaluation_setup_error rather than a run that would
// mislabel its own evidence.
func (r *Runner) ValidateProfile() error {
	if r == nil {
		return errors.New("evaluation_setup_error: runner is nil")
	}
	if err := r.Profile.Validate(); err != nil {
		return err
	}
	if r.Profile.Scenario != eval.ScenarioRuntimeFake || profileValue(r.Profile.ModelProvider) != eval.ProviderFake || profileValue(r.Profile.Executor) != eval.ExecutorLocalFake {
		return fmt.Errorf("evaluation_setup_error: local runner requires scenario %q with executor %q and provider %q, got scenario %q with executor %q and provider %q",
			eval.ScenarioRuntimeFake, eval.ExecutorLocalFake, eval.ProviderFake,
			r.Profile.Scenario, profileValue(r.Profile.Executor), profileValue(r.Profile.ModelProvider))
	}
	return nil
}

func profileValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (r *Runner) RunCase(ctx context.Context, item eval.Case) (evidence Evidence, runErr error) {
	// chat_cancel 的取消由独立 goroutine 发出，与主流程共享 evidence。
	var cancelMutex sync.Mutex
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
	// The fixture name identifies the environment the Case expects; the actual
	// environment is whatever configuration this Runner starts. Only the
	// reference format is enforced here.
	if !eval.ValidFixtureName(item.Preconditions.Fixture) {
		return evidence, fmt.Errorf("evaluation_setup_error: invalid fixture name %q", item.Preconditions.Fixture)
	}
	subject := strings.TrimSpace(item.Preconditions.Subject)
	if subject == "" {
		subject = "demo-user"
	}
	if err := r.ValidateProfile(); err != nil {
		return evidence, err
	}
	evidence = Evidence{Profile: r.Profile, CaseID: item.ID, CaseVersion: item.Version, CodeVersion: r.CodeVersion, EvaluatorVersion: EvaluatorVersion, Fixture: item.Preconditions.Fixture, Subject: subject, RepeatVerdictStable: true}
	headers := evalHeaders(item, r.CodeVersion)
	needsDatabase := item.Postconditions.Database != nil
	needsLogs := item.Postconditions.Logs != nil
	server, closeServer, writeCount, snapshot, databaseProbe, readLogs, err := newLocalServer(r.ConfigPath, r.Token, r.EnableObservability, needsLogs)
	if err != nil {
		return evidence, err
	}
	startWriteCount := writeCount()
	evidence.RegisteredTools = append([]string(nil), snapshot.ToolIDs...)
	evidence.WorkerToolAllowList = snapshot.WorkerTools
	evidence.RegisteredToolMetadata = snapshot.ToolMetadata
	if needsDatabase {
		if databaseProbe == nil {
			evidence.Error = "database postcondition requested but no database probe is available"
		} else if state, probeErr := databaseProbe(caseCtx); probeErr != nil {
			evidence.Error = probeErr.Error()
		} else {
			evidence.Database = databaseEvidence(state, true, true)
		}
	}
	defer func() {
		if needsDatabase && databaseProbe != nil {
			if state, probeErr := databaseProbe(caseCtx); probeErr != nil {
				if evidence.Error == "" {
					evidence.Error = probeErr.Error()
				}
			} else {
				after := databaseEvidence(state, false, true)
				evidence.Database.Backend = after.Backend
				evidence.Database.AfterOrderCount = after.AfterOrderCount
				evidence.Database.AfterOrderDigest = after.AfterOrderDigest
				evidence.Database.Available = evidence.Database.Available && after.Available
			}
		}
		if needsLogs && readLogs != nil {
			if logs, logErr := readLogs(); logErr != nil {
				if evidence.Error == "" {
					evidence.Error = logErr.Error()
				}
			} else {
				evidence.Logs = logs
			}
		}
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
		case "chat", "chat_cancel":
			token := tokenForSubject(stepSubject(step, subject), r.Token)
			// chat_cancel cancels the Run while it is still executing. The trigger
			// is a timer, not an SSE event: the HTTP layer writes the event stream
			// only after the Run finished (handler.go projects service.Events after
			// Start returns), so no client can observe "execution started" live.
			// The execution window is deterministic instead — it stays open for the
			// whole tool delay configured via tools.*.fake_delay_ms — so a cancel
			// sent inside it is reliable. A case whose cancel lands outside that
			// window fails loudly: the Run completes instead of being canceled.
			var cancelDone chan struct{}
			if step.Action == "chat_cancel" && step.CancelAfterMS > 0 {
				cancelDone = make(chan struct{})
				go func() {
					defer close(cancelDone)
					timer := time.NewTimer(time.Duration(step.CancelAfterMS) * time.Millisecond)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-caseCtx.Done():
						return
					}
					status, cancelErr := postJSON(caseCtx, client, server.URL+"/api/runs/"+runID+"/cancel", token, nil, headers)
					cancelMutex.Lock()
					evidence.HTTPStatus = append(evidence.HTTPStatus, status)
					if cancelErr != nil {
						evidence.Error = cancelErr.Error()
					}
					cancelMutex.Unlock()
				}()
			}
			events, status, requestErr := retryPreCall(item.RetryBudget, &evidence.Retries, func() ([]Event, int, error) {
				return postChat(caseCtx, client, server.URL, token, runID, step, headers)
			})
			if cancelDone != nil {
				<-cancelDone
			}
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

func newLocalServer(configPath, token string, enableObservability, captureLogs bool) (*httptest.Server, func() error, func() int, composition.RuntimeSnapshot, func(context.Context) (composition.DatabaseState, error), func() (DiagnosticLogEvidence, error), error) {
	if absolute, err := filepath.Abs(configPath); err == nil {
		configPath = absolute
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, nil, composition.RuntimeSnapshot{}, nil, nil, err
	}
	var logPath, logDir string
	if captureLogs {
		logDir, err = os.MkdirTemp("", "eval-runtime-logs-")
		if err != nil {
			return nil, nil, nil, composition.RuntimeSnapshot{}, nil, nil, err
		}
		logPath = filepath.Join(logDir, "runtime.jsonl")
		cfg.Observability.LogFile = logPath
	}
	for _, credential := range cfg.Auth.Credentials {
		credentialToken := tokenForSubject(credential.SubjectID, token)
		digest := sha256.Sum256([]byte(credentialToken))
		if os.Getenv(credential.TokenSHA256Env) == "" {
			if err := os.Setenv(credential.TokenSHA256Env, hex.EncodeToString(digest[:])); err != nil {
				_ = os.RemoveAll(logDir)
				return nil, nil, nil, composition.RuntimeSnapshot{}, nil, nil, err
			}
		}
	}
	cfg.Model.Provider = "fake"
	if !enableObservability {
		cfg.Observability.Enabled = false
		cfg.Observability.LangfuseEnabled = false
		cfg.Observability.Endpoint = ""
		cfg.Observability.LangfuseEndpoint = ""
		if !captureLogs {
			cfg.Observability.LogFile = ""
		}
		cfg.Observability.TraceFile = ""
	}
	app, err := composition.New(cfg)
	if err != nil {
		_ = os.RemoveAll(logDir)
		return nil, nil, nil, composition.RuntimeSnapshot{}, nil, nil, err
	}
	server := httptest.NewServer(httpapi.NewRouter(app.Run, app.Health, app.Authenticator))
	closeServer := func() error {
		server.Close()
		if app.Close != nil {
			return app.Close(context.Background())
		}
		return nil
	}
	databaseProbe := func(ctx context.Context) (composition.DatabaseState, error) {
		return app.DatabaseState(ctx)
	}
	var readLogs func() (DiagnosticLogEvidence, error)
	if captureLogs {
		readLogs = func() (DiagnosticLogEvidence, error) {
			defer os.RemoveAll(logDir)
			return readDiagnosticLogs(logPath)
		}
	}
	return server, closeServer, app.FakeWriteCount, app.RuntimeSnapshot(), databaseProbe, readLogs, nil
}

func databaseEvidence(state composition.DatabaseState, before, available bool) DatabaseEvidence {
	evidence := DatabaseEvidence{Available: available && state.Backend != "", Backend: state.Backend}
	if before {
		evidence.BeforeOrderCount = len(state.Orders)
		evidence.BeforeOrderDigest = digestOrders(state.Orders)
	} else {
		evidence.AfterOrderCount = len(state.Orders)
		evidence.AfterOrderDigest = digestOrders(state.Orders)
	}
	return evidence
}

func digestOrders(orders []composition.DatabaseOrder) string {
	values := make([]composition.DatabaseOrder, len(orders))
	copy(values, orders)
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i], values[j]
		if left.UserID != right.UserID {
			return left.UserID < right.UserID
		}
		if left.ProductID != right.ProductID {
			return left.ProductID < right.ProductID
		}
		if left.Quantity != right.Quantity {
			return left.Quantity < right.Quantity
		}
		return left.TotalAmount < right.TotalAmount
	})
	normalized := make([]struct {
		UserID      uint64 `json:"user_id"`
		ProductID   uint64 `json:"product_id"`
		Quantity    int64  `json:"quantity"`
		TotalAmount string `json:"total_amount"`
	}, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, struct {
			UserID      uint64 `json:"user_id"`
			ProductID   uint64 `json:"product_id"`
			Quantity    int64  `json:"quantity"`
			TotalAmount string `json:"total_amount"`
		}{value.UserID, value.ProductID, value.Quantity, value.TotalAmount})
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readDiagnosticLogs(path string) (DiagnosticLogEvidence, error) {
	if strings.TrimSpace(path) == "" {
		return DiagnosticLogEvidence{}, errors.New("diagnostic log path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return DiagnosticLogEvidence{}, err
	}
	defer file.Close()
	result := DiagnosticLogEvidence{Available: true, RedactionPassed: true}
	phases := map[string]bool{}
	errorCodes := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			return DiagnosticLogEvidence{}, errors.New("diagnostic log is not valid JSONL")
		}
		if logContainsSensitiveValue(record) {
			result.RedactionPassed = false
			return DiagnosticLogEvidence{}, errors.New("diagnostic log contains sensitive data")
		}
		result.RecordCount++
		if phase, _ := record["phase"].(string); phase != "" {
			phases[phase] = true
		}
		if code, _ := record["error_code"].(string); code != "" {
			errorCodes[code] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return DiagnosticLogEvidence{}, err
	}
	for phase := range phases {
		result.Phases = append(result.Phases, phase)
	}
	for code := range errorCodes {
		result.ErrorCodes = append(result.ErrorCodes, code)
	}
	sort.Strings(result.Phases)
	sort.Strings(result.ErrorCodes)
	return result, nil
}

func logContainsSensitiveValue(value any) bool {
	const markerList = "authorization token secret password api_key bearer /home/ /workspace/ /tmp/ /app/ /var/ /etc/"
	var walk func(any) bool
	walk = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				lowerKey := strings.ToLower(key)
				for _, marker := range strings.Fields(markerList) {
					if strings.Contains(lowerKey, marker) {
						return true
					}
				}
				if walk(nested) {
					return true
				}
			}
		case []any:
			for _, nested := range typed {
				if walk(nested) {
					return true
				}
			}
		case string:
			lower := strings.ToLower(typed)
			for _, marker := range strings.Fields(markerList) {
				if strings.Contains(lower, marker) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
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

// evidenceVerdictDigest is a verdict identity, not a request identity. The
// per-request trace ID is deliberately excluded: it is randomly generated for
// every HTTP request, so including it would make the same Case produce a
// different digest on every independent run and break the repeat-stability and
// determinism guarantees the digest exists to express. Trace correlation stays
// in Evidence for Langfuse score linking.
func evidenceVerdictDigest(evidence Evidence) string {
	type digestEvent struct {
		EventID  string            `json:"event_id"`
		RunID    string            `json:"run_id"`
		Sequence int64             `json:"sequence"`
		Type     string            `json:"type"`
		Data     map[string]string `json:"data,omitempty"`
	}
	type verdict struct {
		Events   []digestEvent  `json:"events"`
		Terminal string         `json:"terminal"`
		Result   map[string]any `json:"result,omitempty"`
	}
	events := make([]digestEvent, 0, len(evidence.Events))
	for _, event := range evidence.Events {
		events = append(events, digestEvent{EventID: event.EventID, RunID: event.RunID, Sequence: event.Sequence, Type: event.Type, Data: event.Data})
	}
	data, err := json.Marshal(verdict{Events: events, Terminal: terminalFromEvents(evidence.Events), Result: evidence.Result})
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
