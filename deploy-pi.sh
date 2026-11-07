#!/usr/bin/env bash
# Follow Fundpuppet's ARMv7 build and SSH-copy pattern. Do not start a second controller.
set -euo pipefail

PI_HOST="${PI_HOST:-stevepi@10.0.0.126}"
PI_KEY="${PI_KEY:-$HOME/.ssh/id_ed25519_raspberrypi002}"
PI_DIR="${PI_DIR:-/home/stevepi/hue}"
GO="${GO:-/opt/homebrew/bin/go}"
[ -x "$GO" ] || GO=go
CONFIG="${1:-config/circadian-zurich.json}"

if [ ! -f .env ]; then
  printf 'Create .env from .env.example and set the Bridge credential locally.\n' >&2
  exit 1
fi
if [ ! -f "$CONFIG" ]; then
  printf 'Configuration not found: %s\n' "$CONFIG" >&2
  exit 1
fi
STAGING=$(mktemp -d)
trap 'rm -f "$STAGING/hue" "$STAGING/config.json"; rmdir "$STAGING"' EXIT

printf 'Building one binary for the Raspberry Pi 3 (ARMv7)…\n'
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 "$GO" build -trimpath -o "$STAGING/hue" .
# New Pi uploads start disabled. The web switch persists the user's later choice.
sed 's/"enabled":[[:space:]]*true/"enabled": false/' "$CONFIG" > "$STAGING/config.json"

SSH=(ssh -i "$PI_KEY" -o BatchMode=yes -o ConnectTimeout=10 "$PI_HOST")
SCP=(scp -i "$PI_KEY" -o BatchMode=yes -o ConnectTimeout=10)
# A configuration already on the Pi holds choices saved from the web page, so later runs keep it
# and the Pi's .env, and replace only the program and run-web.sh.
MODE=$("${SSH[@]}" "if [ -e '$PI_DIR/config.json' ]; then echo update; else echo new; fi")
"${SSH[@]}" "mkdir -p '$PI_DIR' && chmod 700 '$PI_DIR'"
# Upload beside the old program, then rename: Linux refuses to overwrite a running program's file.
"${SCP[@]}" "$STAGING/hue" "$PI_HOST:$PI_DIR/hue.new"
"${SCP[@]}" run-web.sh "$PI_HOST:$PI_DIR/"
if [ "$MODE" = new ]; then
  "${SCP[@]}" "$STAGING/config.json" "$PI_HOST:$PI_DIR/config.json"
  "${SCP[@]}" .env "$PI_HOST:$PI_DIR/"
fi
# Check the Pi's configuration with the new program before it replaces the old one.
"${SSH[@]}" "chmod 600 '$PI_DIR/.env' '$PI_DIR/config.json' &&
chmod 755 '$PI_DIR/hue.new' '$PI_DIR/run-web.sh' &&
'$PI_DIR/hue.new' circadian validate --config '$PI_DIR/config.json' &&
mv '$PI_DIR/hue.new' '$PI_DIR/hue'"
if [ "$MODE" = new ]; then
  printf 'Uploaded to %s:%s. Nothing has been started.\n' "$PI_HOST" "$PI_DIR"
  printf 'Run ./run-web.sh on the Pi to view http://%s:8088 with automation disabled.\n' "${PI_HOST#*@}"
  printf 'Choose one controller host before enabling automation.\n'
else
  printf 'Replaced the program in %s:%s and kept its configuration and .env.\n' "$PI_HOST" "$PI_DIR"
  printf 'Restart run-web.sh on the Pi to use the new program.\n'
fi
