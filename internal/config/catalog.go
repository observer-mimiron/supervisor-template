package config

import (
	"fmt"
	"strings"
)

// Catalog 是历史兼容的启动配置快照。
type Catalog struct {
	Supervisor SupervisorConfig
	Workers    map[string]WorkerConfig
	Tools      map[string]ToolConfig
	Routes     map[string]RouteConfig
}

// RuntimeCatalog 是启动后供运行时读取的冻结能力目录。
// 字段保持私有，访问器返回副本，调用方不能修改目录内部状态。
type RuntimeCatalog struct {
	supervisor SupervisorConfig
	workers    map[string]WorkerConfig
	tools      map[string]ToolConfig
	routes     map[string]RouteConfig
	limits     LimitsConfig
}

func (c RuntimeCatalog) Supervisor() SupervisorConfig     { return cloneSupervisor(c.supervisor) }
func (c RuntimeCatalog) Workers() map[string]WorkerConfig { return cloneWorkers(c.workers) }
func (c RuntimeCatalog) Tools() map[string]ToolConfig     { return cloneTools(c.tools) }
func (c RuntimeCatalog) Routes() map[string]RouteConfig   { return cloneRoutes(c.routes) }
func (c RuntimeCatalog) Limits() LimitsConfig             { return c.limits }

// CompileRuntimeCatalog 校验并编译所有启动引用；编译后运行时不再读取 Config map。
func (c Config) CompileRuntimeCatalog() (RuntimeCatalog, error) {
	if err := c.Validate(); err != nil {
		return RuntimeCatalog{}, err
	}
	if c.Agent.Supervisor.Enabled && strings.TrimSpace(c.Agent.Supervisor.PromptFile) == "" {
		return RuntimeCatalog{}, fmt.Errorf("Supervisor Prompt 未注册")
	}
	for workerID, worker := range c.Agent.Workers {
		if !worker.Enabled {
			continue
		}
		if strings.TrimSpace(worker.PromptFile) == "" {
			return RuntimeCatalog{}, fmt.Errorf("Worker %q Prompt 未注册", workerID)
		}
		if strings.TrimSpace(worker.Runner) == "" {
			return RuntimeCatalog{}, fmt.Errorf("Worker %q Runner 未注册", workerID)
		}
	}
	// File existence is a startup concern for the actual process, but tests and
	// embedders may use an in-memory prompt loader, so only reject empty refs here.
	return RuntimeCatalog{
		supervisor: cloneSupervisor(c.Agent.Supervisor),
		workers:    cloneWorkers(c.Agent.Workers),
		tools:      cloneTools(c.Tools),
		routes:     cloneRoutes(c.Agent.Routes),
		limits:     c.Limits,
	}, nil
}

// CompileCatalog 校验配置并复制能力引用，避免运行期继续读取可变配置 map。
func (c Config) CompileCatalog() (Catalog, error) {
	if err := c.Validate(); err != nil {
		return Catalog{}, err
	}
	return Catalog{
		Supervisor: cloneSupervisor(c.Agent.Supervisor),
		Workers:    cloneWorkers(c.Agent.Workers),
		Tools:      cloneTools(c.Tools),
		Routes:     cloneRoutes(c.Agent.Routes),
	}, nil
}

func cloneSupervisor(value SupervisorConfig) SupervisorConfig {
	value.AllowedWorkers = append([]string(nil), value.AllowedWorkers...)
	return value
}

func cloneWorkers(values map[string]WorkerConfig) map[string]WorkerConfig {
	cloned := make(map[string]WorkerConfig, len(values))
	for id, value := range values {
		value.AllowedTools = append([]string(nil), value.AllowedTools...)
		cloned[id] = value
	}
	return cloned
}

func cloneTools(values map[string]ToolConfig) map[string]ToolConfig {
	cloned := make(map[string]ToolConfig, len(values))
	for id, value := range values {
		value.MCPServer = strings.TrimSpace(value.MCPServer)
		cloned[id] = value
	}
	return cloned
}

func cloneRoutes(values map[string]RouteConfig) map[string]RouteConfig {
	cloned := make(map[string]RouteConfig, len(values))
	for id, value := range values {
		value.Matches = append([]string(nil), value.Matches...)
		cloned[id] = value
	}
	return cloned
}
