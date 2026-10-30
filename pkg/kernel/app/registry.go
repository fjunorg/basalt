package app

import (
	"fmt"
	"sort"
	"strings"
)

// Registry 维护模块注册表，并在启动前解析依赖拓扑序。
// Registry 非并发安全：模块注册发生在进程启动的单一阶段。
type Registry struct {
	modules map[string]Module
	order   []string // 已注册顺序，用于稳定报错输出
}

func NewRegistry() *Registry {
	return &Registry{modules: make(map[string]Module)}
}

// Register 登记一个模块，重名视为注册方编程错误。
func (r *Registry) Register(m Module) error {
	name := m.Name()
	if name == "" {
		return fmt.Errorf("app: module name must not be empty (%T)", m)
	}
	if _, dup := r.modules[name]; dup {
		return fmt.Errorf("app: duplicate module registration %q", name)
	}
	r.modules[name] = m
	r.order = append(r.order, name)
	return nil
}

// Get 按名称取模块，供跨模块接口协作时做类型断言。
func (r *Registry) Get(name string) (Module, bool) {
	m, ok := r.modules[name]
	return m, ok
}

// MustGet 同 Get，缺失时 panic——只能用于声明过依赖的场景，
// Registry 已保证被依赖模块存在且先完成 Init。
func (r *Registry) MustGet(name string) Module {
	m, ok := r.modules[name]
	if !ok {
		panic(fmt.Sprintf("app: module %q not registered", name))
	}
	return m
}

// Resolve 返回依赖拓扑序的模块列表（Kahn 算法）。
// 缺失依赖与循环依赖均返回错误，错误信息包含完整环路以便定位。
func (r *Registry) Resolve() ([]Module, error) {
	indeg := make(map[string]int, len(r.modules))
	adj := make(map[string][]string, len(r.modules)) // dep -> 依赖它的模块
	for _, name := range r.order {
		m := r.modules[name]
		deps := m.Dependencies()
		seen := make(map[string]bool, len(deps))
		for _, d := range deps {
			if d == name {
				return nil, fmt.Errorf("app: module %q depends on itself", name)
			}
			if seen[d] {
				return nil, fmt.Errorf("app: module %q declares duplicate dependency %q", name, d)
			}
			seen[d] = true
			if _, ok := r.modules[d]; !ok {
				return nil, fmt.Errorf("app: module %q depends on unregistered module %q", name, d)
			}
			adj[d] = append(adj[d], name)
			indeg[name]++
		}
	}

	var ready []string
	for _, name := range r.order { // 按 order 保底顺序挑选，保证拓扑序稳定
		if indeg[name] == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready) // 稳定输出：同名级按字典序，避免非确定性构建

	out := make([]Module, 0, len(r.modules))
	for len(ready) > 0 {
		cur := ready[0]
		ready = ready[1:]
		out = append(out, r.modules[cur])
		for _, next := range adj[cur] {
			indeg[next]--
			if indeg[next] == 0 {
				ready = append([]string{next}, ready...)
				sort.Strings(ready)
			}
		}
	}
	if len(out) != len(r.modules) {
		var cyclic []string
		for _, name := range r.order {
			if indeg[name] > 0 {
				cyclic = append(cyclic, name)
			}
		}
		return nil, fmt.Errorf("app: dependency cycle involving: %s", strings.Join(cyclic, ", "))
	}
	return out, nil
}
