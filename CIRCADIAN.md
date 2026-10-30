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

To run inside the existing agent-facing server, set HUE_CIRCADIAN_CONFIG to the configuration path
before launching it. The tools circadian_status, circadian_pause, and circadian_resume control that
process's engine. It starts paused. A separate command process cannot resume this server's engine.

## Behaviour

Profiles specify a temperature or a colour, plus brightness. Room entries may override individual
profiles with their own profiles map. exclude_lights contains light identifiers to leave untouched.

The daily anchors are wake, daytime, late afternoon, sunset, evening, and bedtime. Daytime begins
at the later of wake plus one hour or sunrise plus its offset. Sunset includes its configured offset;
late afternoon is two hours earlier and evening is one hour later. Solar anchors are constrained to
the explicit waking interval: sunset is between wake plus four hours and bedtime minus two hours,
and daytime is no later than sunset minus three hours. Use preview to inspect seasonal results.
Waking intervals shorter than six hours are rejected.

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
Colour devices use their reported gamut. Gradient strips initially receive a uniform colour.

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
replayed. Changes to room/device resources pause the controller until restart and rediscovery.

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
