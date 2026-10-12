#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Burns the DeviceChain corner badge into a video, an animated WebP or a still, so footage that leaves the project
# says whose work it is. The badge is branding/logos/video-badge.png (the horizontal lockup on a translucent dark
# plate, so the white "Device" wordmark reads over bright footage too).
#
#   branding/tools/brand-video.sh [options] <input> <output>
#
#   --logo FILE        badge PNG (default: branding/logos/video-badge.png)
#   --height-pct N     badge height as a percentage of the frame height (default 5.6; ~40 px at 720p)
#   --margin-pct N     distance from the top and left edges, same unit (default 2.8; ~20 px at 720p)
#   --width N          scale the output to this width first (keeps the aspect ratio; default: the input's size)
#   --fps N            resample the frame rate (default: the input's)
#   --duration SECS    keep only the first SECS seconds
#   --crf N            quality for .mp4 (x264, default 23) and .webm (VP9, default 33); lower is better and larger
#   --quality N        quality for .webp and .jpg: 0-100 for WebP (default 75), 2-31 for JPEG (default 2); WebP is
#                      the other way round from --crf, higher is better and larger
#
# The output format follows the output extension: .mp4 (H.264, yuv420p, faststart), .webm (VP9), .webp (animated,
# looping forever), .jpg or .png (one frame, the first). Writing over the input is refused: write a branded copy,
# and keep the clean master, because branding an already-branded file would stack a second badge.
#
# Re-branding is deterministic from the clean source. The badge PNG is a rasterization of video-badge.svg at 1280 px
# wide; regenerate it after editing the SVG with any rasterizer, e.g. `npm i @resvg/resvg-js` and
# `new Resvg(svg, {fitTo: {mode: 'width', value: 1280}}).render().asPng()`.
#
# ffmpeg (with libx264, libvpx-vp9 and libwebp for the formats you ask for) must be on PATH; when it is missing this
# says so and exits 3 without touching anything.
set -euo pipefail

usage() { sed -n '4,27p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
logo="$here/../logos/video-badge.png"
height_pct=5.6
margin_pct=2.8
width=""
fps=""
duration=""
crf=""
quality=""

num() { case "$2" in '' | *[!0-9.]* | *.*.* | .) echo "brand-video: $1 \"$2\" is not a number" >&2; exit 2 ;; esac; }

while [ $# -gt 0 ]; do
	case "$1" in
		--logo) [ $# -ge 2 ] || usage; logo="$2"; shift 2 ;;
		--height-pct) [ $# -ge 2 ] || usage; height_pct="$2"; shift 2 ;;
		--margin-pct) [ $# -ge 2 ] || usage; margin_pct="$2"; shift 2 ;;
		--width) [ $# -ge 2 ] || usage; width="$2"; shift 2 ;;
		--fps) [ $# -ge 2 ] || usage; fps="$2"; shift 2 ;;
		--duration) [ $# -ge 2 ] || usage; duration="$2"; shift 2 ;;
		--crf) [ $# -ge 2 ] || usage; crf="$2"; shift 2 ;;
		--quality) [ $# -ge 2 ] || usage; quality="$2"; shift 2 ;;
		-h | --help) usage ;;
		--) shift; break ;;
		-*) echo "brand-video: unknown option $1" >&2; usage ;;
		*) break ;;
	esac
done
[ $# -eq 2 ] || usage
in="$1"
out="$2"

num --height-pct "$height_pct"
num --margin-pct "$margin_pct"
[ -z "$width" ] || num --width "$width"
[ -z "$fps" ] || num --fps "$fps"
[ -z "$duration" ] || num --duration "$duration"
[ -z "$crf" ] || num --crf "$crf"
[ -z "$quality" ] || num --quality "$quality"

[ -f "$in" ] || { echo "brand-video: $in is not a file" >&2; exit 2; }
[ -f "$logo" ] || { echo "brand-video: the badge $logo does not exist" >&2; exit 2; }
if [ "$(cd "$(dirname "$in")" && pwd)/$(basename "$in")" = "$(cd "$(dirname "$out")" && pwd)/$(basename "$out")" ]; then
	echo "brand-video: input and output are the same file; write a branded copy" >&2
	exit 2
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
	echo "brand-video: ffmpeg is not installed. Nothing was written." >&2
	exit 3
fi

# The badge scales with the frame, so the same percentages brand a 720p loop and a 1080p master alike. The frame
# size is taken AFTER --width, because the badge must be sized against what is actually written.
if [ -n "$width" ]; then
	frame_w="$width"
	frame_h="$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 "$in" |
		awk -F, -v w="$width" '{ printf "%d", w * $2 / $1 }')"
else
	size="$(ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 "$in")"
	frame_w="${size%%,*}"
	frame_h="${size##*,}"
fi
[ -n "${frame_h:-}" ] && [ "$frame_h" -gt 0 ] || { echo "brand-video: cannot read the frame size of $in" >&2; exit 1; }
badge_h="$(awk -v h="$frame_h" -v p="$height_pct" 'BEGIN { v = int(h * p / 100 + 0.5); if (v % 2) v++; print v }')"
margin="$(awk -v h="$frame_h" -v p="$margin_pct" 'BEGIN { print int(h * p / 100 + 0.5) }')"

pre=""
[ -z "$width" ] || pre="scale=${width}:-2:flags=lanczos,"
[ -z "$fps" ] || pre="${pre}fps=${fps},"
filter="[0:v]${pre}format=rgba[base];[1:v]scale=-2:${badge_h}:flags=lanczos[badge];[base][badge]overlay=${margin}:${margin}:format=auto,format=yuv420p"

args=(-nostdin -loglevel error -y)
[ -z "$duration" ] || args+=(-t "$duration")
args+=(-i "$in" -i "$logo" -filter_complex "$filter")

case "${out,,}" in
	*.mp4) args+=(-c:v libx264 -preset slow -crf "${crf:-23}" -pix_fmt yuv420p -movflags +faststart -an) ;;
	*.webm) args+=(-c:v libvpx-vp9 -b:v 0 -crf "${crf:-33}" -row-mt 1 -pix_fmt yuv420p -an) ;;
	*.webp) args+=(-c:v libwebp_anim -lossless 0 -quality "${quality:-75}" -compression_level 6 -loop 0 -an) ;;
	*.jpg | *.jpeg) args+=(-frames:v 1 -q:v "${quality:-2}" -update 1) ;;
	*.png) args+=(-frames:v 1 -update 1) ;;
	*) echo "brand-video: unsupported output extension in $out (want .mp4 .webm .webp .jpg .png)" >&2; exit 2 ;;
esac

echo "brand-video: $in -> $out (badge ${badge_h} px high, ${margin} px from the top-left of a ${frame_w}x${frame_h} frame)"
ffmpeg "${args[@]}" "$out"
