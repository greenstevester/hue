package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kungfusheep/hue/circadian"
)

//go:embed assets/*
var assets embed.FS

// Controller is shared with the scheduler; the web server never creates another scheduler.
type Controller interface {
	Status() circadian.Status
	SetEnabled(bool) error
	SetSchedule(string, string) error
	SetLightEnabled(string, bool) error
}

type Server struct {
	mu         sync.Mutex
	controller Controller
	config     circadian.Config
	configPath string
	now        func() time.Time
	handler    http.Handler
	addresses  func() ([]net.Addr, error)
}

type scheduledProfile struct {
	Phase    string                       `json:"phase"`
	At       time.Time                    `json:"at"`
	Profiles map[string]circadian.Profile `json:"profiles"`
}

type snapshot struct {
	circadian.Status
	At       time.Time          `json:"at"`
	Timezone string             `json:"timezone"`
	WakeTime string             `json:"wake_time"`
	Bedtime  string             `json:"bedtime"`
	Schedule []scheduledProfile `json:"schedule"`
}

func New(controller Controller, config circadian.Config, path string, now func() time.Time) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if controller == nil || now == nil || path == "" {
		return nil, fmt.Errorf("controller, configuration path, and clock are required")
	}
	s := &Server{controller: controller, config: config, configPath: path, now: now,
		addresses: net.InterfaceAddrs}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/control", s.control)
	mux.HandleFunc("POST /api/schedule", s.schedule)
	mux.HandleFunc("POST /api/light", s.light)
	mux.HandleFunc("GET /api/control", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Use POST to change automation", http.StatusMethodNotAllowed)
	})
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	s.handler = mux
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if value, _, err := net.SplitHostPort(host); err == nil {
		host = value
	}
	if !s.localHost(strings.ToLower(host)) {
		http.Error(w, "Unrecognised host", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	s.handler.ServeHTTP(w, r)
}

// localHost accepts this machine's names and its current addresses. Addresses are read on each
// request, so a new address from the router works without a restart. Other names are refused,
// which stops a web page from reaching this server through a hostname it controls.
func (s *Server) localHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if name, err := os.Hostname(); err == nil {
		name = strings.ToLower(name)
		if host == name || host == name+".local" {
			return true
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	addresses, err := s.addresses()
	if err != nil {
		log.Printf("read local addresses: %v", err)
		return false
	}
	for _, address := range addresses {
		if local, _, err := net.ParseCIDR(address.String()); err == nil && local.Equal(ip) {
			return true
		}
	}
	return false
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeSnapshot(w)
}

func (s *Server) writeSnapshot(w http.ResponseWriter) {
	now := s.now()
	location, _ := time.LoadLocation(s.config.Timezone)
	result := snapshot{Status: s.controller.Status(), At: now.In(location), Timezone: s.config.Timezone,
		WakeTime: s.config.WakeTime, Bedtime: s.config.Bedtime}
	result.Lights = append([]circadian.LightStatus(nil), result.Lights...)
	sort.Slice(result.Lights, func(i, j int) bool {
		if result.Lights[i].Room != result.Lights[j].Room {
			return result.Lights[i].Room < result.Lights[j].Room
		}
		return result.Lights[i].Name < result.Lights[j].Name
	})
	for _, phase := range circadian.ScheduleForDay(s.config, now) {
		profiles := map[string]circadian.Profile{}
		for _, room := range s.config.Rooms {
			p := s.config.Profiles[phase.Phase]
			if override, ok := room.Profiles[phase.Phase]; ok {
				p = override
			}
			profiles[room.Name] = p
		}
		result.Schedule = append(result.Schedule, scheduledProfile{Phase: phase.Phase, At: phase.At, Profiles: profiles})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("write web status: %v", err)
	}
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeChange(w, r, &request) {
		return
	}
	if request.Enabled == nil {
		http.Error(w, "Supply an enabled boolean", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.controller.Status().Enabled
	updated := s.config
	updated.Enabled = *request.Enabled
	s.apply(w, updated, func() error { return s.controller.SetEnabled(*request.Enabled) },
		func() error { return s.controller.SetEnabled(previous) })
}

func (s *Server) schedule(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Wake    string `json:"wake_time"`
		Bedtime string `json:"bedtime"`
	}
	if !decodeChange(w, r, &request) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.config
	updated := s.config
	updated.WakeTime, updated.Bedtime = request.Wake, request.Bedtime
	s.apply(w, updated, func() error { return s.controller.SetSchedule(request.Wake, request.Bedtime) },
		func() error { return s.controller.SetSchedule(previous.WakeTime, previous.Bedtime) })
}

func (s *Server) light(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if !decodeChange(w, r, &request) {
		return
	}
	if request.ID == "" || request.Enabled == nil {
		http.Error(w, "Supply a light and enabled boolean", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := true
	var excluded []string
	for _, id := range s.config.ExcludeLights {
		if id == request.ID {
			previous = false
		} else {
			excluded = append(excluded, id)
		}
	}
	if !*request.Enabled {
		excluded = append(excluded, request.ID)
	}
	updated := s.config
	updated.ExcludeLights = excluded
	s.apply(w, updated, func() error { return s.controller.SetLightEnabled(request.ID, *request.Enabled) },
		func() error { return s.controller.SetLightEnabled(request.ID, previous) })
}

func (s *Server) apply(w http.ResponseWriter, updated circadian.Config, change, restore func() error) {
	if err := updated.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := change(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := saveConfig(s.configPath, updated); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			log.Printf("restore automation state: %v", restoreErr)
		}
		log.Printf("save lighting setting: %v", err)
		http.Error(w, "Could not save the setting. Please try again.", http.StatusInternalServerError)
		return
	}
	s.config = updated
	s.writeSnapshot(w)
}

func decodeChange(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			http.Error(w, "Cross-origin changes are not allowed", http.StatusForbidden)
			return false
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Cross-site changes are not allowed", http.StatusForbidden)
		return false
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		http.Error(w, "Use application/json", http.StatusUnsupportedMediaType)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "Supply valid settings", http.StatusBadRequest)
		return false
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "Supply one JSON object", http.StatusBadRequest)
		return false
	}
	return true
}

func saveConfig(path string, config circadian.Config) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hue-config-*")
	if err != nil {
		return err
	}
	defer file.Close()
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !os.IsNotExist(err) {
			log.Printf("remove temporary config: %v", err)
		}
	}()
	if err = file.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err = file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
