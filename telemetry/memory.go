package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const defaultStreamBuffer = 64

// MemoryOption configures MemoryTracer dependencies.
type MemoryOption func(*MemoryTracer)

// WithTraceSink adds a completed-trace sink.
func WithTraceSink(sink TraceSink) MemoryOption {
	return func(tracer *MemoryTracer) {
		if sink != nil {
			tracer.traceSinks = append(tracer.traceSinks, sink)
		}
	}
}

// WithMetricSink adds a metrics sink.
func WithMetricSink(sink MetricSink) MemoryOption {
	return func(tracer *MemoryTracer) {
		if sink != nil {
			tracer.metricSinks = append(tracer.metricSinks, sink)
		}
	}
}

// WithAuditSink adds an audit sink.
func WithAuditSink(sink AuditSink) MemoryOption {
	return func(tracer *MemoryTracer) {
		if sink != nil {
			tracer.auditSinks = append(tracer.auditSinks, sink)
		}
	}
}

// WithClock injects a deterministic clock for tests or replay systems.
func WithClock(clock func() time.Time) MemoryOption {
	return func(tracer *MemoryTracer) {
		if clock != nil {
			tracer.clock = clock
		}
	}
}

// WithStreamBuffer configures the bounded per-subscriber event channel.
func WithStreamBuffer(size int) MemoryOption {
	return func(tracer *MemoryTracer) {
		if size > 0 {
			tracer.streamBuffer = size
		}
	}
}

// MemoryTracer is a concurrent in-memory Trace/Metric/Audit implementation.
type MemoryTracer struct {
	mu           sync.RWMutex
	traces       map[string]*memoryTrace
	metrics      []Metric
	audits       []AuditRecord
	auditSeq     uint64
	subSeq       uint64
	subscribers  map[uint64]*memorySubscription
	clock        func() time.Time
	streamBuffer int
	traceSinks   []TraceSink
	metricSinks  []MetricSink
	auditSinks   []AuditSink
}

type memoryTrace struct {
	rootID     string
	spans      map[string]*memorySpanState
	operations map[string]*Operation
}

type memorySpanState struct {
	traceID    string
	id         string
	parentID   string
	name       string
	kind       SpanKind
	startedAt  time.Time
	endedAt    time.Time
	status     Status
	err        *ErrorInfo
	attributes Attributes
	events     []Event
	children   []string
}

// NewMemoryTracer constructs an isolated tracer without global state.
func NewMemoryTracer(options ...MemoryOption) *MemoryTracer {
	tracer := &MemoryTracer{
		traces:       make(map[string]*memoryTrace),
		subscribers:  make(map[uint64]*memorySubscription),
		clock:        time.Now,
		streamBuffer: defaultStreamBuffer,
	}
	for _, option := range options {
		if option != nil {
			option(tracer)
		}
	}
	return tracer
}

// StartTrace creates a globally unique root span.
func (t *MemoryTracer) StartTrace(ctx context.Context, name string, kind SpanKind, attributes Attributes) (context.Context, Span, error) {
	traceID, err := newIdentifier(16)
	if err != nil {
		return ctx, nil, err
	}
	spanID, err := newIdentifier(8)
	if err != nil {
		return ctx, nil, err
	}
	if kind == "" {
		kind = SpanInternal
	}
	state := &memorySpanState{
		traceID:    traceID,
		id:         spanID,
		name:       name,
		kind:       kind,
		startedAt:  t.clock(),
		status:     StatusUnset,
		attributes: cloneAttributes(attributes),
	}
	t.mu.Lock()
	t.traces[traceID] = &memoryTrace{
		rootID:     spanID,
		spans:      map[string]*memorySpanState{spanID: state},
		operations: make(map[string]*Operation),
	}
	t.mu.Unlock()
	traceCtx := TraceContext{TraceID: traceID, SpanID: spanID}
	ctx = ContextWithTrace(ctx, traceCtx)
	return ctx, &memorySpan{tracer: t, traceID: traceID, spanID: spanID}, nil
}

// StartSpan creates a child of the span propagated in ctx.
func (t *MemoryTracer) StartSpan(ctx context.Context, name string, kind SpanKind, attributes Attributes) (context.Context, Span, error) {
	parent, ok := TraceFromContext(ctx)
	if !ok {
		return ctx, nil, errors.New("start span: trace context is missing")
	}
	spanID, err := newIdentifier(8)
	if err != nil {
		return ctx, nil, err
	}
	if kind == "" {
		kind = SpanInternal
	}
	state := &memorySpanState{
		traceID:    parent.TraceID,
		id:         spanID,
		parentID:   parent.SpanID,
		name:       name,
		kind:       kind,
		startedAt:  t.clock(),
		status:     StatusUnset,
		attributes: cloneAttributes(attributes),
	}
	t.mu.Lock()
	trace, exists := t.traces[parent.TraceID]
	if !exists {
		t.mu.Unlock()
		return ctx, nil, fmt.Errorf("start span: trace %q is not registered", parent.TraceID)
	}
	parentState, exists := trace.spans[parent.SpanID]
	if !exists {
		t.mu.Unlock()
		return ctx, nil, fmt.Errorf("start span: parent span %q is not registered", parent.SpanID)
	}
	trace.spans[spanID] = state
	parentState.children = append(parentState.children, spanID)
	t.mu.Unlock()
	traceCtx := TraceContext{TraceID: parent.TraceID, SpanID: spanID, ParentSpanID: parent.SpanID}
	ctx = ContextWithTrace(ctx, traceCtx)
	return ctx, &memorySpan{tracer: t, traceID: parent.TraceID, spanID: spanID}, nil
}

// Record stores, correlates, audits, and streams a lifecycle event.
func (t *MemoryTracer) Record(ctx context.Context, event Event) error {
	if traceCtx, ok := TraceFromContext(ctx); ok {
		if event.TraceID == "" {
			event.TraceID = traceCtx.TraceID
		}
		if event.SpanID == "" {
			event.SpanID = traceCtx.SpanID
		}
		if event.ParentSpanID == "" {
			event.ParentSpanID = traceCtx.ParentSpanID
		}
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = t.clock()
	}
	if event.Phase == "" {
		event.Phase = phaseForEvent(event.Type)
	}
	if event.Status == "" {
		event.Status = StatusUnset
	}
	event = withSemanticDefaults(event)
	if err := event.Validate(); err != nil {
		return fmt.Errorf("record telemetry event: %w", err)
	}

	var audit AuditRecord
	var subscribers []*memorySubscription
	t.mu.Lock()
	trace, exists := t.traces[event.TraceID]
	if !exists {
		t.mu.Unlock()
		return fmt.Errorf("record telemetry event: trace %q is not registered", event.TraceID)
	}
	span, exists := trace.spans[event.SpanID]
	if !exists {
		t.mu.Unlock()
		return fmt.Errorf("record telemetry event: span %q is not registered", event.SpanID)
	}
	stored := cloneEvent(event)
	span.events = append(span.events, stored)
	if event.CorrelationID != "" {
		operation := trace.operations[event.CorrelationID]
		if operation == nil {
			operation = &Operation{CorrelationID: event.CorrelationID, Status: StatusUnset}
			trace.operations[event.CorrelationID] = operation
		}
		copyForOperation := cloneEvent(stored)
		switch event.Phase {
		case PhaseBefore:
			operation.Intent = &copyForOperation
		case PhaseAfter:
			operation.Effect = &copyForOperation
			operation.Status = event.Status
		}
	}
	t.auditSeq++
	audit = AuditRecord{Sequence: t.auditSeq, Event: cloneEvent(stored)}
	t.audits = append(t.audits, audit)
	for _, subscriber := range t.subscribers {
		if matchesEvent(event, subscriber.query) {
			subscribers = append(subscribers, subscriber)
		}
	}
	for _, subscriber := range subscribers {
		select {
		case subscriber.events <- cloneEvent(stored):
		default:
			subscriber.dropped.Add(1)
		}
	}
	auditSinks := append([]AuditSink(nil), t.auditSinks...)
	t.mu.Unlock()

	var sinkErrs []error
	for _, sink := range auditSinks {
		if err := sink.AppendAudit(ctx, audit); err != nil {
			sinkErrs = append(sinkErrs, err)
		}
	}
	for _, metric := range metricsFromEvent(event) {
		if err := t.RecordMetric(ctx, metric); err != nil {
			sinkErrs = append(sinkErrs, err)
		}
	}
	return errors.Join(sinkErrs...)
}

// RecordMetric stores a metric and forwards it to configured sinks.
func (t *MemoryTracer) RecordMetric(ctx context.Context, metric Metric) error {
	if metric.Timestamp.IsZero() {
		metric.Timestamp = t.clock()
	}
	metric.Attributes = cloneAttributes(metric.Attributes)
	t.mu.Lock()
	t.metrics = append(t.metrics, metric)
	sinks := append([]MetricSink(nil), t.metricSinks...)
	t.mu.Unlock()
	var errs []error
	for _, sink := range sinks {
		if err := sink.RecordMetric(ctx, metric); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Query returns immutable projections for waterfall, status, metrics, and audit views.
func (t *MemoryTracer) Query(ctx context.Context, query Query) (ViewModel, error) {
	if err := ctx.Err(); err != nil {
		return ViewModel{}, err
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	view := ViewModel{}
	traceIDs := make([]string, 0, len(t.traces))
	for traceID := range t.traces {
		if query.TraceID == "" || query.TraceID == traceID {
			traceIDs = append(traceIDs, traceID)
		}
	}
	sort.Strings(traceIDs)
	for _, traceID := range traceIDs {
		trace := t.traces[traceID]
		view.Traces = append(view.Traces, snapshotTrace(traceID, trace))
		for _, span := range trace.spans {
			for _, event := range span.events {
				if matchesEvent(event, query) {
					view.Events = append(view.Events, cloneEvent(event))
				}
			}
		}
	}
	sort.SliceStable(view.Events, func(i, j int) bool { return view.Events[i].Timestamp.Before(view.Events[j].Timestamp) })
	for _, metric := range t.metrics {
		if matchesMetric(metric, query) {
			metric.Attributes = cloneAttributes(metric.Attributes)
			view.Metrics = append(view.Metrics, metric)
		}
	}
	for _, audit := range t.audits {
		if matchesEvent(audit.Event, query) {
			audit.Event = cloneEvent(audit.Event)
			view.Audits = append(view.Audits, audit)
		}
	}
	applyLimit(&view, query.Limit)
	return view, nil
}

// Subscribe returns a bounded filtered real-time event stream.
func (t *MemoryTracer) Subscribe(ctx context.Context, query Query) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.subSeq++
	sub := &memorySubscription{
		owner:  t,
		id:     t.subSeq,
		query:  query,
		events: make(chan Event, t.streamBuffer),
	}
	t.subscribers[sub.id] = sub
	t.mu.Unlock()
	go func() {
		<-ctx.Done()
		sub.Close()
	}()
	return sub, nil
}

var (
	_ Tracer         = (*MemoryTracer)(nil)
	_ MetricRecorder = (*MemoryTracer)(nil)
	_ Queryer        = (*MemoryTracer)(nil)
	_ Streamer       = (*MemoryTracer)(nil)
)
