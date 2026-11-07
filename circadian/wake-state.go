package circadian

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// savedState is written beside the configuration. It lists the lights the schedule itself switched
// off for a colour-only phase, so they still come back on at wake after a restart.
type savedState struct {
	SwitchedOff []string `json:"switched_off"`
}

// StatePath names the state file for a configuration: config.json keeps its state in config.state.json.
func StatePath(configPath string) string {
	return strings.TrimSuffix(configPath, filepath.Ext(configPath)) + ".state.json"
}

// RememberSwitchedOff saves the schedule's switch-offs to path from now on and restores the saved
// list. A saved light that is on again, or no longer managed, is dropped: someone took it over while
// the controller was stopped. One turned on and off again in that time cannot be told apart and
// still comes back on at wake.
func (e *Engine) RememberSwitchedOff(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data, err = nil, nil
	}
	if err != nil {
		return fmt.Errorf("read circadian state: %w", err)
	}
	var saved savedState
	if len(data) > 0 {
		if err = json.Unmarshal(data, &saved); err != nil {
			err = fmt.Errorf("ignoring unreadable circadian state %s: %w", path, err)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.statePath = path
	e.stateDirty = err != nil // replace an unreadable file with the current list
	for _, id := range saved.SwitchedOff {
		if s := e.lights[id]; s != nil && !s.managed.Light.On.On {
			s.switchedOff = true
		} else {
			e.stateDirty = true // rewrite without the dropped light
		}
	}
	return err
}

// saveSwitchedOff writes the list if it changed. Only the event loop calls it, so writes never overlap.
func (e *Engine) saveSwitchedOff() error {
	e.mu.Lock()
	if e.statePath == "" || !e.stateDirty {
		e.mu.Unlock()
		return nil
	}
	state := savedState{SwitchedOff: []string{}}
	for id, s := range e.lights {
		if s.switchedOff {
			state.SwitchedOff = append(state.SwitchedOff, id)
		}
	}
	path := e.statePath
	e.stateDirty = false
	e.mu.Unlock()
	sort.Strings(state.SwitchedOff)
	if err := writeState(path, state); err != nil {
		e.mu.Lock()
		e.stateDirty = true
		e.mu.Unlock()
		return err
	}
	return nil
}

// writeState replaces the file in one rename, so a crash never leaves half a file.
func writeState(path string, state savedState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hue-state-*")
	if err != nil {
		return fmt.Errorf("save circadian state: %w", err)
	}
	defer file.Close()
	defer func() { _ = os.Remove(file.Name()) }() // fails harmlessly once renamed
	if err = file.Chmod(0600); err != nil {
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
