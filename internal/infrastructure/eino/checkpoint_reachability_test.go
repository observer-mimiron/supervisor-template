package eino

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

// countingCheckPointStore 记录框架到底有没有往 store 里写。
//
// 这个探针回答一个会影响架构决策的问题：在我们的用法下（领域层做审批、从不产生 Eino
// 中断），ADK 会不会持久化 checkpoint？如果不会，那么"持久化 store"在今天的链路上
// 就是死代码，跨进程 resume 也无从谈起。
type countingCheckPointStore struct {
	mu      sync.Mutex
	sets    int
	gets    int
	deletes int
	inner   compose.CheckPointStore
}

func (s *countingCheckPointStore) Set(ctx context.Context, key string, value []byte) error {
	s.mu.Lock()
	s.sets++
	s.mu.Unlock()
	return s.inner.Set(ctx, key, value)
}

func (s *countingCheckPointStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	s.gets++
	s.mu.Unlock()
	return s.inner.Get(ctx, key)
}

func (s *countingCheckPointStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.deletes++
	s.mu.Unlock()
	if deleter, ok := s.inner.(interface {
		Delete(context.Context, string) error
	}); ok {
		return deleter.Delete(ctx, key)
	}
	return nil
}

func (s *countingCheckPointStore) counts() (sets, gets, deletes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sets, s.gets, s.deletes
}

// slowToolExecutor 让工具执行真正耗时，这样取消才会发生在执行中间，
// 而不是在 Run 已经跑完之后。
type slowToolExecutor struct {
	inner *captureToolExecutor
	delay time.Duration
}

func (s *slowToolExecutor) Execute(ctx context.Context, toolID string, input map[string]string, key string) (string, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return s.inner.Execute(ctx, toolID, input, key)
}

func newCountingRunner(t *testing.T) (*Runner, *countingCheckPointStore, *captureToolExecutor) {
	t.Helper()
	executor := &captureToolExecutor{}
	store := &countingCheckPointStore{inner: NewMemoryCheckpointStore()}
	runner, err := NewAgentRunner(context.Background(), &deterministicToolCallingModel{}, store,
		[]domaintool.Contract{{ToolID: "user_query", Risk: "read_only", RequiredInputs: []string{"message"}}},
		executor, captureToolValidator{}, "")
	if err != nil {
		t.Fatal(err)
	}
	return runner, store, executor
}

func einoStepRequest() application.WorkerRequest {
	return application.WorkerRequest{
		RunID: "run-probe", WorkerID: "worker", Intent: "query", ToolID: "user_query",
		Input: map[string]string{"message": "hello"}, IdempotencyKey: "run-probe:step-1",
		Step: agent.PlanStep{StepID: "step-1", WorkerID: "worker", Intent: "query", ToolID: "user_query",
			Input: map[string]string{"message": "hello"}, Status: agent.StepRunning, IdempotencyKey: "run-probe:step-1"},
	}
}

// TestADKNeverPersistsCheckpointOnHappyPath 记录事实：一次成功的 Worker 步骤
// 不会往 CheckPointStore 写任何东西。Eino 只在取消/中断/非空闲 Stop 时写。
func TestADKNeverPersistsCheckpointOnHappyPath(t *testing.T) {
	runner, store, executor := newCountingRunner(t)
	if _, err := runner.Run(context.Background(), einoStepRequest()); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("fixture did not execute the tool, calls=%d", executor.calls)
	}
	sets, _, _ := store.counts()
	if sets != 0 {
		t.Fatalf("happy path wrote %d checkpoints; the probe's premise is wrong", sets)
	}
	t.Log("FACT: successful step writes 0 checkpoints")
}

// TestADKNeverPersistsCheckpointOnContextCancel 是最关键的一条：我们运行时的取消是
// 普通 context cancel（internal/application/run 用 context.WithCancel），
// 不经过 Eino 的 interrupt 机制。ADK 只在 cancelErr.interruptSignal != nil 时保存，
// 因此这条路径同样不落盘。
//
// 结论：当前架构下 ADK checkpoint 永远为空，"跨进程 Eino resume" 无从发生。
// 这个断言如果哪天变红，说明我们引入了 Eino 中断，届时应重新评估持久化 store 的价值。
func TestADKNeverPersistsCheckpointOnContextCancel(t *testing.T) {
	executor := &captureToolExecutor{}
	store := &countingCheckPointStore{inner: NewMemoryCheckpointStore()}
	runner, err := NewAgentRunner(context.Background(), &deterministicToolCallingModel{}, store,
		[]domaintool.Contract{{ToolID: "user_query", Risk: "read_only", RequiredInputs: []string{"message"}}},
		&slowToolExecutor{inner: executor, delay: 400 * time.Millisecond}, captureToolValidator{}, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond) // 工具要跑 400ms，取消必定落在执行中间
		cancel()
	}()
	start := time.Now()
	_, runErr := runner.Run(ctx, einoStepRequest())
	elapsed := time.Since(start)

	// 先证明取消真的发生了：否则这条测试和 happy path 没区别。
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("cancel did not interrupt the run (elapsed=%s); the probe is meaningless", elapsed)
	}
	if runErr == nil {
		t.Fatalf("cancelled run should not return nil error (elapsed=%s)", elapsed)
	}

	sets, _, _ := store.counts()
	if sets != 0 {
		t.Fatalf("context cancel wrote %d checkpoints (err=%v); our cancel path now reaches Eino persistence", sets, runErr)
	}
	t.Logf("FACT: cancel mid-execution wrote 0 checkpoints (elapsed=%s err=%v)", elapsed, runErr)
}

// TestADKDoesNotTouchCheckpointStoreOnFreshRun 是最强的一条：即使 store 里已经预置了
// 一条 state，一次全新的 Run 也**既不读也不写也不删**。
//
// 三者叠加（happy path 不写、取消不写、新 Run 不读）说明：在我们当前的用法下，
// ADK 的 CheckPointStore 是一个**完全不被触碰**的依赖。持久化它不会带来任何行为差异。
func TestADKDoesNotTouchCheckpointStoreOnFreshRun(t *testing.T) {
	runner, store, _ := newCountingRunner(t)
	// 预置一条 checkpoint，模拟"上一次跑留下过状态"。若框架真的会读取，就该命中它。
	if err := store.inner.Set(context.Background(), "run-probe:step-1", []byte("stale-state")); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), einoStepRequest()); err != nil {
		t.Fatal(err)
	}
	sets, gets, deletes := store.counts()
	if sets != 0 || gets != 0 || deletes != 0 {
		t.Fatalf("a fresh run touched the store: sets=%d gets=%d deletes=%d", sets, gets, deletes)
	}
	t.Log("FACT: fresh run leaves CheckPointStore completely untouched (sets=0 gets=0 deletes=0)")
}
