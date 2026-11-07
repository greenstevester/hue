package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kungfusheep/hue/circadian"
	"github.com/kungfusheep/hue/webui"
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
	var showSchedule bool
	preview.Flags().StringVar(&at, "at", "", "RFC3339 timestamp (default: now)")
	preview.Flags().BoolVar(&showSchedule, "schedule", false, "Show the selected day's lighting times")
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
		if showSchedule {
			return json.NewEncoder(os.Stdout).Encode(circadian.ScheduleForDay(c, now))
		}
		targets := map[string]circadian.Target{}
		for _, room := range c.Rooms {
			targets[room.Name] = circadian.TargetAt(c, now, room)
		}
		return json.NewEncoder(os.Stdout).Encode(targets)
	}
	command.AddCommand(preview)
	var resume bool
	var listen string
	run := &cobra.Command{Use: "run", Aliases: []string{"serve"},
		Short: "Run until interrupted; starts paused unless --resume is supplied"}
	run.Flags().BoolVar(&resume, "resume", false, "Explicitly resume control on this start")
	run.Flags().StringVar(&listen, "listen", "", "Web interface address (serve defaults to 127.0.0.1:8088)")
	run.RunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.CalledAs() == "serve" && listen == "" {
			listen = "127.0.0.1:8088"
		}
		c, err := load()
		if err != nil {
			return err
		}
		if !c.Enabled && listen == "" {
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
		var webDone chan error
		if listen != "" {
			handler, err := webui.New(engine, c, path, time.Now)
			if err != nil {
				return err
			}
			listener, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("start web interface: %w", err)
			}
			webServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
				MaxHeaderBytes: 8192}
			webDone = make(chan error, 1)
			go func() {
				err := webServer.Serve(listener)
				if errors.Is(err, http.ErrServerClosed) {
					err = nil
				}
				webDone <- err
			}()
			defer func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutdownCancel()
				if err := webServer.Shutdown(shutdownCtx); err != nil {
					log.Printf("stop web interface: %v", err)
				}
			}()
			log.Printf("Web interface listening on %s", listener.Addr())
		}
		if resume {
			engine.Resume()
		}
		if resumeSignal == nil {
			log.Printf("circadian paused=%t", !resume)
		} else {
			log.Printf("circadian paused=%t; send USR1 to resume or USR2 to pause", !resume)
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, resumeSignal, pauseSignal)
			defer signal.Stop(signals)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case sig := <-signals:
						if sig == resumeSignal {
							engine.Resume()
							log.Print("circadian resumed")
						} else {
							engine.Pause()
							log.Print("circadian paused")
						}
					}
				}
			}()
		}
		select {
		case err := <-done:
			return err
		case err := <-webDone:
			return err
		}
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
