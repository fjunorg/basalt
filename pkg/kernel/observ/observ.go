// Package observ 提供内核可观测性：OpenTelemetry trace/metrics 与 OTLP 导出。
//
// 设计：未配置 BASALT_OTEL_ENDPOINT 时全部为 no-op（零开销、零依赖外部
// 服务），信创内网/私有化盒子开箱即用；配置后经 OTLP/HTTP 导出至
// 任意兼容后端（Jaeger/Tempo/SkyWalking/云厂商）。
package observ

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"

	"github.com/basalt-go/basalt/pkg/kernel/config"
)

// Shutdown 停止导出器并刷出缓冲中的遥测数据。
type Shutdown func(ctx context.Context) error

// Setup 初始化全局 TracerProvider 与 MeterProvider。
// endpoint 为空返回 no-op Shutdown；调用方必须 defer 执行以刷出数据。
func Setup(cfg *config.Config) (Shutdown, error) {
	if cfg.OtelEndpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.Name),
			semconv.DeploymentEnvironmentName(cfg.Env),
		))
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(cfg.OtelEndpoint))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)

	metricExp, err := otlpmetrichttp.New(context.Background(),
		otlpmetrichttp.WithEndpointURL(cfg.OtelEndpoint))
	if err != nil {
		_ = tp.Shutdown(context.Background())
		return nil, err
	}
	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExp,
			metric.WithInterval(30*time.Second))),
		metric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	return func(ctx context.Context) error {
		errT := tp.Shutdown(ctx)
		errM := mp.Shutdown(ctx)
		if errT != nil {
			return errT
		}
		return errM
	}, nil
}

// HTTPHandler 为整棵路由树包装 otelhttp：自动 span（http.server）+
// 请求指标（时长/计数）。healthz 等探针路径跳过采样以降低噪音。
func HTTPHandler(h http.Handler, service string) http.Handler {
	return otelhttp.NewHandler(h, service,
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}))
}
