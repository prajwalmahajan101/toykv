# assets

Visual artefacts referenced from the project root `README.md`.

| File | Used for | How to regenerate |
|---|---|---|
| `tui.gif` | README TUI demo (animated) | `vhs assets/tui.tape` (charmbracelet/vhs) |
| `tui.png` | README TUI screenshot (static fallback) | Single-frame export from the same `tui.tape` recording, or any terminal screenshotter |

The README falls back to an inline ASCII rendering when neither file is present, so the project still reads cleanly on a fresh clone.
