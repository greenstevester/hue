package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kungfusheep/hue/circadian"
	"github.com/kungfusheep/hue/client"
)

func TestCircadianRunKeepsItsSwitchOffsBesideTheConfiguration(t *testing.T) {
	rooms := []client.Room{
		{ID: "kitchen", Metadata: client.Metadata{Name: "Kitchen"},
			Children: []client.ResourceIdentifier{{RID: "k", RType: "light"}}},
		{ID: "bedroom", Metadata: client.Metadata{Name: "Bedroom"},
			Children: []client.ResourceIdentifier{{RID: "b", RType: "light"}}},
	}
	lights := []client.Light{{ID: "k", On: client.OnState{On: true}}, {ID: "b", On: client.OnState{On: true}}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/eventstream/clip/v2" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		data := map[string]interface{}{
			"/clip/v2/resource/room": rooms, "/clip/v2/resource/light": lights,
			"/clip/v2/resource/device": []client.Device{}, "/clip/v2/resource/entertainment_configuration": []client.Entertainment{},
		}[r.URL.Path]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{}, "data": data})
	}))
	defer server.Close()
	previous := hueClient
	hueClient = client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	defer func() { hueClient = previous }()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	config := circadian.DefaultConfig()
	config.Enabled = true
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	// "k" is on, so the controller must drop it from the saved list and rewrite the file.
	statePath := filepath.Join(dir, "config.state.json")
	if err := os.WriteFile(statePath, []byte(`{"switched_off":["k"]}`), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	command := newCircadianCommand()
	command.SetArgs([]string{"run", "--config", configPath})
	finished := make(chan error, 1)
	go func() { finished <- command.ExecuteContext(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		saved, _ := os.ReadFile(statePath)
		if strings.Join(strings.Fields(string(saved)), "") == `{"switched_off":[]}` {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("run never used %s; it still holds %s", statePath, saved)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
