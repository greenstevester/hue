package scheduler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kungfusheep/hue/client"
)

func TestStopSequenceCancelsActiveBridgeRequest(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	c := client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	s := NewScheduler(c)
	defer s.Stop()
	id, err := s.ExecuteSequence(&Sequence{Commands: []Command{{Type: "light", Action: "on", Target: "k"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	if err := s.StopSequence(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request was not cancelled")
	}
}

func TestSchedulerSnapshotsAndRepeatedStopsAreSafe(t *testing.T) {
	c := client.NewClient("unused", "test", http.DefaultClient)
	s := NewScheduler(c)
	sequence := &Sequence{Commands: []Command{{Type: "light", Action: "on", Target: "k", Delay: time.Hour}}}
	id, err := s.ExecuteSequence(sequence)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := s.GetSequences()
	snapshot[id].Name = "changed"
	snapshot[id].Commands[0].Target = "office"
	if s.GetSequences()[id].Commands[0].Target != "k" {
		t.Fatal("snapshot modified running sequence")
	}
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() { defer group.Done(); _ = s.StopSequence(id); s.GetSequences() }()
	}
	group.Wait()
	s.Stop()
	s.Stop()
	if err := s.ExecuteCommand(Command{}); err == nil {
		t.Fatal("stopped scheduler accepted command")
	}
	if _, err := s.ExecuteSequence(sequence); err == nil {
		t.Fatal("stopped scheduler accepted sequence")
	}
}

func TestSequenceHoldsActivityUntilCompletionAndReportsErrors(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"errors":[],"data":[]}`)
	}))
	defer server.Close()
	c := client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	activities := make(chan bool, 2)
	c.SetWriteGuard(func(_ context.Context, _ string, data interface{}) error {
		if a, ok := data.(client.Activity); ok {
			activities <- a.Active
		}
		return nil
	})
	s := NewScheduler(c)
	sequence := &Sequence{Commands: []Command{{Type: "light", Action: "on", Target: "k"}}}
	if _, err := s.ExecuteSequence(sequence); err != nil {
		t.Fatal(err)
	}
	select {
	case active := <-activities:
		if !active {
			t.Fatal("activity not reserved")
		}
	case <-time.After(time.Second):
		t.Fatal("no reservation")
	}
	select {
	case active := <-activities:
		if active {
			t.Fatal("activity not released")
		}
	case <-time.After(time.Second):
		t.Fatal("no release")
	}
	s.Stop()
}
