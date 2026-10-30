package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

// newNewCmd 生成脚手架。当前支持 module；后续扩展 service/crud 等形态。
func newNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Generate scaffolding",
	}
	cmd.AddCommand(newNewModuleCmd())
	return cmd
}

func newNewModuleCmd() *cobra.Command {
	var deps []string
	var rootDir string
	cmd := &cobra.Command{
		Use:   "module <name>",
		Short: "Generate a pluggable module skeleton under pkg/modules/<name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			if !isValidModName(name) {
				return fmt.Errorf("invalid module name %q: lowercase letters, digits and hyphens only", name)
			}
			dir := filepath.Join(rootDir, "pkg", "modules", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			file := filepath.Join(dir, name+".go")
			if _, err := os.Stat(file); err == nil {
				return fmt.Errorf("refusing to overwrite existing %s", file)
			}
			f, err := os.Create(file)
			if err != nil {
				return err
			}
			defer f.Close()
			pkg := strings.ReplaceAll(name, "-", "")
			return modTmpl.Execute(f, struct {
				Name, Pkg string
				Deps      []string
			}{name, pkg, deps})
		},
	}
	cmd.Flags().StringSliceVar(&deps, "deps", nil, "module dependencies (comma-separated, e.g. identity,tenant)")
	cmd.Flags().StringVar(&rootDir, "root", ".", "repository root directory")
	return cmd
}

var modTmpl = template.Must(template.New("module").Parse(`// Package {{.Pkg}} 是 TODO 模块说明。
package {{.Pkg}}

import (
	"context"
	"net/http"

	"github.com/basalt-go/basalt/pkg/kernel/app"
	"github.com/basalt-go/basalt/pkg/kernel/web"
)

const Version = "0.1.0"

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Name() string    { return "{{.Name}}" }
func (*Module) Version() string { return Version }
func (*Module) Dependencies() []string {
	return []string{ {{- range .Deps}}"{{.}}",{{- end}} }
}

func (m *Module) Init(_ context.Context, a *app.App) error {
	a.Router().Handle(http.MethodGet, "/api/v1/{{.Name}}/info", func(w http.ResponseWriter, _ *http.Request) {
		web.OK(w, map[string]any{"module": m.Name(), "version": Version})
	})
	return nil
}

func (*Module) Start(context.Context) error { return nil }
func (*Module) Stop(context.Context) error  { return nil }

var _ app.Module = (*Module)(nil)
`))

func isValidModName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
