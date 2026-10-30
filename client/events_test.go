package client

import (
	"context"
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
