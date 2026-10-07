// Package examplebusiness 集中声明模板自带的示例业务能力。
//
// 这里保存 Worker、Route、Prompt 和 Tool 绑定；它不拥有运行状态，也不绕过
// Policy Gate。新增业务先在自己的明确模块注册，再由配置选择已注册描述。
package examplebusiness

import (
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

const (
	WorkerID             = "user_analysis"
	WorkerImplementation = "fake.user_analysis"
	RunnerID             = "single_tool"
	EinoRunnerID         = "eino_adk"
	PromptFile           = "prompts/user_analysis.md"
	ReadOnlyToolID       = "user_query"
	ReadOnlyToolFake     = "fake.user_query"
	ReadOnlyToolHTTP     = "http.read_only"
	ReadOnlyToolMCP      = "mcp.read_only"
	SummaryWorkerID      = "user_summary"
	SummaryWorkerImpl    = "fake.user_summary"
	SummaryToolID        = "user_summary_query"
	SummaryToolFake      = "fake.user_summary_query"
	SideEffectToolID     = "simulated_outreach"
	SideEffectToolFake   = "fake.simulated_outreach"
	// RawExportToolID 是"上游原始导出"这类只读能力：它如实返回上游给的东西，
	// 包括上游系统自己的内部来源路径。正因如此它才会撞上 Final Guard ——
	// 这是唯一能在验收数据集里触发"敏感输出不得进入公开文本事件"的链路。
	RawExportToolID   = "raw_audience_export"
	RawExportToolFake = "fake.raw_audience_export"
	MySQLWorkerID     = "mysql_order"
	MySQLWorkerImpl   = "gorm.mysql_order"
	MySQLQueryToolID  = "mysql_order_query"
	MySQLInsertToolID = "mysql_order_insert"
	MySQLQueryImpl    = "gorm.mysql_order_query"
	MySQLInsertImpl   = "gorm.mysql_order_insert"
)

// Worker 描述一个已注册的示例 Worker 及其固定边界。
type Worker struct {
	WorkerID       string
	Implementation string
	Runner         string
	PromptFile     string
	AllowedTools   []string
	Timeout        time.Duration
}

// Route 描述一条已注册的示例业务路由。
type Route struct {
	ID       string
	WorkerID string
	Intent   string
	Matches  []string
	ToolID   string
	Risk     agent.Risk
}

var worker = Worker{
	WorkerID:       WorkerID,
	Implementation: WorkerImplementation,
	Runner:         RunnerID,
	PromptFile:     PromptFile,
	AllowedTools:   []string{ReadOnlyToolID, SideEffectToolID, RawExportToolID},
}

var summaryWorker = Worker{
	WorkerID:       SummaryWorkerID,
	Implementation: SummaryWorkerImpl,
	Runner:         RunnerID,
	PromptFile:     "prompts/user_summary.md",
	AllowedTools:   []string{SummaryToolID},
}

var mysqlWorker = Worker{
	WorkerID: MySQLWorkerID, Implementation: MySQLWorkerImpl, Runner: RunnerID,
	PromptFile: "prompts/mysql_order.md", AllowedTools: []string{MySQLQueryToolID, MySQLInsertToolID},
}

var routes = []Route{
	{ID: ReadOnlyToolID, WorkerID: WorkerID, Intent: WorkerID, Matches: []string{"分析", "查询", "分群"}, ToolID: ReadOnlyToolID, Risk: agent.RiskReadOnly},
	{ID: SummaryToolID, WorkerID: SummaryWorkerID, Intent: "summarize_user_analysis", Matches: []string{"总结", "汇总"}, ToolID: SummaryToolID, Risk: agent.RiskReadOnly},
	{ID: SideEffectToolID, WorkerID: WorkerID, Intent: WorkerID, Matches: []string{"触达", "发送", "模拟写入"}, ToolID: SideEffectToolID, Risk: agent.RiskSideEffect},
	{ID: RawExportToolID, WorkerID: WorkerID, Intent: WorkerID, Matches: []string{"原始导出", "导出原始"}, ToolID: RawExportToolID, Risk: agent.RiskReadOnly},
	{ID: MySQLQueryToolID, WorkerID: MySQLWorkerID, Intent: MySQLWorkerID, Matches: []string{"订单查询", "查询订单"}, ToolID: MySQLQueryToolID, Risk: agent.RiskReadOnly},
	{ID: MySQLInsertToolID, WorkerID: MySQLWorkerID, Intent: MySQLWorkerID, Matches: []string{"创建订单", "插入订单", "下单"}, ToolID: MySQLInsertToolID, Risk: agent.RiskSideEffect},
}

// Workers 返回注册描述副本。
func Workers() []Worker {
	value := worker
	value.AllowedTools = append([]string(nil), worker.AllowedTools...)
	second := summaryWorker
	second.AllowedTools = append([]string(nil), summaryWorker.AllowedTools...)
	third := mysqlWorker
	third.AllowedTools = append([]string(nil), mysqlWorker.AllowedTools...)
	return []Worker{value, second, third}
}

// Routes 返回注册路由副本。
func Routes() []Route {
	result := make([]Route, len(routes))
	for index, route := range routes {
		result[index] = route
		result[index].Matches = append([]string(nil), route.Matches...)
	}
	return result
}

// WorkerFor 返回指定 Worker 的注册描述。
func WorkerFor(id string) (Worker, bool) {
	switch id {
	case worker.WorkerID:
		value := worker
		value.AllowedTools = append([]string(nil), worker.AllowedTools...)
		return value, true
	case summaryWorker.WorkerID:
		value := summaryWorker
		value.AllowedTools = append([]string(nil), summaryWorker.AllowedTools...)
		return value, true
	case mysqlWorker.WorkerID:
		value := mysqlWorker
		value.AllowedTools = append([]string(nil), mysqlWorker.AllowedTools...)
		return value, true
	default:
		return Worker{}, false
	}
}

// RouteFor 返回指定 Route 的注册描述。
func RouteFor(id string) (Route, bool) {
	for _, route := range routes {
		if route.ID == id {
			route.Matches = append([]string(nil), route.Matches...)
			return route, true
		}
	}
	return Route{}, false
}

// ToolContracts 返回当前实现选择下的安全合同。
//
// Summary 是审批请求与审计记录里给人看的动作说明：审批文案属于业务注册描述，
// 因此只能在这里定义，应用层不得硬编码业务文案。
func ToolContracts(readOnlyImplementation string) []domaintool.Contract {
	if readOnlyImplementation == "" {
		readOnlyImplementation = ReadOnlyToolFake
	}
	return []domaintool.Contract{
		{ToolID: ReadOnlyToolID, Implementation: readOnlyImplementation, Risk: string(agent.RiskReadOnly), RetryLimit: 1, Summary: "只读查询示例客群", RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 1 << 20},
		{ToolID: SummaryToolID, Implementation: SummaryToolFake, Risk: string(agent.RiskReadOnly), RetryLimit: 1, Summary: "只读汇总上一步分析结果", RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 1 << 20},
		{ToolID: SideEffectToolID, Implementation: SideEffectToolFake, Risk: string(agent.RiskSideEffect), RequiresApproval: true, IdempotencyRequired: true, Summary: "模拟触达示例用户", RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 1 << 20},
		{ToolID: RawExportToolID, Implementation: RawExportToolFake, Risk: string(agent.RiskReadOnly), RetryLimit: 1, Summary: "只读导出上游原始客群片段", RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 1 << 20},
	}
}

// ToolContractsWithMySQL returns the base contracts plus optional order Tools.
func ToolContractsWithMySQL(readOnlyImplementation string) []domaintool.Contract {
	contracts := ToolContracts(readOnlyImplementation)
	return append(contracts,
		domaintool.Contract{ToolID: MySQLQueryToolID, Implementation: MySQLQueryImpl, Risk: string(agent.RiskReadOnly), RetryLimit: 1, Summary: "只读查询示例订单", RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 64 << 10},
		domaintool.Contract{ToolID: MySQLInsertToolID, Implementation: MySQLInsertImpl, Risk: string(agent.RiskSideEffect), RequiresApproval: true, IdempotencyRequired: true, Summary: "写入示例订单", RequiredInputs: []string{"message"}, MaxInputBytes: 64 << 10, MaxOutputBytes: 64 << 10},
	)
}

// ToolIDs returns all Tool IDs owned by this business module.
func ToolIDs() []string {
	return []string{ReadOnlyToolID, SummaryToolID, SideEffectToolID, RawExportToolID, MySQLQueryToolID, MySQLInsertToolID}
}

// ToolImplementations 返回指定 Tool 的已注册实现集合副本。
func ToolImplementations(toolID string) []string {
	switch toolID {
	case ReadOnlyToolID:
		return []string{ReadOnlyToolFake, ReadOnlyToolHTTP, ReadOnlyToolMCP}
	case SummaryToolID:
		return []string{SummaryToolFake}
	case SideEffectToolID:
		return []string{SideEffectToolFake}
	case RawExportToolID:
		return []string{RawExportToolFake}
	case MySQLQueryToolID:
		return []string{MySQLQueryImpl}
	case MySQLInsertToolID:
		return []string{MySQLInsertImpl}
	default:
		return nil
	}
}

// ToolImplementationRegistered 判断配置中的 Tool 实现是否属于本模块注册集合。
func ToolImplementationRegistered(toolID, implementation string) bool {
	for _, candidate := range ToolImplementations(toolID) {
		if candidate == implementation {
			return true
		}
	}
	return false
}
