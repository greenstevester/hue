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

// blockingBridge holds every write until release is closed, reporting each arrival by light ID.
func blockingBridge(t *testing.T) (*Client, <-chan string, chan struct{}) {
	t.Helper()
	arrived := make(chan string, 10)
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- strings.TrimPrefix(r.URL.Path, "/clip/v2/resource/light/")
		<-release
		fmt.Fprint(w, `{"errors":[],"data":[]}`)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client()), arrived, release
}

func TestOrdinaryWritesReachTheBridgeAtTheSameTime(t *testing.T) {
	c, arrived, release := blockingBridge(t)
	c.SetWriteGuard(func(context.Context, string, interface{}) error { return nil })
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.SetLightBrightness(context.Background(), id, 50); err != nil {
				t.Errorf("write %s: %v", id, err)
			}
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			t.Fatal("second write waited for the first; ordinary writes are queued")
		}
	}
	close(release)
	wg.Wait()
}

func TestWriteStartedDuringExclusiveWriteIsSentAfterIt(t *testing.T) {
	c, arrived, release := blockingBridge(t)
	var mu sync.Mutex
	var guarded []string
	c.SetWriteGuard(func(_ context.Context, path string, _ interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		guarded = append(guarded, strings.TrimPrefix(path, "/resource/light/"))
		return nil
	})
	automation := make(chan error, 1)
	go func() {
		automation <- c.UpdateLight(WithExclusiveWrite(context.Background()), "auto", LightUpdate{})
	}()
	if id := <-arrived; id != "auto" {
		t.Fatalf("unexpected first write %s", id)
	}
	manual := make(chan error, 1)
	go func() { manual <- c.SetLightBrightness(context.Background(), "manual", 20) }()
	select {
	case id := <-arrived:
		t.Fatalf("write %s reached the Bridge while the exclusive write was in flight", id)
	case <-time.After(200 * time.Millisecond):
	}
	mu.Lock()
	if len(guarded) != 1 {
		t.Fatalf("guard ran for %v before the exclusive write finished", guarded)
	}
	mu.Unlock()
	close(release)
	if err := <-automation; err != nil {
		t.Fatal(err)
	}
	if id := <-arrived; id != "manual" {
		t.Fatalf("unexpected second write %s", id)
	}
	if err := <-manual; err != nil {
		t.Fatal(err)
	}
}
