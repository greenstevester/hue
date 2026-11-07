package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kungfusheep/hue/client"
)

func TestHueScenesCreateCapturesTheNamedRoom(t *testing.T) {
	var posted []string
	resources := map[string]interface{}{
		"room": []client.Room{{ID: "kitchen", Metadata: client.Metadata{Name: "Kitchen"},
			Services: []client.ResourceIdentifier{{RID: "gl-kitchen", RType: "grouped_light"}},
			Children: []client.ResourceIdentifier{{RID: "k-lamp", RType: "light"}}}},
		"zone":   []client.Zone{},
		"device": []client.Device{},
		"grouped_light/gl-kitchen": []client.Group{{ID: "gl-kitchen",
			Owner: &client.ResourceIdentifier{RID: "kitchen", RType: "room"}}},
		"light/k-lamp":    []client.Light{{ID: "k-lamp", On: client.OnState{On: true}, Dimming: client.Dimming{Brightness: 60}}},
		"scene/new-scene": []client.Scene{{ID: "new-scene"}},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/clip/v2/resource/")
		if r.Method == http.MethodPost {
			var body json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted = append(posted, string(body))
			fmt.Fprint(w, `{"errors":[],"data":[{"rid":"new-scene","rtype":"scene"}]}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"errors": []interface{}{}, "data": resources[path]})
	}))
	defer server.Close()
	previous := hueClient
	hueClient = client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client())
	defer func() { hueClient = previous }()

	if err := createHueSceneCmd.RunE(createHueSceneCmd, []string{"Evening", "Kitchen"}); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 || !strings.Contains(posted[0], `"rid":"kitchen","rtype":"room"`) ||
		!strings.Contains(posted[0], `"brightness":60`) {
		t.Fatalf("scene sent: %v", posted)
	}
}
