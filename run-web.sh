#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
set -a
. ./.env
set +a
exec ./hue circadian serve --config config.json --listen 0.0.0.0:8088 --resume
