package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	promexp "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func setup(service string) {
	exp, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint("localhost:4317"),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		log.Fatal(err)
	}
	res := resource.NewSchemaless(attribute.String("service.name", service))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp), // batching is what you use in production
		sdktrace.WithResource(res),
	)
	// Register globally so otelhttp and otel.Tracer() use this provider.
	otel.SetTracerProvider(tp)
	// Without a propagator, no traceparent header is injected/extracted.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
}

func runA() {
	setup("service-a")
	client := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}

	mux := http.NewServeMux()
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		// r.Context() already holds A's server span (from otelhttp.NewHandler).
		// Pass r.Context() so the client span is a child of it and the
		// injected traceparent continues the same trace into B.
		req, _ := http.NewRequestWithContext(r.Context(), "GET", "http://127.0.0.1:8081/charge", nil)
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			http.Error(w, "order failed: "+string(body), http.StatusBadGateway)
			return
		}
		fmt.Fprintf(w, "order placed; B said: %s", body)
	})
	log.Println("A listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", otelhttp.NewHandler(mux, "A /order")))
}

func runB() {
	setup("service-b")
	failed := setupMetrics("service-b")

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/charge", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("b").Start(r.Context(), "db-write")
		defer span.End()

		delay := 80 * time.Millisecond
		if rand.Intn(5) == 0 {
			delay = 900 * time.Millisecond
			span.SetAttributes(attribute.Bool("db.slow_query", true))
		}
		time.Sleep(delay)

		if rand.Intn(10) == 0 {
			err := errors.New("deadlock detected on table payments")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			failed.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "deadlock")))
			logCtx(ctx, "payment failed", "err", err)
			http.Error(w, "payment failed", http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, "charged")
	})
	log.Println("B listening on 127.0.0.1:8081")
	log.Fatal(http.ListenAndServe("127.0.0.1:8081", otelhttp.NewHandler(mux, "B /charge")))
}

// Metrics: an OTel MeterProvider whose reader is a Prometheus exporter.
func setupMetrics(service string) metric.Int64Counter {
	exp, err := promexp.New()
	if err != nil {
		log.Fatal(err)
	}
	res := resource.NewSchemaless(attribute.String("service.name", service))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp), sdkmetric.WithResource(res))
	otel.SetMeterProvider(mp)

	c, err := otel.Meter("b").Int64Counter("payments.failed",
		metric.WithDescription("Failed payment attempts"))
	if err != nil {
		log.Fatal(err)
	}
	return c
}

// Logs: plain slog, plus the IDs that link this line to its trace.
func logCtx(ctx context.Context, msg string, args ...any) {
	sc := trace.SpanContextFromContext(ctx)
	args = append(args, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	slog.ErrorContext(ctx, msg, args...)
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: go run . a|b")
	}
	switch os.Args[1] {
	case "a":
		runA()
	case "b":
		runB()
	}
}
