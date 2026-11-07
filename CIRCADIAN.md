# Kitchen and bedroom lighting

Circadian control is opt-in and limited to rooms named Kitchen and Bedroom, matched without regard
to letter case. Both must resolve exactly once. No other room is eligible. Optional room identifiers
must match the corresponding room name. Room membership is checked again during operation;
changes require a restart and fresh discovery.

## Try the schedule without changing lights

```sh
go run . circadian validate --config config/circadian-example.json
go run . circadian preview --config config/circadian-example.json \
  --at 2026-10-30T19:15:00+01:00
```

These commands do not need Bridge credentials or contact the Bridge. The example uses 07:00 wake,
22:30 bedtime, and Europe/Zurich. Set both latitude and longitude to use solar times. With neither
coordinate present, the configured fixed sunrise and sunset times apply. An absent polar sunrise or
sunset also uses the corresponding fixed time.

Set wake_time to "sunrise" to start the morning phase at local sunrise each day. This requires
coordinates; sunrise_offset also adjusts that wake time. For Zurich, use
[config/circadian-zurich.json](config/circadian-zurich.json), which uses city-centre coordinates
47.3769 N, 8.5417 E and retains the 22:30 bedtime. Preview its daily boundaries with:

```sh
go run . circadian preview --config config/circadian-zurich.json --schedule
```

The solar calculation follows the [NOAA equations](https://gml.noaa.gov/grad/solcalc/solareqns.PDF).
It estimates apparent sunrise and sunset using a 90.833-degree zenith; terrain and weather are not
modelled. The calculation is scheduling guidance, not an astronomical measurement service.

## Run the controller

Keep the existing HUE_BRIDGE_IP and HUE_USERNAME environment variables configured locally. Then run:

```sh
go run . circadian run --config config/circadian-example.json
```

The process prints selected lights and starts paused. Send it USR1 to resume and USR2 to pause:

```sh
kill -USR1 <process-id>
kill -USR2 <process-id>
```

Use the process identifier of the controller binary, not the parent `go run` process. For unattended
use, build a binary and run it directly. `run --resume` explicitly resumes on that start. Restarting
without that flag starts paused, so restarting cannot silently erase a manual decision. Stop with
Control-C or TERM. The computer must stay awake. A local lock prevents a second owner for the same
Bridge address; do not run an owner on another computer at the same time.

Resuming clears every manual hold, so lights you changed by hand return to the schedule within
seconds. That applies to USR1, `run --resume` and the circadian_resume tool. The web page's switch
keeps manual holds. Windows has no USR1 or USR2; use the web page or the agent tools there.

To run inside the existing agent-facing server, set HUE_CIRCADIAN_CONFIG to the configuration path
before launching it. The tools circadian_status, circadian_pause, and circadian_resume control that
process's engine. It starts paused. A separate command process cannot resume this server's engine.

The installed macOS login service is local.hue.circadian. It runs the Zurich configuration from
~/Library/Application Support/Hue Circadian/config.json and writes controller.log in that directory.
It resumes automatically on login and retries after a process failure. Pause or resume it with:

```sh
launchctl kill SIGUSR2 "gui/$(id -u)/local.hue.circadian"
launchctl kill SIGUSR1 "gui/$(id -u)/local.hue.circadian"
```

## Web interface

The web page shows current light states, manual holds, and today's phase times. Its automatic-lighting
switch pauses or enables scheduling and saves that choice across restarts. Disabling automation
leaves the lights at their current settings. Manual holds keep priority when scheduling is enabled.

Each light has its own scheduling switch. Turning that switch off excludes the light from automatic
changes and leaves its current power and brightness unchanged. The light remains visible on the page.
The timing form lets you choose sunrise or a fixed wake time and change bedtime. These choices and
the individual scheduling switches are saved across restarts.

The page saves every change into the configuration file it was started with. To try it locally, copy
the Zurich configuration first so the tracked file stays unchanged:

```sh
cp config/circadian-zurich.json config/circadian-zurich.local.json
go run . circadian serve --config config/circadian-zurich.local.json --resume
```

Git ignores files ending in `.local.json`.

Open http://127.0.0.1:8088. To make it available on your home network, add
`--listen 0.0.0.0:8088` and use the controller computer's address. The same process runs the scheduler
and serves the page. Its HTML, styles, and JavaScript are embedded in the Go binary; no browser
assets are downloaded and no frontend build step is required. Bridge credentials stay on the server.

The page has no login. Anyone who can reach it on your network can switch automation on or off,
change wake and bed times, and exclude lights. The server refuses requests sent to other host names
and changes sent from other websites, so a website you visit cannot change your settings. It accepts
the controller's current addresses, so a new address from your router works without a restart.

Saving controls requires the configuration file's directory to be writable. The web interface remains
available when automation is disabled.

### Raspberry Pi

The build and upload follow Fundpuppet's Go/SSH pattern and its existing ARMv7 Pi target.

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o /tmp/hue-pi .
```

For an upload, create an ignored .env from .env.example with your local Bridge credential, then run
`./deploy-pi.sh`. It copies the binary, private environment file, disabled configuration, and
run-web.sh to stevepi@10.0.0.126:/home/stevepi/hue. When a configuration is already there, later runs
replace only the binary and run-web.sh and keep the Pi's configuration, saved state and .env. The
new binary must accept that configuration before it replaces the old one. PI_HOST, PI_KEY and
PI_DIR may override the host, key path and folder. Nothing is started or restarted automatically.

On the Pi, run `./run-web.sh` to view http://10.0.0.126:8088 with automation disabled. Choose one
controller host before enabling scheduling. For a 64-bit Pi operating system, build with
GOARCH=arm64 and omit GOARM.

## Behaviour

Profiles specify a temperature or a colour, plus brightness. Room entries may override individual
profiles with their own profiles map. exclude_lights contains light identifiers to leave untouched.

The daily anchors are wake, daytime, late afternoon, sunset, evening, and bedtime. Daytime begins
at the later of wake plus one hour or sunrise plus its offset. Sunset includes its configured offset;
late afternoon is two hours earlier and evening is one hour later. Solar anchors are constrained to
the explicit waking interval: sunset is between wake plus four hours and bedtime minus two hours,
and daytime is no later than sunset minus three hours. Use preview to inspect seasonal results.
Waking intervals shorter than six hours are rejected.

For sunrise waking, the waking interval varies with the seasons. If it is shorter than six hours,
the intermediate phase spacing contracts to fit without moving the sunrise wake time.

Each phase blends from the previous profile over transition, shortened if the preceding interval is
shorter. The smoothstep curve controls brightness and temperature or colour. The Bridge performs
short, ten-second fades between calculated targets. The controller sends at most one update per
second and skips targets that have not changed enough. That rate is a conservative implementation
choice; it is not a claim about an official Bridge limit.

Local calendar dates determine the schedule. Missing daylight-saving clock times move to the first
valid minute afterward. Repeated clock times use their first occurrence. Overnight waking periods
are supported. The override duration measures elapsed time.

The controller adapts lights already on. It does not turn a manually extinguished room back on when
a hold expires. White Ambiance devices use their advertised temperature limits and switch off for
colour-only phases. Dimming-only devices follow brightness during white phases and switch off for
colour-only phases. A light switched off by that capability fallback can return during a white phase.
The controller saves those switch-offs in a file beside its configuration (config.state.json for
config.json), so such a light still comes back at wake after a restart. At startup it drops any saved
light that is already on again. It cannot see a light that someone switched on and off again while
it was stopped, so that light also comes back at wake. Colour devices use their reported gamut. Gradient strips initially receive a uniform colour.

External changed light values create a hold for override_duration, two hours by default. Another
manual change restarts that duration. Local scenes, sequences, and batches reserve control for their
operation, followed by a hold. Active entertainment and reported native effects suspend adaptation.
An external effect in a separate process is observed through its light changes; its private job
lifetime is unavailable to this controller.

Hue events do not reliably identify who issued a command. The controller compares changed fields
with its pending target and expected fade progress. An external command indistinguishable from that
progress cannot be attributed perfectly. Explicit pause is appropriate for long external sessions.

Observation errors stop automation writes. Reconnection requires fresh lights, rooms, devices, and
entertainment state. State is refreshed periodically as a backstop. Missed daily phases are not
replayed. Adding or removing a light, or changing a room or device, pauses the controller until
restart and rediscovery. Adding or deleting scenes, zones or automations does not pause it.

## Light appearance and measurement

Temperature describes white appearance; it does not specify the emitted spectrum. A colour command
also does not specify a wavelength or treatment dose. Brightness percentages are device settings,
not measured illuminance. Equal percentages across bulbs need not produce equal light levels.

This controller approximates a daily lighting experience. It cannot add dedicated near-infrared
emission or reproduce Lumios spectra, colour rendering, flicker characteristics, or biological
outcomes. No independent Lumios measurement has been used to validate equivalence. Ambient sensors,
segment-specific gradients, and biological exposure calculations are outside this implementation.

## Verification

```sh
go test -race ./... -timeout 45s
go vet ./...
```

The tests use mock Bridge responses and an injected clock for scheduling and override behaviour.
Hardware acceptance still requires reading the real room inventory and checking transitions,
minimum brightness, and external controls on the installed kitchen and bedroom lights. The example
configuration does not enable any background process by itself.
