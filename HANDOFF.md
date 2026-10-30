# Circadian lighting handoff

Updated 2026-10-30.

## Next session

The user asked to stop trying to find the Hue Bridge in this session. Resume that work next session.
Control must remain limited to Kitchen and Bedroom. Leave every other room untouched.

The last read-only request to the recorded Bridge address, 10.0.0.101, timed out. This does not prove
that the Bridge is offline or that its address is still correct. HUE_USERNAME and HUE_BRIDGE_IP were
absent from the shell environment. No physical lights were changed or tested.

Ask the user for the current Bridge address if it cannot be resolved from local configuration.
Have them configure credentials locally; do not ask them to paste credentials into chat. A
credential-like value exists in an unrelated, uncommitted README edit. Do not copy it into this
handoff or include that edit in the circadian commit.

Before enabling writes:

1. Read the real room, device, light, and entertainment resources.
2. Resolve exactly one Kitchen and one Bedroom and inspect their light identifiers. If the actual
   names differ, confirm the mapping rather than selecting another room by a partial name.
3. Check each selected light's dimming minimum, temperature limits, colour gamut, gradient features,
   and entertainment status. Verify exclusions and confirm that no other room's lights are selected.
4. Confirm the user's latitude/longitude, wake time, and bedtime. The current example uses
   Europe/Zurich, 07:00 wake, and 22:30 bedtime. Coordinates are deliberately unset; solar scheduling
   currently falls back to the configured fixed times. A time zone does not establish a location.
5. Start paused and review the selected lights before explicitly resuming. Test fades, minimum
   brightness, amber/red appearance, and white-only fallbacks on the selected rooms.
6. Test manual changes through the Hue app, switches, and existing controller commands. Confirm the
   two-hour hold, explicit resume, and that manually switched-off lights stay off after hold expiry.
7. Check disconnection/reconnection, scene recall, effects, and entertainment coexistence. Confirm
   that reconnecting does not write to a light moved out of either selected room.

## Implementation and local checks

Usage is in [CIRCADIAN.md](CIRCADIAN.md). Configuration is in
[config/circadian-example.json](config/circadian-example.json).

The engine supports six profiles, solar/fixed-time scheduling, smooth interpolation, device
fallbacks, manual holds, and pause/resume. It starts paused. Local sequence and batch ownership,
Bridge write errors, colour conversion, and event cancellation also received changes.

The following passed in this session before handoff preparation:

```sh
go test -race ./... -timeout 45s
go vet ./...
go build -o /tmp/hue-circadian .
/tmp/hue-circadian circadian validate --config config/circadian-example.json
/tmp/hue-circadian circadian preview --config config/circadian-example.json \
  --at 2026-10-30T19:15:00+01:00
```

These checks use mock Bridge responses and injected time. They do not establish hardware acceptance.
Rerun them after any changes next session.

## Remaining limits

- Exact Bridge request ceilings and maximum transition durations were not verified. Detailed Hue
  developer pages returned access errors. One write per second and ten-second Bridge fades are
  implementation choices pending hardware checks, not verified manufacturer limits.
- Gradient strips receive uniform colour. Ambient-sensor brightness adjustment and biological
  exposure calculations are deferred; the controller works without additional hardware.
- No independent Lumios spectral or flicker measurements were verified. The implementation makes
  no equivalence or therapeutic claims and cannot add dedicated near-infrared emission.
- External commands that match expected transition progress cannot always be distinguished from
  the controller's own changes. A separate process's private effect lifetime is unavailable.
- Restarting clears in-memory holds, so startup is paused unless explicitly resumed. No background
  process or automatic startup was installed.

## Existing work to preserve

Before this task, EFFECTS_GUIDE.md, README.md, cmd/resolver.go, cmd/root.go, cmd/stream.go, and main.go
already had uncommitted changes. CLAUDE.md, todos.md, and the hue executable were also untracked.
Keep those unrelated edits and files out of the circadian commit. README.md, cmd/stream.go, and
main.go contain both existing work and changes from this task, so stage their circadian changes
selectively. Do not discard or unstage the user's work.
