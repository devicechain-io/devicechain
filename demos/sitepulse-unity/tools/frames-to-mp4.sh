#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Turns a PNG sequence from an offline render (-sitepulse-render) into an mp4.
#
#   tools/frames-to-mp4.sh <render-out-dir> [fps]        every shot under the render's output directory
#   tools/frames-to-mp4.sh <shot-dir> [fps] [out.mp4]    one shot's frames
#
# The frame rate defaults to the one render.json records (never guessed from the file names). The mp4 is H.264, yuv420p
# (plays everywhere), no audio. ffmpeg is not installed with this repository: when it is missing this says so and exits 3
# without touching anything; install it in WSL with `sudo apt install ffmpeg`.
set -euo pipefail

usage() { sed -n '4,13p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
[ $# -ge 1 ] || usage
src="$1"
[ -d "$src" ] || { echo "frames-to-mp4: $src is not a directory" >&2; exit 2; }

if ! command -v ffmpeg >/dev/null 2>&1; then
	echo "frames-to-mp4: ffmpeg is not installed (WSL: sudo apt install ffmpeg). The PNG sequences are untouched." >&2
	exit 3
fi

fps="${2:-}"
if [ -z "$fps" ] && [ -f "$src/render.json" ]; then
	fps="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["fps"])' "$src/render.json")"
fi
case "$fps" in
	'' ) echo "frames-to-mp4: no frame rate: pass one, or point at a render directory that has render.json" >&2; exit 2 ;;
	*[!0-9]* ) echo "frames-to-mp4: fps \"$fps\" is not a whole number" >&2; exit 2 ;;
esac

encode() {
	local dir="$1" out="$2"
	[ -f "$dir/frame_000001.png" ] || { echo "frames-to-mp4: $dir holds no frame_000001.png" >&2; return 1; }
	local n
	n="$(find "$dir" -maxdepth 1 -name 'frame_*.png' | wc -l)"
	echo "frames-to-mp4: $dir: $n frames at $fps fps -> $out"
	ffmpeg -nostdin -loglevel error -y -framerate "$fps" -i "$dir/frame_%06d.png" \
		-c:v libx264 -pix_fmt yuv420p -crf 18 -movflags +faststart "$out"
}

if [ -f "$src/frame_000001.png" ]; then
	encode "$src" "${3:-$src/$(basename "$src").mp4}"
	exit
fi

shopt -s nullglob
rc=0
found=0
for d in "$src"/*/; do
	d="${d%/}"
	[ -f "$d/frame_000001.png" ] || continue
	found=1
	encode "$d" "$src/$(basename "$d").mp4" || rc=1
done
[ "$found" = 1 ] || { echo "frames-to-mp4: $src holds no shot directories with frames" >&2; exit 1; }
exit "$rc"
