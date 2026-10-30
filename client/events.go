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

	// Scene events
	Status *struct {
		Active string `json:"active"`
	} `json:"status,omitempty"`

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

func (es *EventStream) processEvent(data string) {
	var events []Event
	if err := json.Unmarshal([]byte(data), &events); err != nil {
		es.report(fmt.Errorf("parse event: %w", err))
		return
	}
	for _, event := range events {
		select {
		case es.events <- event:
		default:
			es.report(fmt.Errorf("event buffer overflow; refresh Bridge state"))
		}
	}
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
