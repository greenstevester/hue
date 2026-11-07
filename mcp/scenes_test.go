package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kungfusheep/hue/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// sceneBridge serves one Kitchen room with one lamp and records the scene bodies posted to it.
func sceneBridge(t *testing.T) (*client.Client, *[]string) {
	t.Helper()
	var posted []string
	resources := map[string]interface{}{
		"room": []client.Room{{ID: "kitchen", Metadata: client.Metadata{Name: "Kitchen"},
			Children: []client.ResourceIdentifier{{RID: "k-lamp", RType: "light"}}}},
		"zone":   []client.Zone{},
		"device": []client.Device{},
		"grouped_light/gl-kitchen": []client.Group{{ID: "gl-kitchen",
			Owner: &client.ResourceIdentifier{RID: "kitchen", RType: "room"}}},
		"light/k-lamp": []client.Light{{ID: "k-lamp", On: client.OnState{On: true},
			Dimming: client.Dimming{Brightness: 60}}},
		"scene":           []client.Scene{{ID: "5f1c0de4-0000-4000-8000-000000000001", IDV1: "/scenes/AbC", Metadata: client.Metadata{Name: "Relax"}}},
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
	t.Cleanup(server.Close)
	return client.NewClient(strings.TrimPrefix(server.URL, "https://"), "test", server.Client()), &posted
}

func TestCreateSceneToolCapturesTheGroupsRoom(t *testing.T) {
	c, posted := sceneBridge(t)
	request := mcp.CallToolRequest{}
	request.Params.Arguments = map[string]interface{}{"name": "Evening", "group_id": "gl-kitchen"}
	result, err := HandleCreateScene(c)(context.Background(), request)
	if err != nil || result.IsError {
		t.Fatalf("%+v %v", result, err)
	}
	if len(*posted) != 1 || !strings.Contains((*posted)[0], `"rid":"kitchen","rtype":"room"`) ||
		!strings.Contains((*posted)[0], `"brightness":60`) {
		t.Fatalf("scene sent: %v", *posted)
	}
}

func TestListScenesLabelsTheCurrentID(t *testing.T) {
	c, _ := sceneBridge(t)
	result, err := HandleListScenes(c)(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "Relax (ID: 5f1c0de4-0000-4000-8000-000000000001, v1: /scenes/AbC)") {
		t.Fatalf("scene list: %s", text)
	}
}
