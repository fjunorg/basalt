package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/basalt-go/basalt/pkg/kernel/config"
	"github.com/basalt-go/basalt/pkg/kernel/log"
	"github.com/basalt-go/basalt/pkg/kernel/observ"
	"github.com/basalt-go/basalt/pkg/kernel/web"
)

// App 是模块的运行容器：持有全局配置、日志与 HTTP 路由挂载点，
// 并按依赖拓扑序驱动全部模块的 Init/Start/Stop。
type App struct {
	cfg    *config.Config
	logger *slog.Logger
	router web.Router
	reg    *Registry
}

func New(cfg *config.Config) *App {
	if cfg == nil {
		cfg = config.Defaults()
	}
	logger := log.New(cfg)
	a := &App{
		cfg:    cfg,
		logger: logger,
		router: web.NewRouter(cfg.IsProd()),
		reg:    NewRegistry(),
	}
	a.mountPlatform()
	return a
}

func (a *App) Config() *config.Config { return a.cfg }
func (a *App) Logger() *slog.Logger   { return a.logger }
func (a *App) Registry() *Registry    { return a.reg }
func (a *App) Router() web.Router     { return a.router }

// ModulesInfo 返回全部已注册模块的自描述元信息（拓扑序）。
// 供平台 API 暴露给前端做动态菜单与权限对账。
func (a *App) ModulesInfo() []ModuleInfo {
	mods, err := a.reg.Resolve()
	if err != nil {
		return nil
	}
	out := make([]ModuleInfo, 0, len(mods))
	for _, m := range mods {
		info := ModuleInfo{Name: m.Name(), Version: m.Version()}
		if d, ok := m.(Descriptor); ok {
			info = d.Describe()
		}
		out = append(out, info)
	}
	return out
}

// mountPlatform 挂载平台级自描述路由（与模块路由平级）。
func (a *App) mountPlatform() {
	a.router.Handle(http.MethodGet, "/api/v1/platform/modules", func(w http.ResponseWriter, _ *http.Request) {
		web.OK(w, a.ModulesInfo())
	})
}

// Run 解析依赖并驱动完整生命周期，阻塞直至收到退出信号。
// Init 失败即快速失败（fail-fast）；Stop 失败记录后继续，保证其余模块可退出。
func (a *App) Run(ctx context.Context) error {
	shutdown, err := observ.Setup(a.cfg)
	if err != nil {
		return fmt.Errorf("setup observability: %w", err)
	}

	mods, err := a.reg.Resolve()
	if err != nil {
		return fmt.Errorf("resolve modules: %w", err)
	}

	srv := &http.Server{
		Addr:    a.cfg.Addr,
		Handler: observ.HTTPHandler(a.router.Handler(), a.cfg.Name),
	}

	initCtx, cancelInit := context.WithTimeout(ctx, a.cfg.InitTimeout)
	defer cancelInit()
	for _, m := range mods {
		if err := m.Init(initCtx, a); err != nil {
			return fmt.Errorf("init module %s: %w", m.Name(), err)
		}
		a.logger.Info("module initialized", "module", m.Name(), "version", m.Version())
	}

	errCh := make(chan error, 1)
	go func() {
		a.logger.Info("http server listening", "addr", a.cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	for _, m := range mods {
		m := m
		go func() {
			if err := m.Start(runCtx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Error("module start failed", "module", m.Name(), "err", err)
				errCh <- fmt.Errorf("module %s: %w", m.Name(), err)
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		a.logger.Info("shutdown signal received", "signal", sig.String())
	case err := <-errCh:
		a.logger.Error("fatal error, shutting down", "err", err)
	case <-ctx.Done():
	}

	stopRun()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()
	for i := len(mods) - 1; i >= 0; i-- { // 依赖逆序停止
		if err := mods[i].Stop(shutdownCtx); err != nil {
			a.logger.Error("module stop failed", "module", mods[i].Name(), "err", err)
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	// 流量停止后再刷出缓冲的遥测数据（trace/metrics）。
	return shutdown(shutdownCtx)
}
