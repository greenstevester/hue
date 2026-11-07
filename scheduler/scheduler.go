package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/kungfusheep/hue/client"
)

// Command represents a scheduled command
type Command struct {
	Type   string                 // "light", "group", "scene", etc.
	Action string                 // "on", "off", "color", "brightness", etc.
	Target string                 // ID of the target (light, group, etc.)
	Params map[string]interface{} // Additional parameters
	Delay  time.Duration          // Delay before executing this command
}

// Sequence represents a sequence of commands
type Sequence struct {
	ID       string
	Name     string
	Commands []Command
	Loop     bool // Whether to loop the sequence
	Running  bool
	stopChan chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	release  func()
}

// Scheduler manages scheduled lighting operations
type Scheduler struct {
	client    *client.Client
	sequences map[string]*Sequence
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewScheduler creates a new scheduler
func NewScheduler(client *client.Client) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		client:    client,
		sequences: make(map[string]*Sequence),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Stop stops all sequences and shuts down the scheduler
func (s *Scheduler) Stop() {
	s.cancel()

	s.mu.Lock()

	for _, seq := range s.sequences {
		if seq.Running {
			seq.cancel()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// ExecuteCommand executes a single command asynchronously
func (s *Scheduler) ExecuteCommand(cmd Command) error {
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return s.ctx.Err()
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		// Apply delay if specified
		if cmd.Delay > 0 {
			select {
			case <-time.After(cmd.Delay):
			case <-s.ctx.Done():
				return
			}
		}

		// Execute the command
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()

		if err := s.executeCommandSync(ctx, cmd); err != nil {
			log.Printf("scheduled %s %s: %v", cmd.Action, cmd.Target, err)
		}
	}()

	return nil
}

// ExecuteSequence starts executing a sequence asynchronously
func (s *Scheduler) ExecuteSequence(seq *Sequence) (string, error) {
	if seq == nil || len(seq.Commands) == 0 {
		return "", fmt.Errorf("sequence needs commands")
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return "", s.ctx.Err()
	}

	if seq.ID == "" {
		seq.ID = fmt.Sprintf("seq_%d", time.Now().UnixNano())
	}

	if _, exists := s.sequences[seq.ID]; exists && s.sequences[seq.ID].Running {
		s.mu.Unlock()
		return "", fmt.Errorf("sequence %s is already running", seq.ID)
	}

	copy := cloneSequence(seq)
	copy.Running = true
	copy.ctx, copy.cancel = context.WithCancel(s.ctx)
	copy.release = func() {}
	// Reserve the ID and count the run now, then let go of the lock: claiming the lights below can
	// wait for an automation write to finish, and listing or stopping sequences must not wait with it.
	s.sequences[copy.ID] = copy
	s.wg.Add(1)
	s.mu.Unlock()

	var targets []string
	for _, cmd := range copy.Commands {
		if cmd.Type != "light" {
			targets = nil
			break
		}
		targets = append(targets, cmd.Target)
	}
	release, err := s.client.BeginActivity(copy.ctx, targets)
	s.mu.Lock()
	if err != nil {
		copy.Running = false
		if s.sequences[copy.ID] == copy {
			delete(s.sequences, copy.ID)
		}
		s.mu.Unlock()
		copy.cancel()
		s.wg.Done()
		return "", err
	}
	copy.release = release
	s.mu.Unlock()

	// Start the sequence in a goroutine
	go s.runSequence(copy)

	return seq.ID, nil
}

// StopSequence stops a running sequence
func (s *Scheduler) StopSequence(sequenceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	seq, exists := s.sequences[sequenceID]
	if !exists {
		return fmt.Errorf("sequence %s not found", sequenceID)
	}

	if seq.Running {
		seq.cancel()
		seq.Running = false
	}

	return nil
}

// GetSequences returns all sequences
func (s *Scheduler) GetSequences() map[string]*Sequence {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to avoid concurrent modification
	result := make(map[string]*Sequence)
	for k, v := range s.sequences {
		result[k] = cloneSequence(v)
	}
	return result
}

// runSequence executes a sequence of commands
func (s *Scheduler) runSequence(seq *Sequence) {
	defer s.wg.Done()
	defer seq.release()
	defer seq.cancel()
	defer func() {
		s.mu.Lock()
		seq.Running = false
		s.mu.Unlock()
	}()

	for {
		for _, cmd := range seq.Commands {
			// Check if we should stop
			select {
			case <-seq.ctx.Done():
				return
			default:
			}

			// Apply delay if specified
			if cmd.Delay > 0 {
				select {
				case <-time.After(cmd.Delay):
				case <-seq.ctx.Done():
					return
				}
			}

			// Execute the command
			ctx, cancel := context.WithTimeout(seq.ctx, 5*time.Second)
			err := s.executeCommandSync(ctx, cmd)
			cancel()
			if err != nil {
				log.Printf("sequence %s command %s: %v", seq.ID, cmd.Action, err)
				return
			}
		}

		// If not looping, we're done
		if !seq.Loop {
			break
		}
	}
}

func cloneSequence(seq *Sequence) *Sequence {
	copy := *seq
	copy.ctx = nil
	copy.cancel = nil
	copy.release = nil
	copy.stopChan = nil
	copy.Commands = append([]Command(nil), seq.Commands...)
	for i, cmd := range copy.Commands {
		params := make(map[string]interface{}, len(cmd.Params))
		for key, value := range cmd.Params {
			params[key] = value
		}
		copy.Commands[i].Params = params
	}
	return &copy
}

// executeCommandSync executes a command synchronously
func (s *Scheduler) executeCommandSync(ctx context.Context, cmd Command) error {
	switch cmd.Type {
	case "light":
		return s.executeLightCommand(ctx, cmd)
	case "group":
		return s.executeGroupCommand(ctx, cmd)
	case "scene":
		return s.executeSceneCommand(ctx, cmd)
	default:
		return fmt.Errorf("unknown command type: %s", cmd.Type)
	}
}

// executeLightCommand executes a light command
func (s *Scheduler) executeLightCommand(ctx context.Context, cmd Command) error {
	switch cmd.Action {
	case "on":
		return s.client.TurnOnLight(ctx, cmd.Target)
	case "off":
		return s.client.TurnOffLight(ctx, cmd.Target)
	case "brightness":
		if brightness, ok := cmd.Params["brightness"].(float64); ok {
			return s.client.SetLightBrightness(ctx, cmd.Target, brightness)
		}
		return fmt.Errorf("brightness parameter required")
	case "color":
		if color, ok := cmd.Params["color"].(string); ok {
			return s.client.SetLightColor(ctx, cmd.Target, color)
		}
		return fmt.Errorf("color parameter required")
	default:
		return fmt.Errorf("unknown light action: %s", cmd.Action)
	}
}

// executeGroupCommand executes a group command
func (s *Scheduler) executeGroupCommand(ctx context.Context, cmd Command) error {
	switch cmd.Action {
	case "on":
		return s.client.TurnOnGroup(ctx, cmd.Target)
	case "off":
		return s.client.TurnOffGroup(ctx, cmd.Target)
	case "brightness":
		if brightness, ok := cmd.Params["brightness"].(float64); ok {
			return s.client.SetGroupBrightness(ctx, cmd.Target, brightness)
		}
		return fmt.Errorf("brightness parameter required")
	case "color":
		if color, ok := cmd.Params["color"].(string); ok {
			return s.client.SetGroupColor(ctx, cmd.Target, color)
		}
		return fmt.Errorf("color parameter required")
	default:
		return fmt.Errorf("unknown group action: %s", cmd.Action)
	}
}

// executeSceneCommand executes a scene command
func (s *Scheduler) executeSceneCommand(ctx context.Context, cmd Command) error {
	if cmd.Action == "recall" || cmd.Action == "activate" {
		return s.client.ActivateScene(ctx, cmd.Target)
	}
	return fmt.Errorf("unknown scene action: %s", cmd.Action)
}
