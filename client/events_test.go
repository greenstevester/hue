package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventStreamCloseCancelsQuietConnectionAndCanBeRepeated(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	c := NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	c.httpClient.Timeout = 10 * time.Millisecond
	stream, err := c.StreamEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.Connected():
	case <-time.After(time.Second):
		t.Fatal("no connection")
	}
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() { defer group.Done(); stream.Close() }()
	}
	group.Wait()
	select {
	case _, ok := <-stream.Events():
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(time.Second):
		t.Fatal("quiet read did not cancel")
	}
}

func TestEventStreamFiltersResourceTypeAndReportsOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &EventStream{events: make(chan Event, 1), errors: make(chan error, 2), ctx: ctx, cancel: cancel}
	stream.processEvent(`[{"type":"update","data":[{"type":"light","id":"one"}]}]`)
	filtered := stream.FilterEvents("light")
	select {
	case event := <-filtered:
		if event.Data[0].ID != "one" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("light update filtered out")
	}
	stream.processEvent(`not json`)
	select {
	case <-stream.Errors():
	default:
		t.Fatal("parse error hidden")
	}
	cancel()
	overflow := &EventStream{events: make(chan Event, 1), errors: make(chan error, 2)}
	overflow.processEvent(`[{"type":"update"},{"type":"update"}]`)
	select {
	case err := <-overflow.Errors():
		if !strings.Contains(err.Error(), "overflow") {
			t.Fatal(err)
		}
	default:
		t.Fatal("overflow hidden")
	}
}

func TestWriteChecksBridgeErrorsAndGuardPrecedesRequest(t *testing.T) {
	received := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		fmt.Fprint(w, `{"errors":[{"description":"not supported"}],"data":[]}`)
	}))
	defer server.Close()
	c := NewClient("unused", "test", server.Client())
	c.baseURL = server.URL
	err := c.UpdateLight(context.Background(), "k", LightUpdate{On: &OnState{On: true}})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("%v", err)
	}
	c.SetWriteGuard(func(context.Context, string, interface{}) error { return fmt.Errorf("manual hold") })
	if err := c.TurnOnLight(context.Background(), "k"); err == nil || !strings.Contains(err.Error(), "manual hold") {
		t.Fatalf("%v", err)
	}
	if received != 1 {
		t.Fatal("guarded write reached server")
	}
	if c.BridgeAddress() != "unused" {
		t.Fatal("wrong Bridge identity")
	}
}

func TestRetryAfterPreservesBridgeDelay(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	if got := retryDelay("120", now); got != 2*time.Minute {
		t.Fatal(got)
	}
	if got := retryDelay(now.Add(time.Minute).Format(http.TimeFormat), now); got != time.Minute {
		t.Fatal(got)
	}
	if got := retryDelay("invalid", now); got != 0 {
		t.Fatal(got)
	}
}

func TestEventStreamReconnectsAndRequiresFreshConnectionSignal(t *testing.T) {
	var requests int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: [{\"type\":\"update\",\"data\":[{\"id\":\"light-%d\",\"type\":\"light\"}]}]\n\n", requests)
		w.(http.Flusher).Flush()
		if requests > 1 {
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	c := NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retry := make(chan struct{})
	stream := &EventStream{client: c, events: make(chan Event, 10), errors: make(chan error, 10),
		ctx: ctx, cancel: cancel, connected: make(chan struct{}, 1),
		waitReconnect: func(ctx context.Context) bool {
			select {
			case <-retry:
				return true
			case <-ctx.Done():
				return false
			}
		},
	}
	go stream.connect(ctx)
	defer stream.Close()
	for _, channel := range []<-chan struct{}{stream.Connected()} {
		select {
		case <-channel:
		case <-time.After(time.Second):
			t.Fatal("no initial connection")
		}
	}
	select {
	case event := <-stream.Events():
		if event.Data[0].ID != "light-1" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	select {
	case <-stream.Errors():
	case <-time.After(time.Second):
		t.Fatal("disconnect not reported")
	}
	if stream.IsConnected() || stream.Revision() == 0 {
		t.Fatal("disconnect did not invalidate observations")
	}
	retry <- struct{}{}
	select {
	case <-stream.Connected():
	case <-time.After(time.Second):
		t.Fatal("no reconnect signal")
	}
	select {
	case event := <-stream.Events():
		if event.Data[0].ID != "light-2" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("no recovery event")
	}
}

func TestFilterEventsDropsOtherResourceTypes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &EventStream{events: make(chan Event, 2), errors: make(chan error, 2), ctx: ctx, cancel: cancel}
	stream.processEvent(`[{"type":"update","data":[{"type":"button","id":"switch"}]},` +
		`{"type":"update","data":[{"type":"light","id":"lamp"}]}]`)
	select {
	case event := <-stream.FilterEvents("light"):
		if event.Data[0].ID != "lamp" {
			t.Fatalf("button event got through a light filter: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("light update filtered out")
	}
}

// collect drains what processEvent delivered and reports whether it raised a problem.
func collect(t *testing.T, batch string) ([]Event, bool) {
	t.Helper()
	stream := &EventStream{events: make(chan Event, 10), errors: make(chan error, 10)}
	stream.processEvent(batch)
	close(stream.events)
	var events []Event
	for event := range stream.events {
		events = append(events, event)
	}
	return events, len(stream.errors) > 0
}

func TestStatusArrivesAsTextOrAsSceneObject(t *testing.T) {
	events, problem := collect(t, `[{"type":"update","data":[
		{"id":"zc","type":"zigbee_connectivity","status":"connected"},
		{"id":"ent","type":"entertainment_configuration","status":"active"},
		{"id":"sc","type":"scene","status":{"active":"static"}},
		{"id":"k","type":"light","dimming":{"brightness":42}}]}]`)
	if problem || len(events) != 1 || len(events[0].Data) != 4 {
		t.Fatalf("problem=%v events=%+v", problem, events)
	}
	data := events[0].Data
	if data[0].Status.Value != "connected" || data[1].Status.Value != "active" || data[2].Status.Active != "static" {
		t.Fatalf("statuses decoded as %+v %+v %+v", data[0].Status, data[1].Status, data[2].Status)
	}
	if data[3].Dimming == nil || data[3].Dimming.Brightness != 42 {
		t.Fatalf("light change lost: %+v", data[3])
	}
}

func TestUnexpectedFieldShapeLosesOnlyThatItem(t *testing.T) {
	events, problem := collect(t, `[
		{"type":"update","data":[{"id":"odd","type":"future_thing","on":"sometimes"},
			{"id":"k","type":"light","dimming":{"brightness":42}}]},
		{"type":"update","data":[{"id":"b","type":"light","on":{"on":false}}]}]`)
	if !problem {
		t.Fatal("unreadable item was dropped silently")
	}
	if len(events) != 2 || len(events[0].Data) != 1 || events[0].Data[0].ID != "k" || events[1].Data[0].ID != "b" {
		t.Fatalf("light changes lost alongside the unreadable item: %+v", events)
	}
}

func TestStatusIsWrittenBackInTheShapeItArrivedIn(t *testing.T) {
	for _, raw := range []string{`"connected"`, `{"active":"static"}`} {
		var status EventStatus
		if err := json.Unmarshal([]byte(raw), &status); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(status)
		if err != nil || string(out) != raw {
			t.Fatalf("%s came back as %s (%v)", raw, out, err)
		}
	}
}
