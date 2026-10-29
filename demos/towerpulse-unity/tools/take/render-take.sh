#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# render-take.sh -- turns two RECORDED runs into the Towerpulse cut and the website loop, from Git Bash on Windows.
#
#   render-take.sh plan   --run-a DIR --run-b DIR            writes tools/take/take-plan.json from what the two recordings say happened
#   render-take.sh render --run-a DIR --run-b DIR            replays each run through the player's frame renderer and encodes every shot to an mp4
#   render-take.sh owner  --stills DIR                       the annex owner's part (10 s) from console_stills.py's PNGs: devices, then alarms
#   render-take.sh cut    [--owner-clip FILE]                joins the shots (and the console's part, if there is one) into the 90-second cut
#   render-take.sh loop                                      the 25-second website loop, from the same footage, as mp4 and webp
#   render-take.sh sheet                                     a contact sheet of sampled frames, one row per shot
#
# Everything is written under Build/take/ (ignored by git): frames/ (deleted after each shot is encoded, only the sampled frames stay in samples/),
# shots/<id>.mp4, towerpulse-cut.mp4, towerpulse-loop.mp4, towerpulse-loop.webp, contact-sheet.jpg.
#
# The player is Build/Towerpulse.exe, started once per run with -tp-replay (the recording), -tp-render (the plan), -tp-run A|B and -tp-out. There is no
# platform and no network: a take is rendered from what a run recorded, the HUD says REPLAY and the run's id, and every shot whose clock jumps says so.
# Needs ffmpeg and python on PATH. 1920x1080, 30 fps, H.264 yuv420p, no audio. The loop is sized like the Sitepulse one (720x406, 10 fps webp).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="$(cd "$HERE/../.." && pwd)"
PLAYER="$PROJECT/Build/Towerpulse.exe"
OUT="${TAKE_OUT:-$PROJECT/Build/take}"
PLAN="$HERE/take-plan.json"
FPS=30
WIDTH=1920
HEIGHT=1080
SAMPLE_EVERY_S=1

log() { printf '[take] %s\n' "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }
win() { cygpath -ma "$1"; }
need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required on PATH"; }

cmd="${1:-}"
[ -n "$cmd" ] || { sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 2; }
shift
RUN_A="" RUN_B="" OWNER_CLIP="" STILLS=""
while [ $# -gt 0 ]; do
	case "$1" in
	--run-a) RUN_A="${2:?--run-a needs a recording directory}"; shift ;;
	--run-b) RUN_B="${2:?--run-b needs a recording directory}"; shift ;;
	--owner-clip) OWNER_CLIP="${2:?--owner-clip needs a file}"; shift ;;
	--stills) STILLS="${2:?--stills needs a directory}"; shift ;;
	--out) OUT="${2:?--out needs a directory}"; shift ;;
	--plan) PLAN="${2:?--plan needs a file}"; shift ;;
	*) die "unknown argument $1" ;;
	esac
	shift
done

PY="$(command -v python3 || command -v python)" || die "python is required"

cmd_plan() {
	[ -d "$RUN_A" ] && [ -d "$RUN_B" ] || die "--run-a and --run-b must be recording directories"
	"$PY" -I "$HERE/take_plan.py" --run-a "$RUN_A" --run-b "$RUN_B" --out "$PLAN"
}

plan_shots() { # plan_shots <run>: the shot ids of a run in show order
	"$PY" -I -c 'import json,sys; [print(s["id"]) for s in json.load(open(sys.argv[1]))["shots"] if s["run"]==sys.argv[2]]' "$PLAN" "$1" | tr -d '\015'
}

encode_shot() { # encode_shot <frames dir> <out mp4>
	ffmpeg -nostdin -loglevel error -y -framerate "$FPS" -i "$1/frame_%06d.jpg" -c:v libx264 -pix_fmt yuv420p -crf 17 -movflags +faststart "$2"
}

render_run() { # render_run <A|B> <recording dir>
	local run="$1" rec="$2" frames="$OUT/frames/$1"
	[ -d "$rec" ] || die "$rec is not a recording directory"
	rm -rf "$frames"
	mkdir -p "$frames" "$OUT/shots" "$OUT/samples"
	log "rendering run $run from $(basename "$rec")"
	"$PLAYER" -force-d3d11 -screen-width "$WIDTH" -screen-height "$HEIGHT" -screen-fullscreen 0 -tp-replay "$(win "$rec")" -tp-render "$(win "$PLAN")" -tp-run "$run" \
		-tp-out "$(win "$frames")" -tp-fps "$FPS" -logFile "$(win "$OUT/render-$run.log")" || die "the player failed rendering run $run (see $OUT/render-$run.log)"
	[ -f "$frames/render.json" ] || die "the render left no render.json: it did not finish"
	local id n
	while IFS= read -r id; do
		[ -f "$frames/$id/frame_000001.jpg" ] || die "shot $id has no frames"
		n="$(find "$frames/$id" -maxdepth 1 -name 'frame_*.jpg' | wc -l)"
		log "shot $id: $n frames"
		# keep one frame a second (and the last) for the frame check and the contact sheet, then encode, then drop the frames
		mkdir -p "$OUT/samples/$id"
		local i=1
		while [ "$i" -le "$n" ]; do
			cp "$frames/$id/$(printf 'frame_%06d.jpg' "$i")" "$OUT/samples/$id/"
			i=$((i + FPS * SAMPLE_EVERY_S))
		done
		cp "$frames/$id/$(printf 'frame_%06d.jpg' "$n")" "$OUT/samples/$id/"
		encode_shot "$frames/$id" "$OUT/shots/$id.mp4"
		rm -rf "$frames/$id"
	done < <(plan_shots "$run")
}

cmd_render() {
	[ -f "$PLAYER" ] || die "no player at $PLAYER: build it first"
	[ -f "$PLAN" ] || die "no plan at $PLAN: run '$0 plan' first"
	need ffmpeg
	render_run A "$RUN_A"
	render_run B "$RUN_B"
	log "done: $(ls "$OUT/shots" | wc -l) shot clips in $OUT/shots"
}

cmd_cut() {
	need ffmpeg
	local list="$OUT/cut.txt" id
	: >"$list"
	# show order is the plan's order; the console's part sits in the plan's gap, between the last chiller shot and the pull-back
	while IFS= read -r id; do
		if [ "$id" = pull-back ] && [ -n "$OWNER_CLIP" ]; then printf "file '%s'\n" "$(win "$OWNER_CLIP")" >>"$list"; fi
		[ -f "$OUT/shots/$id.mp4" ] || die "no clip for shot $id: render first"
		printf "file '%s'\n" "$(win "$OUT/shots/$id.mp4")" >>"$list"
	done < <("$PY" -I -c 'import json,sys; [print(s["id"]) for s in json.load(open(sys.argv[1]))["shots"]]' "$PLAN" | tr -d '\015')
	ffmpeg -nostdin -loglevel error -y -f concat -safe 0 -i "$list" -c:v libx264 -pix_fmt yuv420p -crf 18 -r "$FPS" -movflags +faststart "$OUT/towerpulse-cut.mp4"
	log "cut: $(ffprobe -v error -show_entries format=duration -of csv=p=0 "$OUT/towerpulse-cut.mp4") s -> $OUT/towerpulse-cut.mp4"
}

cmd_owner() {
	need ffmpeg
	[ -f "$STILLS/annex-owner-devices.png" ] && [ -f "$STILLS/annex-owner-alarms.png" ] || die "--stills must hold annex-owner-devices.png and annex-owner-alarms.png (tools/console_stills.py)"
	local font="C\:/Windows/Fonts/segoeuib.ttf" cap
	cap="Console capture  -  the annex owner signs in. Seven of the thirty-one devices, and only the annex alarm"
	ffmpeg -nostdin -loglevel error -y -loop 1 -t 5 -framerate "$FPS" -i "$STILLS/annex-owner-devices.png" -loop 1 -t 5 -framerate "$FPS" -i "$STILLS/annex-owner-alarms.png" -filter_complex 		"[0:v][1:v]concat=n=2:v=1:a=0,scale=$WIDTH:$HEIGHT,format=yuv420p,drawbox=x=0:y=ih-110:w=iw:h=110:color=0x101820@0.92:t=fill,drawtext=fontfile='$font':text='$cap':fontcolor=white:fontsize=34:x=40:y=h-72,fade=t=in:st=0:d=0.3,fade=t=out:st=9.7:d=0.3[v]" -map "[v]" -c:v libx264 -pix_fmt yuv420p -crf 17 -r "$FPS" "$OUT/owner.mp4"
	log "owner clip -> $OUT/owner.mp4"
}

# The loop: five pieces of the SAME footage that run forward in each recorded run, opening and closing on the wide campus, and a half-second
# cross-dissolve at the loop point. Pieces are (shot, start s, length s).
LOOP_PIECES=("establish 0 4" "hot-room-alarm 1 6" "hot-room-cools 12 6" "chiller-silent 0 6" "pull-back 0 3.5")

cmd_loop() {
	need ffmpeg
	local parts=() p id ss len i=0 f
	for p in "${LOOP_PIECES[@]}"; do
		read -r id ss len <<<"$p"
		f="$OUT/loop-part-$i.mp4"
		ffmpeg -nostdin -loglevel error -y -ss "$ss" -t "$len" -i "$OUT/shots/$id.mp4" -c:v libx264 -pix_fmt yuv420p -crf 17 -r "$FPS" "$f"
		printf "file '%s'\n" "$(win "$f")" >>"$OUT/loop.txt.new"
		parts+=("$f")
		i=$((i + 1))
	done
	mv "$OUT/loop.txt.new" "$OUT/loop.txt"
	ffmpeg -nostdin -loglevel error -y -f concat -safe 0 -i "$OUT/loop.txt" -c:v libx264 -pix_fmt yuv420p -crf 17 -r "$FPS" "$OUT/loop-full.mp4"
	local total
	total="$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$OUT/loop-full.mp4")"
	local xf=0.5
	# body = everything between the first and the last half second; blend = the last half second dissolving into the first; loop = body then blend
	ffmpeg -nostdin -loglevel error -y -i "$OUT/loop-full.mp4" -filter_complex \
		"[0:v]trim=start=$xf:end=$(awk "BEGIN{print $total-$xf}"),setpts=PTS-STARTPTS[body];\
[0:v]trim=start=$(awk "BEGIN{print $total-$xf}"),setpts=PTS-STARTPTS[tail];\
[0:v]trim=end=$xf,setpts=PTS-STARTPTS[head];\
[tail][head]xfade=transition=fade:duration=$xf:offset=0[blend];\
[body][blend]concat=n=2:v=1:a=0[v]" -map "[v]" -c:v libx264 -pix_fmt yuv420p -crf 17 -r "$FPS" -movflags +faststart "$OUT/towerpulse-loop.mp4"
	# the website image: the Sitepulse loop's size, 720 wide at 10 fps
	local q="${WEBP_Q:-45}"
	ffmpeg -nostdin -loglevel error -y -i "$OUT/towerpulse-loop.mp4" -vf "fps=10,scale=720:-2:flags=lanczos" -c:v libwebp -lossless 0 -q:v "$q" -compression_level 6 -loop 0 -an "$OUT/towerpulse-loop.webp"
	rm -f "$OUT"/loop-part-*.mp4 "$OUT/loop-full.mp4"
	log "loop: $(ffprobe -v error -show_entries format=duration -of csv=p=0 "$OUT/towerpulse-loop.mp4") s; webp $(stat -c %s "$OUT/towerpulse-loop.webp") bytes (q $q)"
}

cmd_sheet() {
	need ffmpeg
	local rows=() id
	# a row per shot: its sampled frames at the start, middle and end, scaled down, tiled into one image
	local list="$OUT/sheet.txt"
	: >"$list"
	while IFS= read -r id; do
		local frames=("$OUT/samples/$id"/frame_*.jpg)
		local n=${#frames[@]}
		[ "$n" -gt 0 ] || continue
		for k in 0 $((n / 3)) $((2 * n / 3)) $((n - 1)); do printf "file '%s'\nduration 1\n" "$(win "${frames[$k]}")" >>"$list"; done
	done < <("$PY" -I -c 'import json,sys; [print(s["id"]) for s in json.load(open(sys.argv[1]))["shots"]]' "$PLAN" | tr -d '\015')
	ffmpeg -nostdin -loglevel error -y -f concat -safe 0 -i "$list" -vf "fps=1,scale=480:-2,tile=4x8:padding=4:margin=4:color=0x20242c" -frames:v 1 -q:v 3 "$OUT/contact-sheet.jpg"
	log "contact sheet -> $OUT/contact-sheet.jpg"
}

case "$cmd" in
plan) cmd_plan ;;
render) cmd_render ;;
cut) cmd_cut ;;
loop) cmd_loop ;;
owner) cmd_owner ;;
sheet) cmd_sheet ;;
*) die "unknown command $cmd (plan, render, owner, cut, loop, sheet)" ;;
esac
