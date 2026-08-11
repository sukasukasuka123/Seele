package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestEventJSONContainsStableEnvelope(t *testing.T) {
	event := Event{
		Timestamp:     time.Unix(100, 0).UTC(),
		Type:          EventToolAfter,
		Phase:         PhaseAfter,
		TraceID:       "trace",
		SpanID:        "span",
		ParentSpanID:  "parent",
		CorrelationID: "call-1",
		Attributes:    Attributes{"bytes_written": 42},
		Status:        StatusError,
		Error:         &ErrorInfo{Type: "write_error", Message: "disk full"},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	for _, key := range []string{"timestamp", "type", "trace_id", "span_id", "parent_span_id", "correlation_id", "attributes", "status", "error"} {
		if _, exists := decoded[key]; !exists {
			t.Errorf("JSON envelope is missing %q", key)
		}
	}
}

func TestLifecycleHookCorrelatesIntentEffectAndMetrics(t *testing.T) {
	tracer := NewMemoryTracer()
	hook, err := NewLifecycleHook(tracer, WithStrictHookErrors())
	if err != nil {
		t.Fatalf("new hook: %v", err)
	}
	ctx, invocation, err := hook.Before(context.Background(), Action{
		Type:     EventLLMBefore,
		Name:     "completion",
		SpanKind: SpanLLM,
		Attributes: Attributes{
			AttributeGenAIRequestModel: "test-model",
		},
	})
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if err := hook.After(ctx, invocation, Effect{
		Status: StatusOK,
		Attributes: Attributes{
			AttributeGenAIUsageInput:  20,
			AttributeGenAIUsageOutput: 5,
		},
	}); err != nil {
		t.Fatalf("after: %v", err)
	}

	traceCtx, ok := TraceFromContext(ctx)
	if !ok {
		t.Fatal("hook context should propagate trace identity")
	}
	view, err := tracer.Query(context.Background(), Query{TraceID: traceCtx.TraceID})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(view.Traces) != 1 || len(view.Traces[0].Operations) != 1 {
		t.Fatalf("expected one trace and one operation, got %#v", view.Traces)
	}
	operation := view.Traces[0].Operations[0]
	if operation.Intent == nil || operation.Effect == nil {
		t.Fatalf("intent/effect pair was not correlated: %#v", operation)
	}
	if operation.Intent.CorrelationID != operation.Effect.CorrelationID {
		t.Fatal("intent and effect correlation IDs differ")
	}
	if operation.Effect.Attributes[AttributeGenAIOperationName] != "chat" {
		t.Fatalf("semantic operation = %v, want chat", operation.Effect.Attributes[AttributeGenAIOperationName])
	}
	if len(view.Audits) != 2 {
		t.Fatalf("audit count = %d, want 2", len(view.Audits))
	}
	if len(view.Metrics) != 3 {
		t.Fatalf("metric count = %d, want duration + input + output", len(view.Metrics))
	}
}

func TestDecorateCapturesIntentAndEffectWithoutBusinessDependency(t *testing.T) {
	tracer := NewMemoryTracer()
	hook, _ := NewLifecycleHook(tracer, WithStrictHookErrors())
	wantErr := errors.New("tool failed")
	handler := Decorate(
		func(_ context.Context, input string) (int, error) { return len(input), wantErr },
		hook,
		func(input string) Action {
			return Action{Type: EventToolBefore, Name: "write_file", Attributes: Attributes{"path": input}}
		},
		func(size int, err error) Effect {
			return Effect{Error: err, Attributes: Attributes{"bytes_written": size}}
		},
	)
	_, gotErr := handler(context.Background(), "result.txt")
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("decorated error = %v, want %v", gotErr, wantErr)
	}
	view, err := tracer.Query(context.Background(), Query{EventTypes: []EventType{EventToolAfter}})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(view.Events) != 1 || view.Events[0].Status != StatusError {
		t.Fatalf("tool effect event = %#v", view.Events)
	}
	if view.Events[0].Attributes["bytes_written"] != 10 {
		t.Fatalf("effect bytes = %v, want 10", view.Events[0].Attributes["bytes_written"])
	}
}

func TestLifecycleHookBestEffortDoesNotStopWork(t *testing.T) {
	hook, _ := NewLifecycleHook(failingTracer{})
	called := false
	handler := Decorate(
		func(context.Context, int) (int, error) { called = true; return 2, nil },
		hook,
		func(int) Action { return Action{Type: EventAgentStart} },
		nil,
	)
	result, err := handler(context.Background(), 1)
	if err != nil || result != 2 || !called {
		t.Fatalf("best-effort hook changed business outcome: result=%d err=%v called=%v", result, err, called)
	}
}

func TestDecorateRecordsPanicEffectAndRethrows(t *testing.T) {
	tracer := NewMemoryTracer()
	hook, _ := NewLifecycleHook(tracer, WithStrictHookErrors())
	handler := Decorate(
		func(context.Context, int) (int, error) { panic("boom") },
		hook,
		func(int) Action { return Action{Type: EventAgentStart, Name: "agent"} },
		nil,
	)
	defer func() {
		if recovered := recover(); recovered != "boom" {
			t.Fatalf("recovered = %v, want boom", recovered)
		}
		view, err := tracer.Query(context.Background(), Query{EventTypes: []EventType{EventAgentEnd}})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(view.Events) != 1 || view.Events[0].Status != StatusError {
			t.Fatalf("panic effect = %#v", view.Events)
		}
		if view.Events[0].Attributes[AttributeExceptionEscaped] != true {
			t.Fatal("panic event should carry exception.escaped=true")
		}
	}()
	_, _ = handler(context.Background(), 1)
}

func TestLifecycleHookOnError(t *testing.T) {
	tracer := NewMemoryTracer()
	hook, _ := NewLifecycleHook(tracer, WithStrictHookErrors())
	ctx, root, err := tracer.StartTrace(context.Background(), "agent", SpanAgent, nil)
	if err != nil {
		t.Fatalf("start trace: %v", err)
	}
	if err := hook.OnError(ctx, "loop", errors.New("iteration limit"), Attributes{"turn": 20}); err != nil {
		t.Fatalf("on error: %v", err)
	}
	root.End(ctx, StatusError, errors.New("iteration limit"))
	view, err := tracer.Query(context.Background(), Query{EventTypes: []EventType{EventError}})
	if err != nil || len(view.Events) != 1 {
		t.Fatalf("error events = %#v, err=%v", view.Events, err)
	}
	if view.Events[0].Error == nil || view.Events[0].Attributes["turn"] != 20 {
		t.Fatalf("error event = %#v", view.Events[0])
	}
}

func TestMemoryTracerParallelChildAgentSpans(t *testing.T) {
	tracer := NewMemoryTracer()
	ctx, root, err := tracer.StartTrace(context.Background(), "main-agent", SpanAgent, nil)
	if err != nil {
		t.Fatalf("start trace: %v", err)
	}
	const childCount = 32
	var wg sync.WaitGroup
	for i := 0; i < childCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, child, startErr := tracer.StartSpan(ctx, "subagent", SpanAgent, nil)
			if startErr != nil {
				t.Errorf("start child: %v", startErr)
				return
			}
			child.End(ctx, StatusOK, nil)
		}()
	}
	wg.Wait()
	root.End(ctx, StatusOK, nil)

	traceCtx, _ := TraceFromContext(ctx)
	view, err := tracer.Query(context.Background(), Query{TraceID: traceCtx.TraceID})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	children := view.Traces[0].Root.Children
	if len(children) != childCount {
		t.Fatalf("child spans = %d, want %d", len(children), childCount)
	}
	seen := make(map[string]struct{}, childCount)
	for _, child := range children {
		if child.ParentSpanID != root.ID() {
			t.Errorf("child parent = %s, want %s", child.ParentSpanID, root.ID())
		}
		if _, duplicate := seen[child.SpanID]; duplicate {
			t.Errorf("duplicate child span ID %s", child.SpanID)
		}
		seen[child.SpanID] = struct{}{}
	}
}

func TestMemoryTracerQueryAndStreamFilters(t *testing.T) {
	tracer := NewMemoryTracer(WithStreamBuffer(2))
	ctx, root, err := tracer.StartTrace(context.Background(), "agent", SpanAgent, nil)
	if err != nil {
		t.Fatalf("start trace: %v", err)
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription, err := tracer.Subscribe(streamCtx, Query{EventTypes: []EventType{EventToolAfter}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer subscription.Close()

	traceCtx, _ := TraceFromContext(ctx)
	for _, eventType := range []EventType{EventToolBefore, EventToolAfter} {
		event := Event{
			Type:          eventType,
			TraceID:       traceCtx.TraceID,
			SpanID:        traceCtx.SpanID,
			CorrelationID: "tool-1",
			Status:        StatusOK,
		}
		if err := tracer.Record(ctx, event); err != nil {
			t.Fatalf("record %s: %v", eventType, err)
		}
	}
	select {
	case event := <-subscription.Events():
		if event.Type != EventToolAfter {
			t.Fatalf("stream event = %s, want %s", event.Type, EventToolAfter)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream event")
	}
	root.End(ctx, StatusOK, nil)
}

func TestMemoryTracerQueryPreservesSameTimestampWriteOrder(t *testing.T) {
	tracer := NewMemoryTracer()
	ctx, root, err := tracer.StartTrace(context.Background(), "agent", SpanAgent, nil)
	if err != nil {
		t.Fatalf("start trace: %v", err)
	}
	traceCtx, _ := TraceFromContext(ctx)
	now := time.Unix(1700000000, 0).UTC()
	events := []Event{
		{Timestamp: now, Type: EventLLMBefore, Phase: PhaseBefore, Name: "llm-1", CorrelationID: "call-1", TraceID: traceCtx.TraceID, SpanID: traceCtx.SpanID},
		{Timestamp: now, Type: EventLLMAfter, Phase: PhaseAfter, Name: "llm-1", CorrelationID: "call-1", TraceID: traceCtx.TraceID, SpanID: traceCtx.SpanID},
		{Timestamp: now, Type: EventLLMBefore, Phase: PhaseBefore, Name: "llm-2", CorrelationID: "call-2", TraceID: traceCtx.TraceID, SpanID: traceCtx.SpanID},
		{Timestamp: now, Type: EventLLMAfter, Phase: PhaseAfter, Name: "llm-2", CorrelationID: "call-2", TraceID: traceCtx.TraceID, SpanID: traceCtx.SpanID},
	}
	for _, event := range events {
		if err := tracer.Record(ctx, event); err != nil {
			t.Fatalf("record %s/%s: %v", event.Type, event.CorrelationID, err)
		}
	}

	view, err := tracer.Query(context.Background(), Query{TraceID: traceCtx.TraceID})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	want := []struct {
		Type          EventType
		CorrelationID string
	}{
		{EventLLMBefore, "call-1"},
		{EventLLMAfter, "call-1"},
		{EventLLMBefore, "call-2"},
		{EventLLMAfter, "call-2"},
	}
	if len(view.Events) != len(want) {
		t.Fatalf("event count = %d, want %d: %#v", len(view.Events), len(want), view.Events)
	}
	for i, got := range view.Events {
		if got.Type != want[i].Type || got.CorrelationID != want[i].CorrelationID {
			t.Errorf("event %d = %s/%s, want %s/%s (same-timestamp events must preserve write order)", i, got.Type, got.CorrelationID, want[i].Type, want[i].CorrelationID)
		}
	}
	root.End(ctx, StatusOK, nil)
}

func TestMemoryTracerForwardsTraceMetricAndAuditSinks(t *testing.T) {
	sink := &capturingSink{}
	tracer := NewMemoryTracer(WithTraceSink(sink), WithMetricSink(sink), WithAuditSink(sink))
	hook, _ := NewLifecycleHook(tracer, WithStrictHookErrors())
	ctx, invocation, err := hook.Before(context.Background(), Action{Type: EventAgentStart, Name: "agent"})
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if err := hook.After(ctx, invocation, Effect{Status: StatusOK}); err != nil {
		t.Fatalf("after: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.traces) != 1 || len(sink.audits) != 2 || len(sink.metrics) != 1 {
		t.Fatalf("sink counts traces=%d audits=%d metrics=%d", len(sink.traces), len(sink.audits), len(sink.metrics))
	}
}

func TestOTelTracerPreservesParentAndSemanticEvents(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer, err := NewOTelTracer(provider, "telemetry-test")
	if err != nil {
		t.Fatalf("new OTel tracer: %v", err)
	}
	ctx, root, err := tracer.StartTrace(context.Background(), "agent", SpanAgent, nil)
	if err != nil {
		t.Fatalf("start root: %v", err)
	}
	childCtx, child, err := tracer.StartSpan(ctx, "completion", SpanLLM, Attributes{AttributeGenAIRequestModel: "test-model"})
	if err != nil {
		t.Fatalf("start child: %v", err)
	}
	traceCtx, _ := TraceFromContext(childCtx)
	if err := tracer.Record(childCtx, Event{
		Type:          EventLLMBefore,
		TraceID:       traceCtx.TraceID,
		SpanID:        traceCtx.SpanID,
		ParentSpanID:  traceCtx.ParentSpanID,
		CorrelationID: "llm-1",
	}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	child.End(childCtx, StatusOK, nil)
	root.End(ctx, StatusOK, nil)

	ended := recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(ended))
	}
	var childSpan sdktrace.ReadOnlySpan
	for _, span := range ended {
		if span.Name() == "completion" {
			childSpan = span
		}
	}
	if childSpan == nil {
		t.Fatal("completion span not exported")
	}
	if childSpan.Parent().SpanID().String() != root.ID() {
		t.Fatalf("OTel child parent = %s, want %s", childSpan.Parent().SpanID(), root.ID())
	}
	if childSpan.SpanKind() != oteltrace.SpanKindClient {
		t.Fatalf("OTel child kind = %s, want client", childSpan.SpanKind())
	}
	events := childSpan.Events()
	if len(events) != 1 || events[0].Name != string(EventLLMBefore) {
		t.Fatalf("OTel events = %#v", events)
	}
	if !hasOTelAttribute(events[0].Attributes, AttributeGenAIOperationName, "chat") {
		t.Fatal("OTel event is missing gen_ai.operation.name=chat")
	}
}

type failingTracer struct{}

func (failingTracer) StartTrace(context.Context, string, SpanKind, Attributes) (context.Context, Span, error) {
	return context.Background(), nil, errors.New("telemetry unavailable")
}
func (failingTracer) StartSpan(context.Context, string, SpanKind, Attributes) (context.Context, Span, error) {
	return context.Background(), nil, errors.New("telemetry unavailable")
}
func (failingTracer) Record(context.Context, Event) error { return errors.New("telemetry unavailable") }

type capturingSink struct {
	mu      sync.Mutex
	traces  []TraceSnapshot
	metrics []Metric
	audits  []AuditRecord
}

func (s *capturingSink) StoreTrace(_ context.Context, trace TraceSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces = append(s.traces, trace)
	return nil
}

func (s *capturingSink) RecordMetric(_ context.Context, metric Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = append(s.metrics, metric)
	return nil
}

func (s *capturingSink) AppendAudit(_ context.Context, audit AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, audit)
	return nil
}

func hasOTelAttribute(attributes []attribute.KeyValue, key, want string) bool {
	for _, item := range attributes {
		if string(item.Key) == key && item.Value.AsString() == want {
			return true
		}
	}
	return false
}
