package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/eval/semantic"
)

// judgeServer is a local stand-in for the provider. It records every request so
// a test can assert how many calls were made and exactly what was sent, without
// ever contacting a real model.
type judgeServer struct {
	server   *httptest.Server
	mutex    sync.Mutex
	bodies   []string
	paths    []string
	status   int
	content  string
	delay    time.Duration
	requests int
	// served overrides the model name reported in the response envelope. When
	// servedSet is false the server echoes the requested model; when it is true
	// the override is emitted verbatim, including an empty (unreported) name.
	served    string
	servedSet bool
}

func newJudgeServer(t *testing.T) *judgeServer {
	t.Helper()
	fake := &judgeServer{status: http.StatusOK, content: `{"label":"relevant","reason_code":"addresses_intent"}`}
	fake.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := make([]byte, 0)
		buffer := make([]byte, 4096)
		for {
			read, err := request.Body.Read(buffer)
			body = append(body, buffer[:read]...)
			if err != nil {
				break
			}
		}
		fake.mutex.Lock()
		fake.requests++
		fake.bodies = append(fake.bodies, string(body))
		fake.paths = append(fake.paths, request.URL.Path)
		status, content, delay, served, servedSet := fake.status, fake.content, fake.delay, fake.served, fake.servedSet
		fake.mutex.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if status != http.StatusOK {
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(`{"error":{"message":"upstream"}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if !servedSet {
			var envelope struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &envelope)
			served = envelope.Model
		}
		payload, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": served,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		_, _ = writer.Write(payload)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *judgeServer) requestCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.requests
}

func (f *judgeServer) capturedBodies() []string {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return append([]string(nil), f.bodies...)
}

func (f *judgeServer) setServedModel(model string) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.served, f.servedSet = model, true
}

func (f *judgeServer) setContent(content string) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.content = content
}

func judgeTestMaterial(t *testing.T, answer string) semantic.Material {
	t.Helper()
	material := semantic.Material{
		MaterialSpec: semantic.MaterialSpec{
			SchemaVersion: semantic.MaterialSchemaVersion, SampleID: "sample-1", MaterialGroupID: "group-1",
			DatasetName: "synthetic-operations", DatasetVersion: "5",
			CaseID: "positive-audience-query", CaseVersion: "1.0.0", VerdictDigest: "hard-digest-1",
			Dimension: semantic.DimensionAnswerRelevanceValue, IntentSource: semantic.IntentSourceReviewedBusinessGoal,
			TaskIntent: "查询符合沉睡客户条件的合成客户数量", SourceApproval: semantic.SourceApprovalSynthetic,
			Applicability: semantic.ApplicabilityApplicable,
		},
		PublicAnswer: answer, Rubric: semantic.FrozenRubricIdentity(), Projection: semantic.FrozenProjectionIdentity(),
	}
	material.MaterialDigest = semantic.MaterialDigest(material)
	if err := material.Validate(); err != nil {
		t.Fatalf("fixture material: %v", err)
	}
	return material
}

func judgeTestBudget(calls, timeoutMS, maxOutput int) semantic.BudgetIdentity {
	budget := semantic.BudgetIdentity{
		MaxCalls: calls, TimeoutMS: timeoutMS,
		MaxInputBytes: semantic.MaxProjectionTotalBytes, MaxOutputBytes: maxOutput,
	}
	budget.Digest = semantic.BudgetDigest(budget)
	return budget
}

// The captured outbound body must contain the projection and nothing else from
// the material: the identity, digests and approval source are not sent.
func TestJudgeSendsExactlyTheProjectionWhitelist(t *testing.T) {
	server := newJudgeServer(t)
	t.Setenv("TEST_JUDGE_KEY", "test-credential-value")
	judge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: server.server.URL, APIKeyEnv: "TEST_JUDGE_KEY", Timeout: 5 * time.Second,
	}, 4096)
	if err != nil {
		t.Fatalf("build judge against a local server: %v", err)
	}
	material := judgeTestMaterial(t, "符合条件的合成客户共 12 位")
	budgeted := &semantic.BudgetedJudge{Judge: judge, Budget: judgeTestBudget(2, 5000, 4096)}
	invocation := budgeted.Invoke(context.Background(), material)
	if invocation.Outcome != semantic.CandidatePrediction || invocation.Response == nil {
		t.Fatalf("expected a validated prediction, got %+v", invocation)
	}
	if invocation.Response.Label != semantic.LabelRelevant {
		t.Fatalf("unexpected label: %+v", invocation.Response)
	}
	bodies := server.capturedBodies()
	if len(bodies) != 1 {
		t.Fatalf("exactly one request must be sent, got %d", len(bodies))
	}
	body := bodies[0]
	for _, required := range []string{"查询符合沉睡客户条件的合成客户数量", "符合条件的合成客户共 12 位", "answer_relevance@1"} {
		if !strings.Contains(body, required) {
			t.Fatalf("the outbound body must carry the whitelist value %q: %s", required, body)
		}
	}
	for name, forbidden := range map[string]string{
		"sample id":       material.SampleID,
		"material digest": material.MaterialDigest,
		"verdict digest":  material.VerdictDigest,
		"dataset version": material.DatasetVersion,
		"approval source": string(material.SourceApproval),
		"credential":      "test-credential-value",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the outbound body must not carry the %s (%q)", name, forbidden)
		}
	}
	// The fixed instruction is versioned and is not the evaluated agent's prompt.
	if !strings.Contains(body, JudgePromptVersion) && !strings.Contains(body, "答复切题性评分器") {
		t.Fatalf("the fixed Judge instruction must be the system message: %s", body)
	}
}

// A missing credential must fail before any request is made.
func TestJudgeConstructionWithoutCredentialMakesZeroRequests(t *testing.T) {
	server := newJudgeServer(t)
	t.Setenv("TEST_JUDGE_KEY", "")
	judge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: server.server.URL, APIKeyEnv: "TEST_JUDGE_KEY",
	}, 4096)
	if err == nil {
		t.Fatal("a missing credential must fail construction")
	}
	if judge != nil {
		t.Fatal("no judge may be assembled without a credential")
	}
	if semantic.ReasonOf(err) != semantic.ReasonCredentialMissing {
		t.Fatalf("expected credential_missing, got %v", err)
	}
	if server.requestCount() != 0 {
		t.Fatalf("zero requests expected without credentials, got %d", server.requestCount())
	}

	// A disabled judge never reaches the provider either.
	disabled := &semantic.BudgetedJudge{Judge: nil, Budget: judgeTestBudget(2, 1000, 4096)}
	if invocation := disabled.Invoke(context.Background(), judgeTestMaterial(t, "答复")); invocation.Reason != semantic.ReasonDisabled {
		t.Fatalf("expected disabled, got %+v", invocation)
	}
	if server.requestCount() != 0 {
		t.Fatalf("a disabled judge must make no request, got %d", server.requestCount())
	}
}

func TestJudgeBoundsOutputAndRejectsProviderNoise(t *testing.T) {
	server := newJudgeServer(t)
	t.Setenv("TEST_JUDGE_KEY", "test-credential-value")
	judge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: server.server.URL, APIKeyEnv: "TEST_JUDGE_KEY", Timeout: 5 * time.Second,
	}, 64)
	if err != nil {
		t.Fatal(err)
	}
	material := judgeTestMaterial(t, "答复")

	cases := map[string]struct {
		content string
		reason  semantic.ReasonCode
	}{
		"free-form text":    {`The answer looks relevant.`, semantic.ReasonInvalidOutput},
		"unknown field":     {`{"label":"relevant","reason_code":"addresses_intent","confidence":0.9}`, semantic.ReasonInvalidOutput},
		"inconsistent pair": {`{"label":"relevant","reason_code":"off_topic"}`, semantic.ReasonInvalidOutput},
		"trailing value":    {`{"label":"relevant","reason_code":"addresses_intent"}{"x":1}`, semantic.ReasonInvalidOutput},
		"oversized output":  {`{"label":"relevant","reason_code":"addresses_intent","note":"` + strings.Repeat("x", 200) + `"}`, semantic.ReasonInvalidOutput},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			server.setContent(testCase.content)
			before := server.requestCount()
			invocation := (&semantic.BudgetedJudge{Judge: judge, Budget: judgeTestBudget(2, 5000, 4096)}).
				Invoke(context.Background(), material)
			if invocation.Outcome != semantic.CandidateError || invocation.Reason != testCase.reason {
				t.Fatalf("expected %s, got %+v", testCase.reason, invocation)
			}
			if invocation.Response != nil {
				t.Fatal("an invalid response must never carry a label")
			}
			// One call per invocation: an invalid output is not retried.
			if delta := server.requestCount() - before; delta != 1 {
				t.Fatalf("expected exactly one provider call, got %d", delta)
			}
		})
	}
}

func TestJudgeDoesNotRetryProviderErrorsAndTerminatesOnTimeout(t *testing.T) {
	server := newJudgeServer(t)
	t.Setenv("TEST_JUDGE_KEY", "test-credential-value")
	server.status = http.StatusInternalServerError
	judge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: server.server.URL, APIKeyEnv: "TEST_JUDGE_KEY", Timeout: 5 * time.Second,
	}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	invocation := (&semantic.BudgetedJudge{Judge: judge, Budget: judgeTestBudget(3, 5000, 4096)}).
		Invoke(context.Background(), judgeTestMaterial(t, "答复"))
	if invocation.Outcome != semantic.CandidateError || invocation.Reason != semantic.ReasonCallError {
		t.Fatalf("expected call_error, got %+v", invocation)
	}
	if server.requestCount() != 1 {
		t.Fatalf("a provider error must not be retried, got %d requests", server.requestCount())
	}

	// A slow provider is cut off by the registered timeout and reported closed.
	slow := newJudgeServer(t)
	slow.delay = 2 * time.Second
	slowJudge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: slow.server.URL, APIKeyEnv: "TEST_JUDGE_KEY", Timeout: 30 * time.Millisecond,
	}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	timedOut := (&semantic.BudgetedJudge{Judge: slowJudge, Budget: judgeTestBudget(3, 30, 4096)}).
		Invoke(context.Background(), judgeTestMaterial(t, "答复"))
	if timedOut.Reason != semantic.ReasonTimeout && timedOut.Reason != semantic.ReasonCallError {
		t.Fatalf("expected a bounded failure, got %+v", timedOut)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("the registered timeout must bound the call, took %s", elapsed)
	}
}

// Contaminated material never reaches the provider: the projection refuses it
// first, so the outbound request count stays at zero.
func TestJudgeSendsNothingForContaminatedMaterial(t *testing.T) {
	server := newJudgeServer(t)
	t.Setenv("TEST_JUDGE_KEY", "test-credential-value")
	judge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: server.server.URL, APIKeyEnv: "TEST_JUDGE_KEY", Timeout: 5 * time.Second,
	}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	contaminated := judgeTestMaterial(t, "答复 "+semantic.SensitiveCanaryMarker)
	invocation := (&semantic.BudgetedJudge{Judge: judge, Budget: judgeTestBudget(3, 5000, 4096)}).
		Invoke(context.Background(), contaminated)
	if invocation.Reason != semantic.ReasonSensitiveInput {
		t.Fatalf("expected sensitive_input, got %+v", invocation)
	}
	if server.requestCount() != 0 {
		t.Fatalf("contaminated material must produce zero requests, got %d", server.requestCount())
	}
}

// The projection is rebuilt before every call, so two invocations of the same
// material are two independent requests carrying the same whitelist body and
// nothing extra.
func TestJudgeIsSerialAndOneRequestPerInvocation(t *testing.T) {
	server := newJudgeServer(t)
	t.Setenv("TEST_JUDGE_KEY", "test-credential-value")
	judge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: server.server.URL, APIKeyEnv: "TEST_JUDGE_KEY", Timeout: 5 * time.Second,
	}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	budgeted := &semantic.BudgetedJudge{Judge: judge, Budget: judgeTestBudget(2, 5000, 4096)}
	material := judgeTestMaterial(t, "答复")
	for index := 0; index < 2; index++ {
		if invocation := budgeted.Invoke(context.Background(), material); invocation.Outcome != semantic.CandidatePrediction {
			t.Fatalf("invocation %d: %+v", index, invocation)
		}
	}
	// The third planned invocation is refused by the budget, not sent.
	if invocation := budgeted.Invoke(context.Background(), material); invocation.Reason != semantic.ReasonBudgetExhausted {
		t.Fatalf("expected budget_exhausted, got %+v", invocation)
	}
	if server.requestCount() != 2 {
		t.Fatalf("the registered call budget must bound the requests, got %d", server.requestCount())
	}
	bodies := server.capturedBodies()
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatal("the rebuilt projection must be identical for identical material")
	}
}

// The served model is an observed fact, not an assumption: a provider that
// silently substitutes a different model than the registered scorer must be
// refused, because a different function scored the material.
func TestJudgeRefusesASilentlySubstitutedModel(t *testing.T) {
	server := newJudgeServer(t)
	t.Setenv("TEST_JUDGE_KEY", "test-credential-value")
	judge, err := NewDeepSeekJudge(context.Background(), JudgeOptions{
		Name: "deepseek-flash", BaseURL: server.server.URL, APIKeyEnv: "TEST_JUDGE_KEY", Timeout: 5 * time.Second,
	}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	budget := semantic.BudgetIdentity{
		MaxCalls: 2, TimeoutMS: 5000,
		MaxInputBytes: semantic.MaxProjectionTotalBytes, MaxOutputBytes: 4096,
	}
	invoke := func() semantic.Invocation {
		return (&semantic.BudgetedJudge{Judge: judge, Budget: budget}).Invoke(context.Background(), judgeTestMaterial(t, "答复"))
	}

	// The measured real behaviour on this account: requesting the legacy alias
	// deepseek-chat is served by deepseek-flash.
	server.setServedModel("deepseek-flash")
	if invocation := invoke(); invocation.Outcome != semantic.CandidatePrediction {
		t.Fatalf("the model that is actually registered must be accepted: %+v", invocation)
	}

	// An alias resolving to a dated snapshot of the same name is normal.
	server.setServedModel("deepseek-flash-2026-01-01")
	if invocation := invoke(); invocation.Outcome != semantic.CandidatePrediction {
		t.Fatalf("an alias to a dated snapshot of the same model must be accepted: %+v", invocation)
	}

	// A different model is a substitution and must fail closed with an identity
	// reason, never as a transport error and never as an accepted prediction.
	server.setServedModel("deepseek-v4-pro")
	substituted := invoke()
	if substituted.Outcome != semantic.CandidateError || substituted.Reason != semantic.ReasonCalibrationMismatch {
		t.Fatalf("a substituted model must be refused as calibration_mismatch, got %+v", substituted)
	}
	if substituted.Response != nil {
		t.Fatal("a substituted model must not produce a label")
	}

	// A provider that reports no model at all is accepted, because the scorer
	// identity already declares provider_revision_unverified rather than pinned.
	server.setServedModel("")
	noModel := (&semantic.BudgetedJudge{Judge: judge, Budget: budget}).Invoke(context.Background(), judgeTestMaterial(t, "答复"))
	if noModel.Reason == semantic.ReasonCalibrationMismatch {
		t.Fatalf("an unreported model is unverified, not a mismatch: %+v", noModel)
	}
}
