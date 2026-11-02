#!/usr/bin/env python3
"""Build the CredOrbit demo video: VHS recording + branded intro/outro + captions."""
import json, os, subprocess, shutil, html

W, H = 1280, 800
REPO = os.path.expanduser("~/Documents/code/github/credorbit")
OUT = "/tmp/credorbit-video/out"
CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
os.makedirs(OUT, exist_ok=True)

TYPE = 0.055   # VHS TypingSpeed
KEY = 0.055    # each key command also waits TypingSpeed

theme = json.load(open("/tmp/credorbit-video/theme.json"))
lines, t = [], 0.0
captions = []  # (start, end, title, subtitle)
cap_open = None


def emit(s): lines.append(s)
def sleep(sec):
    global t
    emit(f"Sleep {int(sec*1000)}ms"); t += sec
def typ(text):
    global t
    emit("Type " + json.dumps(text)); t += len(text) * TYPE
def key(k, n=1, gap=0.0):
    global t
    for _ in range(n):
        emit(k); t += KEY
        if gap: sleep(gap)
def caption(title, sub=""):
    global cap_open
    end_caption()
    cap_open = [t, None, title, sub]
def end_caption():
    global cap_open
    if cap_open:
        cap_open[1] = t
        captions.append(tuple(cap_open)); cap_open = None


emit(f'Output "{OUT}/raw.mp4"')
for s in ['Set Shell "bash"', 'Set FontFamily "Menlo"', "Set FontSize 16", f"Set Width {W}", f"Set Height {H}",
          "Set Padding 26", "Set Margin 0", "Set WindowBar Colorful", "Set BorderRadius 12", "Set Framerate 30",
          f"Set TypingSpeed {int(TYPE*1000)}ms", "Set CursorBlink false",
          "Set Theme " + json.dumps(theme, separators=(",", ":"))]:
    emit(s)
emit("Hide")
emit("Type `export PATH=$HOME/bin:$PATH COLORTERM=truecolor LANG=en_US.UTF-8 PS1=$'\\[\\e[38;2;34;211;238m\\]❯\\[\\e[0m\\] ' && clear`")
emit("Enter"); emit("Sleep 600ms")
emit("Show")

sleep(0.6)
caption("One binary. No AWS CLI, Bash or Python.", "Native Go · macOS · Linux · Windows")
typ("credorbit version"); sleep(0.3); key("Enter"); sleep(1.5)
typ("credorbit ui --demo"); sleep(0.4); key("Enter"); sleep(2.2)
caption("Your cloud access, in one place", "SSO status, session and mode at a glance")
sleep(1.0); key("Down", 3, 0.35); key("Up", 3, 0.25); sleep(0.6)

caption("Browse every account and role", "Discovered live from IAM Identity Center — nothing hardcoded")
typ("4"); sleep(2.6)
caption("Fuzzy search", "Name, account ID or profile")
typ("/"); sleep(0.3); typ("payments"); sleep(0.9); key("Enter"); sleep(1.6)
caption("Filter by environment, status and group", "Credential expiry at a glance")
typ("c"); sleep(0.6); typ("e"); sleep(1.0); typ("e"); sleep(1.4); typ("c"); sleep(0.6)
caption("Select accounts for any operation", "Temporary selections never overwrite saved groups")
key("Down", 1, 0.25); key("Space", 1, 0.35); key("Down", 1, 0.25); key("Space", 1, 0.35); key("Down", 1, 0.25); key("Space", 1, 1.3)
key("Escape"); sleep(0.9)

caption("Curate your Main Accounts", "Saved by stable account ID — removing never deletes profiles")
typ("5"); sleep(2.2); key("Down", 1, 0.25); key("Space", 1, 0.45); key("Down", 1, 0.25); key("Space", 1, 1.2)
key("Ctrl+S"); sleep(1.8); key("Escape"); sleep(0.9)

caption("Full Setup & Sync", "Login → discover → sync → export → rotate, in one workflow")
typ("1"); sleep(1.6); key("Enter"); sleep(1.1); key("Enter"); sleep(1.3); key("Enter"); sleep(1.5)
typ("s"); sleep(2.4)
caption("Every change is previewed first", "Approve, then atomic writes with timestamped backups")
sleep(1.6); typ("a"); sleep(0.4)
caption("Live progress", "Bounded concurrency · per-account results · secrets never shown")
sleep(2.4)
caption("A clear execution report", "Exported, failed, backups and warnings")
sleep(5.4); key("Down", 4, 0.25); sleep(1.2); key("Escape"); sleep(0.9)

caption("Claude Code / Bedrock rotation", "Picks the next eligible account — never repeats when it can avoid it")
typ("/"); sleep(0.3); typ("claude"); sleep(0.6); key("Enter"); sleep(2.2); typ("R"); sleep(2.8); key("Escape"); sleep(0.8)

caption("Built-in diagnostics", "SSO, TLS / corporate CA, permissions and Claude integration")
typ("/"); sleep(0.3); typ("diag"); sleep(0.6); key("Enter"); sleep(3.6); key("Escape"); sleep(0.6)
end_caption()
typ("q"); sleep(1.0)
total = t

open(f"{OUT}/credorbit-demo.tape", "w").write(
    "# CredOrbit demo recording — reproduce with: vhs credorbit-demo.tape\n"
    "# Uses `credorbit ui --demo`: synthetic acme-* accounts, no AWS calls, no file writes.\n"
    + "\n".join(lines) + "\n")
json.dump({"captions": captions, "total": total}, open(f"{OUT}/timing.json", "w"), indent=1)

# ---------- branded cards & caption overlays (rendered with headless Chrome) ----------
wordmark = open(f"{REPO}/assets/brand/wordmark.svg").read()
symbol = open(f"{REPO}/assets/brand/logo-symbol.svg").read()
FONT = "'Space Grotesk','Sora','SF Pro Display','Helvetica Neue',Arial,sans-serif"
MONO = "'SF Mono',Menlo,monospace"
BG = """background: radial-gradient(1200px 700px at 50% 35%, #13224a 0%, #0B1020 60%) #0B1020;"""


def shot(name, body, transparent=False):
    path = f"{OUT}/{name}.html"
    bgcss = "background: transparent;" if transparent else BG
    open(path, "w").write(f"""<!doctype html><html><head><meta charset="utf-8"><style>
html,body{{margin:0;width:{W}px;height:{H}px;overflow:hidden;{bgcss}font-family:{FONT};color:#F8FAFC}}
</style></head><body>{body}</body></html>""")
    args = [CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--force-device-scale-factor=1",
            f"--window-size={W},{H}", f"--screenshot={OUT}/{name}.png"]
    if transparent:
        args.append("--default-background-color=00000000")
    subprocess.run(args + ["file://" + path], check=True, capture_output=True)


shot("intro", f"""
<div style="position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:28px">
  <div style="width:640px">{wordmark.replace('<svg', '<svg style="width:100%;height:auto"', 1)}</div>
  <div style="font-size:30px;letter-spacing:.42em;color:#94A3B8;font-weight:500">YOUR CLOUD ACCESS. IN ORBIT.</div>
  <div style="margin-top:18px;font-size:21px;color:#22D3EE">AWS IAM Identity Center · profiles · credentials · Claude/Bedrock rotation</div>
</div>""")

shot("outro", f"""
<div style="position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:22px">
  <div style="width:150px;height:150px">{symbol.replace('<svg', '<svg style="width:100%;height:100%"', 1)}</div>
  <div style="font-size:54px;font-weight:700;letter-spacing:-.01em">Cred<span style="background:linear-gradient(90deg,#22D3EE,#2563EB);-webkit-background-clip:text;color:transparent">Orbit</span></div>
  <div style="font-family:{MONO};font-size:22px;background:#111C31;border:1px solid #2563EB;border-radius:12px;padding:16px 26px;color:#F8FAFC">
    <span style="color:#22D3EE">❯</span> go install github.com/PiniPP/credorbit/cmd/credorbit@latest</div>
  <div style="font-size:26px;color:#22D3EE;font-weight:600">github.com/PiniPP/credorbit</div>
  <div style="font-size:19px;color:#94A3B8">Free for personal, education &amp; non-profit use · Commercial license for companies</div>
</div>""")

for i, (s, e, title, sub) in enumerate(captions):
    shot(f"cap{i:02d}", f"""
<div style="position:absolute;left:50%;bottom:44px;transform:translateX(-50%);max-width:1040px;
  background:rgba(17,28,49,.94);border:1px solid rgba(37,99,235,.9);border-left:5px solid #22D3EE;border-radius:14px;
  padding:14px 26px 15px;box-shadow:0 10px 34px rgba(0,0,0,.55);text-align:left;white-space:nowrap">
  <div style="font-size:27px;font-weight:700;color:#F8FAFC">{html.escape(title)}</div>
  <div style="font-size:18px;color:#94A3B8;margin-top:3px">{html.escape(sub)}</div>
</div>""", transparent=True)
print(f"tape ok: {len(lines)} lines, ~{total:.1f}s, {len(captions)} captions")
