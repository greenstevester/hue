package cmd

import (
	"path/filepath"
	"testing"
)

func TestCircadianValidationAndPreviewNeverNeedBridgeClient(t *testing.T) {
	for _, args := range [][]string{
		{"--config", filepath.Join("..", "config", "circadian-example.json"), "validate"},
		{"--config", filepath.Join("..", "config", "circadian-example.json"), "preview", "--at", "2026-10-30T12:00:00+01:00"},
	} {
		command := newCircadianCommand()
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCircadianRejectsMissingConfigAndInvalidPreviewTime(t *testing.T) {
	for _, args := range [][]string{
		{"--config", "", "validate"},
		{"--config", filepath.Join("..", "config", "circadian-example.json"), "preview", "--at", "tomorrow"},
	} {
		command := newCircadianCommand()
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatal("invalid command accepted")
		}
	}
}

func TestCircadianBridgeRequirementAcceptsFlagsBeforeSubcommand(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{[]string{"run"}, true}, {[]string{"--config", "file.json", "run"}, true},
		{[]string{"run", "--help"}, false}, {[]string{"preview"}, false}, {[]string{"validate"}, false},
	} {
		if got := CircadianNeedsBridge(test.args); got != test.want {
			t.Fatalf("%v: %v", test.args, got)
		}
	}
}
