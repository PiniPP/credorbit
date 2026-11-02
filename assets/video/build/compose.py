#!/usr/bin/env python3
"""Compose intro + captioned recording + outro into the final CredOrbit video."""
import json, subprocess

OUT = "/tmp/credorbit-video/out"
FPS = 30
INTRO, OUTRO, XF1, XF2, CF = 3.4, 5.0, 0.7, 0.9, 0.28

tm = json.load(open(f"{OUT}/timing.json"))
raw_dur = float(subprocess.check_output(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                                         "-of", "csv=p=0", f"{OUT}/raw.mp4"]).decode())
k = raw_dur / tm["total"]  # estimated → actual time scale
caps = [(s * k, e * k, i) for i, (s, e, *_ ) in enumerate(tm["captions"])]

inputs = ["-loop", "1", "-t", str(INTRO), "-i", f"{OUT}/intro.png",
          "-i", f"{OUT}/raw.mp4",
          "-loop", "1", "-t", str(OUTRO), "-i", f"{OUT}/outro.png"]
for s, e, i in caps:
    inputs += ["-loop", "1", "-t", f"{e - s:.3f}", "-i", f"{OUT}/cap{i:02d}.png"]

f = [f"[0]fps={FPS},format=yuv420p,fade=in:st=0:d=0.8[intro]",
     f"[1]fps={FPS},format=yuv420p[rec0]"]
prev = "rec0"
for n, (s, e, i) in enumerate(caps):
    d = e - s
    lbl = f"c{n}"
    f.append(f"[{3 + n}]format=rgba,fade=in:st=0:d={CF}:alpha=1,fade=out:st={max(0, d - CF):.3f}:d={CF}:alpha=1,"
             f"setpts=PTS+{s:.3f}/TB[{lbl}]")
    out = f"rec{n + 1}"
    f.append(f"[{prev}][{lbl}]overlay=0:0:eof_action=pass:format=auto[{out}]")
    prev = out
f.append(f"[2]fps={FPS},format=yuv420p,fade=out:st={OUTRO - 0.6}:d=0.6[outro]")
f.append(f"[intro][{prev}]xfade=transition=fade:duration={XF1}:offset={INTRO - XF1:.3f}[a]")
f.append(f"[a][outro]xfade=transition=fade:duration={XF2}:offset={INTRO - XF1 + raw_dur - XF2:.3f},format=yuv420p[v]")

subprocess.run(["ffmpeg", "-v", "error", "-y", *inputs, "-filter_complex", ";".join(f), "-map", "[v]",
                "-c:v", "libx264", "-preset", "slow", "-crf", "18", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
                "-r", str(FPS), f"{OUT}/credorbit-demo.mp4"], check=True)

# README GIF: 960px wide, 12 fps, optimised 2-pass palette in brand colours.
pal = f"{OUT}/palette.png"
subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", f"{OUT}/credorbit-demo.mp4", "-vf",
                "fps=12,scale=960:-1:flags=lanczos,palettegen=max_colors=160:stats_mode=diff", pal], check=True)
subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", f"{OUT}/credorbit-demo.mp4", "-i", pal, "-lavfi",
                "fps=12,scale=960:-1:flags=lanczos[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle",
                f"{OUT}/credorbit-demo.gif"], check=True)
# Poster frame for players/LinkedIn.
subprocess.run(["ffmpeg", "-v", "error", "-y", "-ss", f"{INTRO + 6:.2f}", "-i", f"{OUT}/credorbit-demo.mp4",
                "-frames:v", "1", f"{OUT}/credorbit-demo-poster.png"], check=True)
print(f"scale k={k:.4f}, raw {raw_dur:.1f}s, final ≈ {INTRO + raw_dur + OUTRO - XF1 - XF2:.1f}s")
