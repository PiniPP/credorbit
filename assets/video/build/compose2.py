#!/usr/bin/env python3
"""Assemble the narrated CredOrbit video (+ SRT subtitles) from build2.py outputs."""
import json, os, subprocess, textwrap

V = "/tmp/credorbit-video/v2"
FPS, CARD = 30, 2.6
meta = json.load(open(f"{V}/meta.json"))
ENC = ["-c:v", "libx264", "-preset", "slow", "-crf", "18", "-pix_fmt", "yuv420p", "-r", str(FPS),
       "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2"]


def dur(p):
    return float(subprocess.check_output(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                                          "-of", "csv=p=0", p]).decode())


def run(args): subprocess.run(["ffmpeg", "-v", "error", "-y", *args], check=True)


DISPLAY = [("Cred Orbit", "CredOrbit"), ("A.W.S.", "AWS"), ("I.A.M.", "IAM"), ("S.S.O.", "SSO"), ("T.L.S.", "TLS"),
           ("I.D.", "ID"), ("mac O S", "macOS"), ("control S", "Ctrl+S"), ("A.W.S", "AWS")]
segs, subs, clock = [], [], 0.0
for i, s in enumerate(meta["segments"]):
    out = f"{V}/seg{i:02d}.mp4"
    if s["kind"] == "card":
        L = s["len"]
        run(["-loop", "1", "-t", f"{L:.3f}", "-i", f"{V}/{s['img']}", "-i", s["wav"], "-filter_complex",
             f"[0]fps={FPS},format=yuv420p,fade=in:st=0:d=0.6,fade=out:st={L-0.6:.3f}:d=0.6[v];"
             f"[1]apad,atrim=0:{L:.3f}[a]", "-map", "[v]", "-map", "[a]", *ENC, out])
        offset = 0.0
    else:
        take = f"{V}/{s['take']}"
        k = s["est"] / dur(take)                      # stretch take to the narration timeline
        L = CARD + s["est"]
        run(["-loop", "1", "-t", f"{CARD:.3f}", "-i", f"{V}/{s['card']}",
             "-i", take,
             "-loop", "1", "-t", f"{s['est']:.3f}", "-i", f"{V}/{s['lt']}",
             "-i", s["wav"],
             "-filter_complex",
             f"[0]fps={FPS},format=yuv420p,fade=in:st=0:d=0.45,fade=out:st={CARD-0.45:.3f}:d=0.45[c];"
             f"[1]setpts={k:.5f}*PTS,fps={FPS},format=yuv420p,trim=0:{s['est']:.3f},setpts=PTS-STARTPTS[t];"
             f"[2]format=rgba,fade=in:st=0.3:d=0.4:alpha=1,fade=out:st=3.2:d=0.5:alpha=1[l];"
             f"[t][l]overlay=0:0:format=auto,fade=in:st=0:d=0.3,fade=out:st={s['est']-0.4:.3f}:d=0.4[tv];"
             f"[c][tv]concat=n=2:v=1:a=0[v];"
             f"[3]adelay={int(CARD*1000)}|{int(CARD*1000)},apad,atrim=0:{L:.3f}[a]",
             "-map", "[v]", "-map", "[a]", *ENC, out])
        offset = CARD
    # subtitles: sentence start + its own synthesized duration
    name = "intro" if s["kind"] == "card" and i == 0 else ("outro" if s["kind"] == "card" else f"n{s['num']}")
    for j, (st, text) in enumerate(s["subs"]):
        d = dur(f"{V}/{name}_s{j}.wav")
        for a_, b_ in DISPLAY: text = text.replace(a_, b_)
        chunks, cur = [], []
        for w in text.split():                       # greedy: each cue fits in 2 lines of 42
            if len(textwrap.wrap(" ".join(cur + [w]), 42)) > 2:
                chunks.append(" ".join(cur)); cur = [w]
            else:
                cur.append(w)
        chunks.append(" ".join(cur))
        per = d / len(chunks)  # (cue timing proportional within a sentence)
        for c_i, c in enumerate(chunks):
            a = clock + offset + st + c_i * per
            subs.append((a, a + per, "\n".join(textwrap.wrap(c, 42))))
    segs.append(out)
    clock += dur(out)

open(f"{V}/list.txt", "w").write("".join(f"file '{p}'\n" for p in segs))
final = f"{V}/credorbit-demo-narrated.mp4"
run(["-f", "concat", "-safe", "0", "-i", f"{V}/list.txt", "-c", "copy", "-movflags", "+faststart", final])


def ts(x):
    h, r = divmod(x, 3600); m, s = divmod(r, 60)
    return f"{int(h):02d}:{int(m):02d}:{int(s):02d},{int(round((s % 1) * 1000)) % 1000:03d}"


with open(f"{V}/credorbit-demo-narrated.srt", "w") as f:
    for n, (a, b, t) in enumerate(subs, 1):
        f.write(f"{n}\n{ts(a)} --> {ts(b)}\n{t}\n\n")
print(f"final {dur(final):.1f}s, {len(subs)} subtitle cues")
