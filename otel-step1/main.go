package main

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func main() {
	ctx := context.Background()

	// Exporter: where finished spans go. Here, your terminal.
	exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		log.Fatal(err)
	}

	// TracerProvider: the factory that creates spans and hands them to the exporter.
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
	defer tp.Shutdown(ctx) // flushes spans before exit
	otel.SetTracerProvider(tp)

	tracer := otel.Tracer("step1")

	// Start returns a NEW ctx that carries this span.
	ctx, span := tracer.Start(ctx, "handle-order")
	span.SetAttributes(attribute.String("order.id", "A-1001"))

	chargeCard(ctx)

	span.End()
}

func chargeCard(ctx context.Context) {
	_, span := otel.Tracer("step1").Start(ctx, "charge-card")
	defer span.End()
	time.Sleep(120 * time.Millisecond)
}
