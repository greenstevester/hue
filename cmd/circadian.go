package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kungfusheep/hue/circadian"
	"github.com/spf13/cobra"
)

func newCircadianCommand() *cobra.Command {
	var path string
	command := &cobra.Command{Use: "circadian", Short: "Adaptive lighting for Kitchen and Bedroom"}
	command.PersistentFlags().StringVar(&path, "config", os.Getenv("HUE_CIRCADIAN_CONFIG"), "Circadian JSON configuration")
	load := func() (circadian.Config, error) {
		if path == "" {
			return circadian.Config{}, fmt.Errorf("supply --config or HUE_CIRCADIAN_CONFIG")
		}
		return circadian.LoadConfig(path)
	}
	command.AddCommand(&cobra.Command{Use: "validate", Short: "Validate configuration without contacting the Bridge",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := load()
			if err == nil {
				fmt.Println("Configuration valid: Kitchen and Bedroom only")
			}
			return err
		}})
	preview := &cobra.Command{Use: "preview", Short: "Show current room targets without changing lights"}
	var at string
	preview.Flags().StringVar(&at, "at", "", "RFC3339 timestamp (default: now)")
	preview.RunE = func(_ *cobra.Command, _ []string) error {
		c, err := load()
		if err != nil {
			return err
		}
		now := time.Now()
		if at != "" {
			now, err = time.Parse(time.RFC3339, at)
			if err != nil {
				return err
			}
		}
		targets := map[string]circadian.Target{}
		for _, room := range c.Rooms {
			targets[room.Name] = circadian.TargetAt(c, now, room)
		}
		return json.NewEncoder(os.Stdout).Encode(targets)
	}
	command.AddCommand(preview)
	var resume bool
	run := &cobra.Command{Use: "run", Short: "Run until interrupted; starts paused unless --resume is supplied"}
	run.Flags().BoolVar(&resume, "resume", false, "Explicitly resume control on this start")
	run.RunE = func(cmd *cobra.Command, _ []string) error {
		c, err := load()
		if err != nil {
			return err
		}
		if !c.Enabled {
			return fmt.Errorf("circadian control is disabled in configuration")
		}
		if hueClient == nil {
			return fmt.Errorf("Bridge client is required")
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		engine, done, stop, err := circadian.Start(ctx, hueClient, c, log.Printf)
		if err != nil {
			return err
		}
		defer stop()
		if resume {
			engine.Resume()
		}
		log.Printf("circadian paused=%t; send USR1 to resume or USR2 to pause", !resume)
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGUSR1, syscall.SIGUSR2)
		defer signal.Stop(signals)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case sig := <-signals:
					if sig == syscall.SIGUSR1 {
						engine.Resume()
						log.Print("circadian resumed")
					} else {
						engine.Pause()
						log.Print("circadian paused")
					}
				}
			}
		}()
		return <-done
	}
	command.AddCommand(run)
	return command
}

func init() { rootCmd.AddCommand(newCircadianCommand()) }

// CircadianNeedsBridge keeps configuration checks and help independent of the Bridge.
func CircadianNeedsBridge(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return false
		}
	}
	command, _, err := newCircadianCommand().Find(args)
	return err == nil && command.Name() == "run"
}
