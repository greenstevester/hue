package circadian

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/kungfusheep/hue/client"
)

// Bridge is the small transport contract used by the controller.
type Bridge interface {
	UpdateLight(context.Context, string, client.LightUpdate) error
}

type automationKey struct{}
type permit struct {
	id       string
	revision uint64
}

type lightState struct {
	managed     ManagedLight
	holdUntil   time.Time
	revision    uint64
	last        client.LightUpdate
	previous    client.Light
	sentAt      time.Time
	pending     bool
	switchedOff bool
}

type Engine struct {
	mu               sync.Mutex
	config           Config
	bridge           Bridge
	now              func() time.Time
	lights           map[string]*lightState
	healthy          bool
	paused           bool
	blocked          map[string]bool
	activities       map[string]int
	lastWrite        time.Time
	invalidInventory bool
	connection       func() bool
	retryAt          time.Time
	failures         int
	statePath        string // where switch-offs are saved; empty keeps them in memory only
	stateDirty       bool   // switch-offs changed since the last save
}

type LightStatus struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	Room       string     `json:"room"`
	On         bool       `json:"on"`
	Brightness float64    `json:"brightness"`
	Mirek      int        `json:"mirek,omitempty"`
	XY         *client.XY `json:"xy,omitempty"`
	HoldUntil  time.Time  `json:"hold_until"`
	Target     Target     `json:"target"`
}

type Status struct {
	Enabled bool          `json:"enabled"`
	Healthy bool          `json:"healthy"`
	Paused  bool          `json:"paused"`
	Lights  []LightStatus `json:"lights"`
}

// SetEnabled changes automation without changing a light's current state or manual hold.
func (e *Engine) SetEnabled(enabled bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if enabled && e.invalidInventory {
		return fmt.Errorf("room membership changed; restart to rediscover lights")
	}
	e.config.Enabled = enabled
	e.paused = !enabled
	for _, s := range e.lights {
		s.revision++
	}
	return nil
}

// SetSchedule applies validated wake and bedtime overrides without clearing manual holds.
func (e *Engine) SetSchedule(wake, bedtime string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	updated := e.config
	updated.WakeTime, updated.Bedtime = wake, bedtime
	if err := updated.Validate(); err != nil {
		return err
	}
	e.config = updated
	for _, s := range e.lights {
		s.revision++
	}
	return nil
}

// SetLightEnabled excludes a managed light from automation while retaining its observation.
func (e *Engine) SetLightEnabled(id string, enabled bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.lights[id]
	if s == nil {
		return fmt.Errorf("light is outside Kitchen and Bedroom")
	}
	var excluded []string
	for _, candidate := range e.config.ExcludeLights {
		if candidate != id {
			excluded = append(excluded, candidate)
		}
	}
	if !enabled {
		excluded = append(excluded, id)
	}
	e.config.ExcludeLights = excluded
	s.revision++
	return nil
}

func (e *Engine) lightEnabled(id string) bool {
	for _, excluded := range e.config.ExcludeLights {
		if excluded == id {
			return false
		}
	}
	return true
}

func NewEngine(c Config, b Bridge, lights []ManagedLight, now func() time.Time) (*Engine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if b == nil || now == nil {
		return nil, fmt.Errorf("Bridge and clock are required")
	}
	e := &Engine{
		config: c, bridge: b, now: now, lights: map[string]*lightState{},
		blocked: map[string]bool{}, activities: map[string]int{}, paused: true,
	}
	for _, l := range lights {
		if strings.ToLower(l.Room.Name) != "kitchen" && strings.ToLower(l.Room.Name) != "bedroom" {
			return nil, fmt.Errorf("unapproved room %q", l.Room.Name)
		}
		e.lights[l.Light.ID] = &lightState{managed: l}
	}
	return e, nil
}

func (e *Engine) Pause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paused = true
	for _, s := range e.lights {
		s.revision++
	}
}
func (e *Engine) Resume() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.invalidInventory {
		return
	}
	e.paused = false
	for _, s := range e.lights {
		s.holdUntil = time.Time{}
		s.last = client.LightUpdate{}
		s.pending = false
		s.revision++
	}
}

func (e *Engine) SetHealthy(healthy bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.healthy = healthy
	if !healthy {
		for _, s := range e.lights {
			s.revision++
		}
	}
}

// SetEntertainment excludes every light in an active Bridge entertainment session.
func (e *Engine) SetEntertainment(configs []client.Entertainment) {
	e.mu.Lock()
	defer e.mu.Unlock()
	blocked := map[string]bool{}
	for _, c := range configs {
		if c.Status == "active" {
			for _, l := range c.LightServices {
				blocked[l.RID] = true
			}
			for _, ch := range c.Channels {
				for _, m := range ch.Members {
					blocked[m.Service.RID] = true
				}
			}
		}
	}
	for id, s := range e.lights {
		if e.blocked[id] && !blocked[id] {
			e.hold(s)
		}
	}
	e.blocked = blocked
}

func (e *Engine) hold(s *lightState) {
	duration, _ := time.ParseDuration(e.config.OverrideDuration)
	s.holdUntil = e.now().Add(duration)
	s.revision++
	s.last = client.LightUpdate{}
	s.pending = false
	if s.switchedOff {
		e.stateDirty = true
	}
	s.switchedOff = false
}

// Guard runs inside the client's write lock, immediately before sending a command.
func (e *Engine) Guard(ctx context.Context, path string, data interface{}) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if activity, ok := data.(client.Activity); ok && path == "/activity" {
		for id, s := range e.lights {
			matches := len(activity.Targets) == 0
			for _, target := range activity.Targets {
				matches = matches || target == id
			}
			if !matches {
				continue
			}
			if activity.Active {
				e.activities[id]++
				e.hold(s)
			} else {
				e.activities[id] = max(0, e.activities[id]-1)
				e.hold(s)
			}
		}
		return nil
	}
	if p, ok := ctx.Value(automationKey{}).(permit); ok {
		s := e.lights[p.id]
		if s == nil || s.revision != p.revision || !e.writeAllowed(p.id, s) {
			return fmt.Errorf("circadian command superseded or paused")
		}
		return nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[1] == "light" {
		if s := e.lights[parts[2]]; s != nil {
			e.hold(s)
		}
	} else {
		// Scene/group/generic writes may affect either managed room. Pause both conservatively.
		for _, s := range e.lights {
			e.hold(s)
		}
	}
	return nil
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	status := Status{Healthy: e.healthy, Paused: e.paused, Enabled: e.config.Enabled && !e.paused}
	for id, s := range e.lights {
		light := LightStatus{ID: id, Name: s.managed.Light.Metadata.Name,
			Room: s.managed.Room.Name, Enabled: e.lightEnabled(id),
			On: s.managed.Light.On.On, Brightness: s.managed.Light.Dimming.Brightness,
			HoldUntil: s.holdUntil, Target: TargetAt(e.config, e.now(), s.managed.Room)}
		if temperature := s.managed.Light.ColorTemperature; temperature != nil && temperature.MirekValid {
			light.Mirek = temperature.Mirek
		}
		if color := s.managed.Light.Color; color != nil {
			xy := color.XY
			light.XY = &xy
		}
		status.Lights = append(status.Lights, light)
	}
	return status
}

// Observe merges partial events, treating unexplained changed fields as manual intent.
func (e *Engine) Observe(data client.EventData) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// A null mirek accompanies colour-mode changes; it is not a manual white target.
	if data.ColorTemperature != nil && data.ColorTemperature.Mirek <= 0 {
		data.ColorTemperature = nil
	}
	s := e.lights[data.ID]
	if s == nil || data.Type != "light" {
		return
	}
	old := s.managed.Light
	next := old
	if data.On != nil {
		next.On = *data.On
	}
	if data.Dimming != nil {
		next.Dimming.Brightness = data.Dimming.Brightness
	}
	if data.Color != nil {
		color := *data.Color
		if old.Color != nil {
			color.Gamut = old.Color.Gamut
		}
		next.Color = &color
	}
	if data.ColorTemperature != nil {
		temperature := *data.ColorTemperature
		if old.ColorTemperature != nil {
			temperature.MirekSchema = old.ColorTemperature.MirekSchema
		}
		next.ColorTemperature = &temperature
	}
	if data.Effects != nil {
		next.Effects = data.Effects
	}
	if data.Mode != nil {
		next.Mode = *data.Mode
	}
	changed := (data.On != nil && old.On != next.On) ||
		(data.Dimming != nil && math.Abs(old.Dimming.Brightness-next.Dimming.Brightness) > 0.1) ||
		(data.Color != nil && (old.Color == nil || distance(old.Color.XY, next.Color.XY) > 0.001)) ||
		(data.ColorTemperature != nil && (old.ColorTemperature == nil ||
			old.ColorTemperature.Mirek != next.ColorTemperature.Mirek)) ||
		(data.Effects != nil && (old.Effects == nil || old.Effects.Effect != next.Effects.Effect))
	if data.On != nil && old.On == next.On {
		data.On = nil
	}
	if data.Dimming != nil && math.Abs(old.Dimming.Brightness-next.Dimming.Brightness) <= 0.1 {
		data.Dimming = nil
	}
	if data.Color != nil && old.Color != nil && distance(old.Color.XY, next.Color.XY) <= 0.001 {
		data.Color = nil
	}
	if data.ColorTemperature != nil && old.ColorTemperature != nil &&
		old.ColorTemperature.Mirek == next.ColorTemperature.Mirek {
		data.ColorTemperature = nil
	}
	if data.Effects != nil && old.Effects != nil && old.Effects.Effect == next.Effects.Effect {
		data.Effects = nil
	}
	if changed && !e.expected(s, data) {
		e.hold(s)
	}
	s.managed.Light = next
}

func distance(a, b client.XY) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

func (e *Engine) expected(s *lightState, d client.EventData) bool {
	if !s.pending {
		return false
	}
	elapsed := e.now().Sub(s.sentAt)
	inTransition := elapsed >= 0 && elapsed <= 12*time.Second
	closeValue := func(observed, start, end, tolerance float64) bool {
		// The Bridge may announce its target immediately, then intermediate fade values.
		if math.Abs(observed-end) <= tolerance {
			return true
		}
		return inTransition && observed >= math.Min(start, end)-tolerance &&
			observed <= math.Max(start, end)+tolerance
	}
	if d.Effects != nil {
		return false
	}
	if d.On != nil && (s.last.On == nil || d.On.On != s.last.On.On) {
		return false
	}
	if d.Dimming != nil && (s.last.Dimming == nil ||
		!closeValue(d.Dimming.Brightness, s.previous.Dimming.Brightness, s.last.Dimming.Brightness, 1)) {
		return false
	}
	whiteEcho := s.last.ColorTemperature != nil && d.ColorTemperature != nil
	if d.Color != nil && !whiteEcho {
		var end client.XY
		tolerance := 0.005
		if s.last.Color != nil {
			end = s.last.Color.XY
		} else if s.last.ColorTemperature != nil {
			// Some third-party bulbs acknowledge a white command only as chromaticity.
			end = targetXY(Target{Mirek: float64(s.last.ColorTemperature.Mirek)})
			tolerance = 0.01
		} else {
			return false
		}
		start := end
		if s.previous.Color != nil {
			start = s.previous.Color.XY
		}
		if !closeValue(d.Color.XY.X, start.X, end.X, tolerance) ||
			!closeValue(d.Color.XY.Y, start.Y, end.Y, tolerance) {
			return false
		}
	}
	if d.ColorTemperature != nil {
		if s.last.ColorTemperature == nil {
			return false
		}
		start := s.last.ColorTemperature.Mirek
		if s.previous.ColorTemperature != nil {
			start = s.previous.ColorTemperature.Mirek
		}
		if start <= 0 && inTransition && s.managed.Light.ColorTemperature != nil {
			limits := s.managed.Light.ColorTemperature.MirekSchema
			if limits != nil && d.ColorTemperature.Mirek >= limits.MirekMinimum &&
				d.ColorTemperature.Mirek <= limits.MirekMaximum {
				return true
			}
		}
		if !closeValue(float64(d.ColorTemperature.Mirek), float64(start), float64(s.last.ColorTemperature.Mirek), 2) {
			return false
		}
	}
	return true
}

func meaningful(a, b client.LightUpdate) bool {
	if (a.On == nil) != (b.On == nil) || (a.Dimming == nil) != (b.Dimming == nil) ||
		(a.Color == nil) != (b.Color == nil) || (a.ColorTemperature == nil) != (b.ColorTemperature == nil) {
		return true
	}
	if a.On != nil && *a.On != *b.On {
		return true
	}
	if a.Dimming != nil && math.Abs(a.Dimming.Brightness-b.Dimming.Brightness) >= 1 {
		return true
	}
	if a.Color != nil && distance(a.Color.XY, b.Color.XY) >= 0.003 {
		return true
	}
	return a.ColorTemperature != nil && absInt(a.ColorTemperature.Mirek-b.ColorTemperature.Mirek) >= 2
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Tick sends at most one current target, keeping Bridge traffic below one write per second.
func (e *Engine) Tick(ctx context.Context) error {
	e.mu.Lock()
	if e.now().Before(e.lastWrite) {
		e.lastWrite = time.Time{}
	}
	if e.now().Sub(e.lastWrite) < time.Second {
		e.mu.Unlock()
		return nil
	}
	var selected *lightState
	var update client.LightUpdate
	var id string
	for candidate, s := range e.lights {
		if !e.writeAllowed(candidate, s) {
			continue
		}
		proposed := Adapt(TargetAt(e.config, e.now(), s.managed.Room), s.managed.Light, s.switchedOff)
		if !meaningful(proposed, s.last) {
			continue
		}
		selected, update, id = s, proposed, candidate
		break
	}
	if selected == nil {
		e.mu.Unlock()
		return nil
	}
	selected.previous = selected.managed.Light
	selected.last, selected.sentAt, selected.pending = update, e.now(), true
	p := permit{id: id, revision: selected.revision}
	e.lastWrite = e.now()
	e.mu.Unlock()
	if update.On == nil || update.On.On {
		update.Dynamics = &client.Dynamics{Duration: 10000}
	}
	// Exclusive, so a manual write that arrives meanwhile is sent after this one and wins.
	writeCtx, cancel := context.WithTimeout(
		client.WithExclusiveWrite(context.WithValue(ctx, automationKey{}, p)), 5*time.Second)
	defer cancel()
	err := e.bridge.UpdateLight(writeCtx, id, update)
	e.mu.Lock()
	defer e.mu.Unlock()
	if selected.revision == p.revision {
		if err != nil {
			selected.last = client.LightUpdate{}
			selected.pending = false
			e.healthy = false
			e.failed(err)
		} else if update.On != nil {
			if selected.switchedOff == update.On.On {
				e.stateDirty = true
			}
			selected.switchedOff = !update.On.On
		}
		if err == nil {
			e.failures = 0
			e.retryAt = time.Time{}
		}
	}
	return err
}

func (e *Engine) writeAllowed(id string, s *lightState) bool {
	if !e.config.Enabled || !e.lightEnabled(id) || e.paused || !e.healthy || e.invalidInventory ||
		e.now().Before(e.retryAt) ||
		(e.connection != nil && !e.connection()) || e.now().Before(s.holdUntil) ||
		e.blocked[id] || e.activities[id] > 0 || s.managed.Light.Mode == "streaming" {
		return false
	}
	effect := s.managed.Light.Effects
	return effect == nil || effect.Status == "" || effect.Status == "no_effect"
}

// failed is called with the engine lock held. Never replay commands while waiting.
func (e *Engine) failed(err error) {
	e.failures++
	delay := 30 * time.Second * time.Duration(1<<min(e.failures-1, 4))
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	var response *client.ResponseError
	if errors.As(err, &response) && response.RetryAfter > delay {
		delay = response.RetryAfter
	}
	if response != nil && response.Status >= 400 && response.Status < 500 && response.Status != 429 {
		e.paused = true
	}
	e.retryAt = e.now().Add(delay)
}

// Run owns the observation stream and stops writes on any uncertainty.
func Run(ctx context.Context, c *client.Client, e *Engine, logf func(string, ...interface{})) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	return run(ctx, c, e, logf, ticker.C)
}

func (e *Engine) invalidateInventory() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.invalidInventory = true
	e.paused = true
	for _, s := range e.lights {
		s.revision++
	}
}

func (e *Engine) reconcileInventory(managed []ManagedLight) error {
	e.mu.Lock()
	matches := len(managed) == len(e.lights)
	for _, l := range managed {
		s := e.lights[l.Light.ID]
		if s == nil || s.managed.Room.Name != l.Room.Name {
			matches = false
		}
	}
	e.mu.Unlock()
	if !matches {
		e.invalidateInventory()
		return fmt.Errorf("selected room membership changed; restart to rediscover")
	}
	for _, l := range managed {
		light := l.Light
		e.Observe(client.EventData{ID: light.ID, Type: "light", On: &light.On, Dimming: &light.Dimming,
			Color: light.Color, ColorTemperature: light.ColorTemperature, Effects: light.Effects})
		e.mu.Lock()
		e.lights[light.ID].managed.Light = light
		e.mu.Unlock()
	}
	return nil
}

func run(
	ctx context.Context, c *client.Client, e *Engine,
	logf func(string, ...interface{}), ticks <-chan time.Time,
) error {
	stream, err := c.StreamEvents(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	e.mu.Lock()
	e.connection = stream.IsConnected
	e.mu.Unlock()
	c.SetWriteGuard(e.Guard)
	defer c.SetWriteGuard(nil)
	refresh := func() error {
		e.SetHealthy(false)
		e.mu.Lock()
		waiting := e.now().Before(e.retryAt)
		e.mu.Unlock()
		if waiting {
			return fmt.Errorf("waiting before Bridge retry")
		}
		revision := stream.Revision()
		if !stream.IsConnected() {
			return fmt.Errorf("waiting for event connection")
		}
		lights, err := c.GetLights(ctx)
		if err != nil {
			return err
		}
		rooms, err := c.GetRooms(ctx)
		if err != nil {
			return err
		}
		devices, err := c.GetDevices(ctx)
		if err != nil {
			return err
		}
		e.mu.Lock()
		configuration := e.config
		e.mu.Unlock()
		managed, err := RoomLights(configuration, rooms, devices, lights)
		if err != nil {
			e.invalidateInventory()
			return err
		}
		if err = e.reconcileInventory(managed); err != nil {
			return err
		}
		entertainment, err := c.GetEntertainmentConfigurations(ctx)
		if err != nil {
			return err
		}
		e.SetEntertainment(entertainment)
		e.SetHealthy(stream.IsConnected() && stream.Revision() == revision)
		return nil
	}
	var refreshed time.Time
	var saveProblem string
	save := func() {
		err := e.saveSwitchedOff()
		if err != nil && err.Error() != saveProblem {
			logf("circadian state: %v", err) // once per new problem, not every second
		}
		if err == nil && saveProblem != "" {
			logf("circadian state saved again")
		}
		saveProblem = ""
		if err != nil {
			saveProblem = err.Error()
		}
	}
	defer save()
	// Stopping closes the stream as well, and the loop may notice that first: a normal stop.
	closed := func() error {
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("event stream closed")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-stream.Connected():
			if !ok {
				return closed()
			}
			if err := refresh(); err != nil {
				logf("circadian refresh: %v", err)
			}
			refreshed = e.now()
		case err, ok := <-stream.Errors():
			if !ok {
				return closed()
			}
			e.SetHealthy(false)
			logf("circadian observation paused: %v", err)
		case event, ok := <-stream.Events():
			if !ok {
				return closed()
			}
			for _, d := range event.Data {
				// Scenes, zones and automations come and go without changing which lights are managed.
				if (event.Type == "add" || event.Type == "delete") && d.Type == "light" {
					e.invalidateInventory()
					logf("circadian paused: a light was added or removed; restart to rediscover rooms")
				}
				if d.Type == "room" || d.Type == "device" {
					e.invalidateInventory()
					logf("circadian paused: room membership or device changed; rediscover before resuming")
				}
				if d.Type == "entertainment_configuration" {
					e.SetHealthy(false)
					refreshed = time.Time{}
				}
				if d.Type == "light" {
					e.Observe(d)
				}
			}
		case <-ticks:
			if e.now().Sub(refreshed) >= 30*time.Second {
				if err := refresh(); err != nil {
					logf("circadian refresh: %v", err)
				}
				refreshed = e.now()
			}
			if err := e.Tick(ctx); err != nil {
				logf("circadian update: %v", err)
			}
			save()
		}
	}
}
