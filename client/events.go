package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// EventStream represents a connection to the Hue event stream
type EventStream struct {
	client        *Client
	events        chan Event
	errors        chan error
	ctx           context.Context
	cancel        context.CancelFunc
	connected     chan struct{}
	isConnected   atomic.Bool
	problem       atomic.Uint64
	waitReconnect func(context.Context) bool
}

// Event represents a Hue v2 event
type Event struct {
	CreationTime string      `json:"creationtime"`
	ID           string      `json:"id"`
	Type         string      `json:"type"`
	Data         []EventData `json:"data"`
}

// EventData represents the data portion of an event
type EventData struct {
	ID    string              `json:"id"`
	IDV1  string              `json:"id_v1,omitempty"`
	Type  string              `json:"type"`
	Mode  *string             `json:"mode,omitempty"`
	Owner *ResourceIdentifier `json:"owner,omitempty"`

	// Light events
	On               *OnState          `json:"on,omitempty"`
	Dimming          *Dimming          `json:"dimming,omitempty"`
	Color            *Color            `json:"color,omitempty"`
	ColorTemperature *ColorTemperature `json:"color_temperature,omitempty"`
	Effects          *Effects          `json:"effects,omitempty"`

	// Motion sensor events
	Motion *MotionReport `json:"motion,omitempty"`

	// Temperature sensor events
	Temperature *TemperatureReport `json:"temperature,omitempty"`

	// Light level sensor events
	Light *LightLevelReport `json:"light,omitempty"`

	// Button events
	Button *ButtonReport `json:"button,omitempty"`

	// Scene, entertainment and connectivity events
	Status *EventStatus `json:"status,omitempty"`

	// Group events
	Alert *Alert `json:"alert,omitempty"`
}

// StreamEvents opens a cancellable connection independent of normal request timeouts.
func (c *Client) StreamEvents(parent context.Context) (*EventStream, error) {
	ctx, cancel := context.WithCancel(parent)
	stream := &EventStream{client: c, events: make(chan Event, 100), errors: make(chan error, 10),
		ctx: ctx, cancel: cancel, connected: make(chan struct{}, 1)}
	go stream.connect(ctx)
	return stream, nil
}

func (es *EventStream) Events() <-chan Event { return es.events }
func (es *EventStream) Errors() <-chan error { return es.errors }

// Connected signals each successful connection; consumers must refresh after reconnecting.
func (es *EventStream) Connected() <-chan struct{} { return es.connected }
func (es *EventStream) IsConnected() bool          { return es.isConnected.Load() }

// Revision changes whenever observation may have lost information.
func (es *EventStream) Revision() uint64 { return es.problem.Load() }
func (es *EventStream) Close()           { es.cancel() }

func (es *EventStream) report(err error) {
	es.problem.Add(1)
	select {
	case es.errors <- err:
	default:
	}
}

func (es *EventStream) connect(ctx context.Context) {
	defer close(es.events)
	defer close(es.errors)
	defer close(es.connected)
	for ctx.Err() == nil {
		err := es.streamEvents(ctx)
		es.isConnected.Store(false)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = io.EOF
		}
		es.report(fmt.Errorf("stream disconnected: %w", err))
		if es.waitReconnect != nil {
			if !es.waitReconnect(ctx) {
				return
			}
			continue
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (es *EventStream) streamEvents(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("https://%s/eventstream/clip/v2", es.client.bridgeIP), nil)
	if err != nil {
		return err
	}
	req.Header.Set("hue-application-key", es.client.username)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	httpClient := *es.client.httpClient
	httpClient.Timeout = 0
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream HTTP %d", resp.StatusCode)
	}
	es.isConnected.Store(true)
	select {
	case es.connected <- struct{}{}:
	default:
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				es.processEvent(data.String())
				data.Reset()
			}
		} else if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		}
	}
	return scanner.Err()
}

// EventStatus is a resource's status. Scenes send an object such as {"active": "static"};
// entertainment areas and Zigbee connectivity send text such as "active" or "connected".
type EventStatus struct {
	Active string // a scene's state: inactive, static or dynamic_palette
	Value  string // the status of any resource that sends it as text
}

func (s *EventStatus) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &s.Value)
	}
	var scene struct {
		Active string `json:"active"`
	}
	if err := json.Unmarshal(data, &scene); err != nil {
		return err
	}
	s.Active = scene.Active
	return nil
}

// MarshalJSON writes the status back in the shape it arrived in.
func (s EventStatus) MarshalJSON() ([]byte, error) {
	if s.Value != "" {
		return json.Marshal(s.Value)
	}
	return json.Marshal(map[string]string{"active": s.Active})
}

func (es *EventStream) processEvent(data string) {
	var batch []json.RawMessage
	if err := json.Unmarshal([]byte(data), &batch); err != nil {
		es.report(fmt.Errorf("parse event: %w", err))
		return
	}
	for _, raw := range batch {
		event, err := decodeEvent(raw)
		if err != nil {
			es.report(fmt.Errorf("parse event: %w", err))
			if len(event.Data) == 0 {
				continue // nothing readable survived
			}
		}
		select {
		case es.events <- event:
		default:
			es.report(fmt.Errorf("event buffer overflow; refresh Bridge state"))
		}
	}
}

// decodeEvent keeps every data item it can read. A resource whose fields arrive in an unexpected
// shape is dropped and reported on its own, so it cannot hide light changes in the same event.
func decodeEvent(raw json.RawMessage) (Event, error) {
	var envelope struct {
		CreationTime string            `json:"creationtime"`
		ID           string            `json:"id"`
		Type         string            `json:"type"`
		Data         []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Event{}, err
	}
	event := Event{CreationTime: envelope.CreationTime, ID: envelope.ID, Type: envelope.Type}
	var problem error
	for _, item := range envelope.Data {
		var data EventData
		if err := json.Unmarshal(item, &data); err != nil {
			problem = fmt.Errorf("%s: %w", item, err)
			continue
		}
		event.Data = append(event.Data, data)
	}
	return event, problem
}

// FilterEvents selects events by resource type, such as light or button.
func (es *EventStream) FilterEvents(types ...string) <-chan Event {
	filtered := make(chan Event, 100)
	wanted := make(map[string]bool, len(types))
	for _, kind := range types {
		wanted[kind] = true
	}
	go func() {
		defer close(filtered)
		for {
			select {
			case <-es.ctx.Done():
				return
			case event, ok := <-es.events:
				if !ok {
					return
				}
				matches := len(types) == 0
				for _, data := range event.Data {
					matches = matches || wanted[data.Type]
				}
				if matches {
					select {
					case filtered <- event:
					case <-es.ctx.Done():
						return
					}
				}
			}
		}
	}()
	return filtered
}
