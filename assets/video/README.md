# CredOrbit demo video

| File | Use |
|---|---|
| `credorbit-demo-narrated.mp4` | 1280×800 H.264 + AAC, 2:47 — narrated walkthrough with chapter cards |
| `credorbit-demo-narrated.srt` | Subtitles for the narrated walkthrough (upload alongside on YouTube / LinkedIn) |
| `credorbit-demo.mp4` | 1280×800 H.264, 72 s — silent captioned cut |
| `credorbit-demo.gif` | 960 px, 12 fps — inline in the README |
| `credorbit-demo-poster.png` | Thumbnail / poster frame |
| `credorbit-demo.tape` | The scripted terminal recording |

Everything uses `credorbit ui --demo`: synthetic `acme-*` accounts, no AWS calls, no file writes —
so no real account names, IDs or start URLs can appear.

## Toolchain

| Step | Tool |
|---|---|
| Terminal recording | [VHS](https://github.com/charmbracelet/vhs) (uses ttyd) with a CredOrbit-branded terminal theme |
| Intro, outro and captions | Headless Chrome rendering HTML/CSS with the brand SVGs and palette |
| Narration | macOS text-to-speech (`say`, voice Samantha), one clip per sentence so actions fire exactly on cue |
| Editing (chapters, fades, overlays, audio mix, encoding, GIF palette) | ffmpeg |

## Re-record the terminal part

```bash
brew install vhs            # also installs ttyd and ffmpeg
make build && export PATH="$PWD:$PATH"
cd assets/video && vhs credorbit-demo.tape   # writes credorbit-demo-raw.mp4
```
