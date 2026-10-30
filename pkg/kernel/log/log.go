// Package log 基于标准库 slog 封装框架日志。
//
// 选型说明（见 docs/01-architecture.md L0 层）：信创环境坚持零额外依赖，
// 生产环境输出 JSON 便于采集，开发环境输出人类可读文本。
package log

import (
	"io"
	"log/slog"
	"os"

	"github.com/basalt-go/basalt/pkg/kernel/config"
)

func New(cfg *config.Config) *slog.Logger {
	var w io.Writer = os.Stdout
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if cfg.IsProd() {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	opts.Level = slog.LevelDebug
	return slog.New(slog.NewTextHandler(w, opts))
}
