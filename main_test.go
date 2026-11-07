package main

import "testing"

func TestCommandsThatWorkWithoutBridgeCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"discover"}, {"help"}, {"--help"}, {"-h"}, {"lights", "--help"},
		{"completion", "bash"}, {"completion", "zsh"},
		{"__complete", "lights", ""}, {"__completeNoDesc", "li"},
	} {
		if !runsWithoutBridge(args) {
			t.Errorf("%v asks for Bridge credentials", args)
		}
	}
}

func TestBridgeCommandsStillNeedCredentials(t *testing.T) {
	for _, args := range [][]string{{"lights", "list"}, {"stream"}, {"hue-scenes", "activate", "Relax"}} {
		if runsWithoutBridge(args) {
			t.Errorf("%v would run without a Bridge client", args)
		}
	}
}
