package config

// Catalog 是启动时编译出的不可变能力快照。
// 它只描述已注册能力之间的引用，不携带可执行函数、权限绕过逻辑或运行状态。
type Catalog struct {
	Supervisor SupervisorConfig
	Workers    map[string]WorkerConfig
	Tools      map[string]ToolConfig
	Routes     map[string]RouteConfig
}

// CompileCatalog 校验配置并复制能力引用，避免运行期继续读取可变配置 map。
func (c Config) CompileCatalog() (Catalog, error) {
	if err := c.Validate(); err != nil {
		return Catalog{}, err
	}
	catalog := Catalog{
		Supervisor: c.Agent.Supervisor,
		Workers:    make(map[string]WorkerConfig, len(c.Agent.Workers)),
		Tools:      make(map[string]ToolConfig, len(c.Tools)),
		Routes:     make(map[string]RouteConfig, len(c.Agent.Routes)),
	}
	for id, worker := range c.Agent.Workers {
		worker.AllowedTools = append([]string(nil), worker.AllowedTools...)
		catalog.Workers[id] = worker
	}
	for id, tool := range c.Tools {
		catalog.Tools[id] = tool
	}
	for id, route := range c.Agent.Routes {
		route.Matches = append([]string(nil), route.Matches...)
		catalog.Routes[id] = route
	}
	return catalog, nil
}
