package webui

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kungfusheep/hue/circadian"
)

// chrome finds a Chrome or Chromium to run the page in. HUE_TEST_CHROME names one explicitly.
func chrome(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("HUE_TEST_CHROME"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skip("no Chrome or Chromium found; set HUE_TEST_CHROME to run the page in a browser")
	return ""
}

// renderedPage loads the page in headless Chrome and returns it once its script has read the status.
func renderedPage(t *testing.T, s *Server) string {
	t.Helper()
	browser := chrome(t)
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The time budget lets the page's status request finish before Chrome prints the page.
	out, err := exec.CommandContext(ctx, browser, "--headless", "--disable-gpu", "--no-first-run",
		"--no-default-browser-check", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=5000",
		"--dump-dom", server.URL+"/").Output()
	if err != nil {
		t.Fatalf("Chrome could not load the page: %v", err)
	}
	page := string(out)
	if !strings.Contains(page, "Kitchen-Island") {
		t.Fatalf("the page script never listed the lights:\n%s", page)
	}
	return page
}

var loungePanel = regexp.MustCompile(`<section class="panel room" aria-labelledby="lounge-title"( hidden="")?>`)

func TestLoungePanelAppearsOnlyWhenALoungeLightIsControlled(t *testing.T) {
	s, controller, _ := fixture(t)
	if match := loungePanel.FindStringSubmatch(renderedPage(t, s)); match == nil || match[1] == "" {
		t.Fatalf("lounge panel shown with no lounge light: %q", match)
	}

	controller.mu.Lock()
	controller.state.Lights = append(controller.state.Lights, circadian.LightStatus{
		ID: "f", Name: "Lounge floor box", Room: "Lounge", On: true, Brightness: 85, Enabled: true})
	controller.mu.Unlock()
	page := renderedPage(t, s)
	if match := loungePanel.FindStringSubmatch(page); match == nil || match[1] != "" {
		t.Fatalf("lounge panel hidden although the floor box is controlled: %q", match)
	}
	if !strings.Contains(page, "Lounge floor box") {
		t.Fatal("lounge panel does not list the floor box")
	}
}
