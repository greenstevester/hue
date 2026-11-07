# Circadian lighting handoff

Updated 2026-11-07.

## Current setup

The Bridge is reachable at 10.0.0.101. Its time zone is Europe/Zurich. The user selected Zurich as the
location and requested that wake follow sunrise. config/circadian-zurich.json uses city-centre
coordinates 47.3769 N, 8.5417 E, wake_time "sunrise", a zero sunrise offset, and 22:30 bedtime.
The calculated wake for 2026-11-08 is 07:18:45 local time; the published Zurich sunrise is about 07:19.

Only these lights may be controlled:

| Room | Light | Identifier |
|---|---|---|
| Kitchen | Kitchen-Island | d023cd29-fd34-4e13-b56a-dbd3357c2372 |
| Kitchen | kitchen-cupboards | b7c78373-ff8c-4f78-99dc-a3e05e183c21 |
| Bedroom | bed-platform | 44f68c6e-4fe3-4ca8-9f68-c706ccee752f |
| Bedroom | bedroom-window | dac5cd32-a924-480a-8ac2-9783f8254d30 |

Both kitchen lights and bed-platform are Hue lightstrip plus devices. bedroom-window is a
third-party extended-colour bulb. All four advertise colour control and temperature ranges. None
reports gradient capability. The strips report minimum brightness 0.04%; the window bulb does not
report a minimum. Do not infer its physical minimum from that omission.

## Entertainment correction

At the user's request, bed-platform was removed from both entertainment areas. The Bridge accepted
both updates and reported the strip back in normal mode. Entertainment area 1 is empty and inactive.
Entertainment area 2 retains its other light and remained active. Do not reattach bed-platform.

## Live verification and fixes

The bedroom-window bulb initially reported a connection issue. The user powered it on; the Bridge
then reported it connected and subsequent writes succeeded.

Live white-light testing revealed that the Bridge may announce the final target before reporting
intermediate fade values. The controller now accepts those expected values during the bounded fade
window instead of creating a false manual hold. The window bulb can report a white target as colour
coordinates with a null mirek. Expected white-colour acknowledgements are now recognised too.
Regression tests reproduced both failures before the fixes.

The corrected controller reached white targets without false holds. An external kitchen brightness
change created the normal two-hour hold. A separate temporary 15-second hold test expired and
returned the kitchen light to its target. That short duration is test-only; the Zurich configuration
retains two hours.

The manual-off test also passed after the temporary hold expired. Amber commands were accepted by
all four devices; the first bed-platform snapshot was still transitioning. All four subsequently
reported the red target at approximately 3.16% brightness. Subjective fade quality and actual Hue
app/switch presses still need user confirmation; external authenticated writes test the observation
path but do not prove those physical controls.

## Automatic startup

Installed local.hue.circadian as a macOS login service. Its binary, private configuration, and log
are under ~/Library/Application Support/Hue Circadian/. Its private startup file is
~/Library/LaunchAgents/local.hue.circadian.plist. Both configuration files have owner-only read/write
permissions. Credentials are local and must never be copied into repository files or chat.

The service runs circadian serve --resume --listen 0.0.0.0:8088 with the Zurich configuration
and the normal two-hour hold. Open http://127.0.0.1:8088 on the controller computer.
An initial network connection failure caused an exit; the service automatically retried and started
successfully. Inspect controller.log and service status when diagnosing future connection failures.
The service starts at login and requires the computer to remain awake for scheduled changes.

Pause and resume the running service with:

```sh
launchctl kill SIGUSR2 "gui/$(id -u)/local.hue.circadian"
launchctl kill SIGUSR1 "gui/$(id -u)/local.hue.circadian"
```

Resuming with SIGUSR1 clears every manual hold. The web page's switch keeps them.

## Verification commands

```sh
go test -race ./... -timeout 45s
go vet ./...
go build -o /tmp/hue-live-controller .
/tmp/hue-live-controller circadian validate --config config/circadian-zurich.json
/tmp/hue-live-controller circadian preview --config config/circadian-zurich.json --schedule
```

The race tests, static checks, build, validation, and schedule preview passed after the live fixes
and sunrise-wake change. Recheck after further edits. Usage is in CIRCADIAN.md.

## Update the running Mac controller

Run these commands from the repository directory after approving the code changes. They build and
check the new binary, replace the installed executable, and restart the existing login service.
The private configuration and startup credentials remain in place. Do not copy the repository's
example configuration over the live file: dashboard settings are saved in that live file.

```sh
go test -race ./... -timeout 45s &&
go vet ./... &&
go build -trimpath -o /tmp/hue-circadian-update . &&
/tmp/hue-circadian-update circadian validate \
  --config "$HOME/Library/Application Support/Hue Circadian/config.json" &&
install -m 755 /tmp/hue-circadian-update \
  "$HOME/Library/Application Support/Hue Circadian/hue-circadian.new" &&
mv "$HOME/Library/Application Support/Hue Circadian/hue-circadian.new" \
  "$HOME/Library/Application Support/Hue Circadian/hue-circadian" &&
launchctl kickstart -k "gui/$(id -u)/local.hue.circadian"
```

Check the page and controller health after restart:

```sh
curl --connect-timeout 3 --max-time 10 -fsS http://127.0.0.1:8088/api/status |
  jq '{healthy, enabled, wake_time, bedtime, lights: [.lights[] | {name, enabled, on}]}'
tail -n 30 "$HOME/Library/Application Support/Hue Circadian/controller.log"
```

If the first connection fails, inspect the log and allow the login service to retry. A successful
page response alone does not prove Bridge connectivity; healthy must be true. Enabled may be false
if the user paused scheduling. Check http://127.0.0.1:8088 locally and the Mac's current home-network
address from the iPad; the last verified address was http://10.0.0.129:8088.

## Build and upload for Raspberry Pi

Read the Raspberry Pi section in CIRCADIAN.md and deploy-pi.sh first. The target is the existing
32-bit ARMv7 Pi, stevepi@10.0.0.126, using ~/.ssh/id_ed25519_raspberrypi002. Build without uploading:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -o /tmp/hue-pi .
```

For the first upload, prepare the ignored .env using .env.example and the existing private Bridge
credential, then run ./deploy-pi.sh. The script refuses to replace an existing Pi configuration and
does not start the controller. It uploads to /home/stevepi/hue. A later update requires reviewing the
installed configuration before replacing files; the script is not an unattended update mechanism.
Installing automatic startup on the Pi has not been implemented. Agree on stopping the Mac owner
before starting scheduling on the Pi. Do not deploy or start a second owner merely to check a build.

## Remaining limits and work to preserve

The embedded web page now shows all four lights, observed state, manual holds, and daily phase times.
It saves global and per-light scheduling switches, sunrise or fixed wake time, and bedtime to the
private configuration. Disabling a light's schedule leaves its physical state unchanged. Excluded
lights remain visible. Live checks verified saved settings after a service restart, then restored
sunrise waking, 22:30 bedtime, and scheduling enabled for all four lights. Safari clicks verified
global and individual switches and the timing form. Automated race tests and static checks passed.

The web server and assets are in webui/. The serve command shares the existing controller process.
run-web.sh and deploy-pi.sh follow Fundpuppet's single-binary pattern. The ARMv7 build succeeded and
an earlier web build ran its help command on the actual 32-bit Pi. The Pi controller has not been
started or installed as a service; the Mac remains the automation owner. Never run both owners.

Exact manufacturer request ceilings were not verified. One write per second and short Bridge fades
are implementation choices; observed success does not establish the maximum supported rate.
No independent Lumios spectral/flicker measurements were verified. No equivalence, therapeutic,
or dedicated near-infrared capability is claimed. Ambient-sensor adjustment and gradient segments
remain outside this implementation.

External changes indistinguishable from an expected fade cannot always be attributed perfectly.
In-memory holds reset on restart; ordinary startup remains paused unless explicitly resumed.
Automatic startup, when installed, uses --resume. The computer must remain awake for scheduling.

The initial implementation commit is e978a97. Dashboard controls, live-test fixes, sunrise waking,
and Pi build/upload scripts were committed and pushed to origin/main in fb1bb55. The subsequent
housekeeping change includes the startup instructions, deployment handoff, earlier command fixes,
project guidance, and rebuilt Mac executable. The README credential example uses a placeholder.
Check git status at session startup rather than assuming the staging area is empty. Preserve any
existing changes and ask before committing. Do not expose or commit credentials. Temporary
live-test connection files are in a private directory recorded by /tmp/hue-live-path.
