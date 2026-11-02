#!/usr/bin/env python3
"""Narrated CredOrbit video: per-chapter VHS takes synced to per-sentence TTS."""
import html, json, os, subprocess

W, H, FPS = 1280, 800, 30
V = "/tmp/credorbit-video/v2"
REPO = os.path.expanduser("~/Documents/code/github/credorbit")
CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
VOICE, RATE, GAP = "Samantha", 172, 0.32  # GAP = silence between sentences
TYPE = KEY = 0.055
CARD = 2.6           # chapter card length (silent, music-free)
theme = json.load(open("/tmp/credorbit-video/theme.json"))
os.makedirs(V, exist_ok=True)


def dur(p):
    return float(subprocess.check_output(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                                          "-of", "csv=p=0", p]).decode())


def tts(text, out):
    subprocess.run(["say", "-v", VOICE, "-r", str(RATE), "-o", out + ".aiff", text], check=True)
    subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", out + ".aiff", "-ar", "48000", "-ac", "2", out + ".wav"], check=True)
    return dur(out + ".wav")


# ---- actions DSL ---------------------------------------------------------
def T(s): return ("type", s)
def K(k, n=1, gap=0.0): return ("key", k, n, gap)
def S(s): return ("sleep", s)


def act_time(acts):
    t = 0.0
    for a in acts:
        if a[0] == "type": t += len(a[1]) * TYPE
        elif a[0] == "key": t += a[2] * (KEY + a[3])
        elif a[0] == "sleep": t += a[1]
    return t


def act_lines(acts):
    out = []
    for a in acts:
        if a[0] == "type": out.append("Type " + json.dumps(a[1]))
        elif a[0] == "key":
            for _ in range(a[2]):
                out.append(a[1])
                if a[3]: out.append(f"Sleep {int(a[3]*1000)}ms")
        elif a[0] == "sleep": out.append(f"Sleep {int(a[1]*1000)}ms")
    return out


ENV = ("Type `export PATH=$HOME/bin:$PATH COLORTERM=truecolor LANG=en_US.UTF-8 "
       "PS1=$'\\[\\e[38;2;34;211;238m\\]❯\\[\\e[0m\\] ' && clear`")
LAUNCH = ['Type "credorbit ui --demo"', "Enter", "Sleep 2800ms"]

# ---- the script ------------------------------------------------------------
# Each chapter: title, subtitle, prelude ("shell" = show the shell, "menu" = start in the TUI menu),
# and sentences [(narration, actions fired when that sentence starts)].
INTRO = ["Meet Cred Orbit. Your cloud access, in orbit.",
         "Cred Orbit is one app for A.W.S. I.A.M. Identity Center. It signs you in, discovers every account and role "
         "assigned to you, keeps your A.W.S. config in sync, and fetches temporary credentials, all from a single terminal."]
CHAPTERS = [
    ("01", "Launch", "One native binary · macOS · Linux · Windows", "shell", [
        ("It's a single static binary for mac O S, Linux and Windows. No A.W.S. command line, Bash or Python required.",
         [S(0.5), T("credorbit version"), S(0.3), K("Enter")]),
        ("Run Cred Orbit, and the interactive interface opens.",
         [T("credorbit ui --demo"), S(0.3), K("Enter")]),
        ("The header always shows your sign-in status, the active S.S.O. session, and whether you're in normal or script-compatible mode.",
         [S(1.6), K("Down", 3, 0.45), K("Up", 3, 0.35)]),
    ]),
    ("02", "Discover your accounts", "Live from IAM Identity Center — nothing hardcoded", "menu", [
        ("Browse Accounts and Roles lists every account assigned to you, discovered live from Identity Center. Nothing is hard-coded.",
         [S(0.4), T("4")]),
        ("Search by name, account I.D., or profile.",
         [T("/"), S(0.3), T("payments"), S(0.6), K("Enter")]),
        ("Filter by environment, status or group, and see at a glance when each account's credentials expire.",
         [T("c"), S(0.6), T("e"), S(1.3), T("e"), S(1.6), T("c")]),
        ("Select accounts with the space bar to use them in any operation, without changing your saved groups.",
         [K("Down", 1, 0.3), K("Space", 1, 0.45), K("Down", 1, 0.3), K("Space", 1, 0.45), K("Down", 1, 0.3), K("Space")]),
    ]),
    ("03", "Main Accounts", "The accounts you work with every day", "menu", [
        ("Your Main Accounts are the set you work with every day.",
         [S(0.3), T("5")]),
        ("Toggle membership with the space bar, and save with control S.",
         [K("Down", 1, 0.35), K("Space", 1, 0.6), K("Down", 1, 0.35), K("Space", 1, 1.0), K("Ctrl+S")]),
        ("Groups are stored by stable account I.D., and removing an account from a group never deletes its A.W.S. profile or credentials.",
         []),
    ]),
    ("04", "Full Setup & Sync", "Login → discover → sync → export → rotate", "menu", [
        ("Full Setup and Sync runs the whole workflow in one pass.",
         [S(0.3), T("1")]),
        ("Choose the session, the mode, and the account scope.",
         [S(0.4), K("Enter"), S(1.0), K("Enter"), S(1.2), K("Enter")]),
        ("Cred Orbit signs in, discovers your accounts, and compares them with your A.W.S. config.",
         [S(0.6), T("s")]),
        ("Before anything is written, every change is shown for your review.",
         []),
        ("After you approve, credentials are fetched in parallel and written atomically, with your files backed up first.",
         [S(0.8), T("a")]),
        ("The report shows exactly what happened: what was exported, what failed, and where the backups are.",
         [S(0.6), K("Down", 4, 0.35)]),
    ]),
    ("05", "Claude Code rotation", "Spread Amazon Bedrock usage across accounts", "menu", [
        ("If you use Claude Code with Amazon Bedrock, Cred Orbit rotates the A.W.S. account it uses.",
         [S(0.3), T("/"), S(0.2), T("claude"), S(0.4), K("Enter")]),
        ("It only picks accounts with valid credentials, and never repeats the current one when there's an alternative.",
         [S(1.4), T("R")]),
    ]),
    ("06", "Diagnostics", "Actionable checks with a fix for every issue", "menu", [
        ("And when something is wrong, Diagnostics checks your S.S.O. sessions, T.L.S. and corporate certificates, "
         "file permissions, and the Claude integration, with a clear fix for every issue.",
         [S(0.3), T("/"), S(0.2), T("diag"), S(0.4), K("Enter")]),
    ]),
]
OUTRO = ["Cred Orbit is free for personal, educational and non-profit use. Companies need a commercial license.",
         "Install it with go install, or download a release from GitHub.",
         "Cred Orbit. Your cloud access, in orbit."]

# ---- audio -----------------------------------------------------------------
def narration(name, sentences, lead=0.0):
    """Synthesize sentences, concat with gaps; return (wav, starts, total)."""
    parts, starts, t = [], [], lead
    for i, s in enumerate(sentences):
        d = tts(s, f"{V}/{name}_s{i}")
        starts.append(t); parts.append((f"{V}/{name}_s{i}.wav", d)); t += d + GAP
    total = t - GAP
    # concat via filter: delay each part to its start
    ins, fl = [], []
    for i, (p, _) in enumerate(parts):
        ins += ["-i", p]; ms = int(starts[i] * 1000); fl.append(f"[{i}]adelay={ms}|{ms}[a{i}]")
    fl.append("".join(f"[a{i}]" for i in range(len(parts))) + f"amix=inputs={len(parts)}:normalize=0[out]")
    subprocess.run(["ffmpeg", "-v", "error", "-y", *ins, "-filter_complex", ";".join(fl), "-map", "[out]",
                    "-ar", "48000", "-ac", "2", f"{V}/{name}.wav"], check=True)
    return f"{V}/{name}.wav", starts, total


meta = {"segments": []}

# ---- cards (headless Chrome) -------------------------------------------------
wordmark = open(f"{REPO}/assets/brand/wordmark.svg").read()
symbol = open(f"{REPO}/assets/brand/logo-symbol.svg").read()
FONT = "'Space Grotesk','Sora','SF Pro Display','Helvetica Neue',Arial,sans-serif"
MONO = "'SF Mono',Menlo,monospace"
BG = "background: radial-gradient(1200px 700px at 50% 35%, #13224a 0%, #0B1020 60%) #0B1020;"


def shot(name, body, transparent=False):
    path = f"{V}/{name}.html"
    open(path, "w").write(f"""<!doctype html><html><head><meta charset="utf-8"><style>
html,body{{margin:0;width:{W}px;height:{H}px;overflow:hidden;{'background:transparent;' if transparent else BG}font-family:{FONT};color:#F8FAFC}}
</style></head><body>{body}</body></html>""")
    a = [CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--force-device-scale-factor=1",
         f"--window-size={W},{H}", f"--screenshot={V}/{name}.png"]
    if transparent: a.append("--default-background-color=00000000")
    subprocess.run(a + ["file://" + path], check=True, capture_output=True)


svg = lambda s, css: s.replace("<svg", f'<svg style="{css}"', 1)
shot("intro", f"""<div style="position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:28px">
<div style="width:640px">{svg(wordmark,'width:100%;height:auto')}</div>
<div style="font-size:30px;letter-spacing:.42em;color:#94A3B8;font-weight:500">YOUR CLOUD ACCESS. IN ORBIT.</div>
<div style="margin-top:18px;font-size:21px;color:#22D3EE">AWS IAM Identity Center · profiles · credentials · Claude/Bedrock rotation</div></div>""")
shot("outro", f"""<div style="position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:22px">
<div style="width:150px;height:150px">{svg(symbol,'width:100%;height:100%')}</div>
<div style="font-size:54px;font-weight:700">Cred<span style="background:linear-gradient(90deg,#22D3EE,#2563EB);-webkit-background-clip:text;color:transparent">Orbit</span></div>
<div style="font-family:{MONO};font-size:22px;background:#111C31;border:1px solid #2563EB;border-radius:12px;padding:16px 26px"><span style="color:#22D3EE">❯</span> go install github.com/PiniPP/credorbit/cmd/credorbit@latest</div>
<div style="font-size:26px;color:#22D3EE;font-weight:600">github.com/PiniPP/credorbit</div>
<div style="font-size:19px;color:#94A3B8">Free for personal, education &amp; non-profit use · Commercial license for companies</div></div>""")
for num, title, sub, *_ in CHAPTERS:
    shot(f"ch{num}", f"""<div style="position:absolute;inset:0;display:flex;align-items:center;justify-content:center">
<div style="display:flex;align-items:center;gap:44px">
  <div style="width:120px;height:120px;opacity:.95">{svg(symbol,'width:100%;height:100%')}</div>
  <div style="border-left:3px solid #2563EB;padding-left:40px">
    <div style="font-family:{MONO};font-size:26px;color:#22D3EE;letter-spacing:.2em">CHAPTER {num}</div>
    <div style="font-size:58px;font-weight:700;margin:6px 0 10px">{html.escape(title)}</div>
    <div style="font-size:24px;color:#94A3B8">{html.escape(sub)}</div>
  </div></div></div>""")
    shot(f"lt{num}", f"""<div style="position:absolute;right:40px;bottom:96px;background:rgba(17,28,49,.92);border:1px solid rgba(37,99,235,.9);
border-left:4px solid #22D3EE;border-radius:10px;padding:7px 16px;font-size:17px;font-weight:600;box-shadow:0 6px 20px rgba(0,0,0,.45)">
<span style="font-family:{MONO};color:#22D3EE">{num}</span>&nbsp;&nbsp;{html.escape(title)}</div>""", transparent=True)

# ---- intro & outro segments -----------------------------------------------
wav, starts, tot = narration("intro", INTRO, lead=0.9)
meta["segments"].append({"kind": "card", "img": "intro.png", "len": tot + 1.2, "wav": wav,
                         "subs": [[st, s] for st, s in zip(starts, INTRO)]})

# ---- chapter takes ---------------------------------------------------------
for num, title, sub, prelude, sents in CHAPTERS:
    wav, starts, tot = narration(f"n{num}", [s for s, _ in sents], lead=0.4)
    lines = [f'Output "{V}/take{num}.mp4"', 'Set Shell "bash"', 'Set FontFamily "Menlo"', "Set FontSize 16",
             f"Set Width {W}", f"Set Height {H}", "Set Padding 26", "Set Margin 0", "Set WindowBar Colorful",
             "Set BorderRadius 12", "Set Framerate 30", f"Set TypingSpeed {int(TYPE*1000)}ms", "Set CursorBlink false",
             "Set Theme " + json.dumps(theme, separators=(",", ":")), "Hide", ENV, "Enter", "Sleep 500ms"]
    if prelude == "menu":
        lines += LAUNCH
    lines.append("Show")
    t = 0.0
    for (sentence, acts), st in zip(sents, starts):
        if st > t:
            lines.append(f"Sleep {int((st - t)*1000)}ms"); t = st
        lines += act_lines(acts); t += act_time(acts)
    end = tot + 1.3
    if end > t:
        lines.append(f"Sleep {int((end - t)*1000)}ms"); t = end
    open(f"{V}/take{num}.tape", "w").write("\n".join(lines) + "\n")
    meta["segments"].append({"kind": "chapter", "num": num, "card": f"ch{num}.png", "lt": f"lt{num}.png",
                             "take": f"take{num}.mp4", "est": t, "wav": wav,
                             "subs": [[st, s] for st, (s, _) in zip(starts, sents)]})

wav, starts, tot = narration("outro", OUTRO, lead=0.8)
meta["segments"].append({"kind": "card", "img": "outro.png", "len": tot + 1.8, "wav": wav,
                         "subs": [[st, s] for st, s in zip(starts, OUTRO)]})
json.dump(meta, open(f"{V}/meta.json", "w"), indent=1)
print("segments:", [(s.get("num", s["kind"]), round(s.get("est", s.get("len", 0)), 1)) for s in meta["segments"]])
