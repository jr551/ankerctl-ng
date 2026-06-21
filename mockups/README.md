# ankerctl-ng · UI redesign mockups

Static, self-contained HTML mockups of a proposed redesign for the
**ankerctl-ng** web UI. Open any page in a browser — no build step, no
server, no external dependencies.

## How to view

Just open any file in a browser. Start at the dashboard:

```sh
open mockups/index.html        # macOS
xdg-open mockups/index.html    # Linux
```

Or, if you'd rather serve them:

```sh
python3 -m http.server 8080 --directory mockups
# then visit http://localhost:8080/
```

Each page has a banner at the top with quick links to every other page.

## Files

| File                | What it shows                                                              |
|---------------------|----------------------------------------------------------------------------|
| `index.html`        | **Advanced dashboard** — camera, control pad, temps, AI monitor, power     |
| `basic-dashboard.html` | **Basic Mode dashboard** — simplified "cover": camera + status + big buttons |
| `mode-picker.html`  | **Mode-picker cover** — full-screen splash: Basic / Advanced / Beginner    |
| `slice.html`        | **Slice & Build** — STL/​OpenSCAD input, 3D preview, settings, GCode stats  |
| `history.html`      | **History** — filterable table, AI evidence frames, model reasoning        |
| `filaments.html`    | **Filaments** — extrude/​retract, guided swap, library grid, colour match   |
| `setup.html`        | **Settings · Appearance** — theme, accent, density, default mode           |
| `setup-account.html` | **Settings · Account** — AnkerMake login, region, API key, danger zone     |
| `setup-printer.html` | **Settings · Printer** — active printer details, list, add                 |
| `setup-tools.html`   | **Settings · Tools** — slicer integration, gcode archive, server           |
| `setup-notifications.html` | **Settings · Notifications** — channels, event matrix, quiet hours  |
| `setup-camera-ai.html` | **Settings · Camera & AI** — source, print monitor, 🐾 animal safety stop |
| `setup-timelapse.html` | **Settings · Timelapse** — capture, storage, gallery, manual controls     |
| `setup-mqtt.html`   | **Settings · MQTT / API** — HTTP API, broker, PPPP, trusted networks       |
| `setup-power.html`  | **Settings · Power & Socket** — live state, config, behaviours, usage chart|
| `setup-homeassistant.html` | **Settings · Home Assistant** — connection, discovery, entity mapping |
| `beginner.html`     | **Beginner wizard** — all 3 steps (idea → AI → print) shown side-by-side   |
| `assets/styles.css` | Shared design system (tokens, components, layout)                          |
| `assets/nav.js`     | Sidebar + topbar + subnav + theme toggle bootstrap                         |

## The three "modes"

The redesign proposes a small **mode model** on top of the existing UI:

- **Basic Mode** (`basic-dashboard.html`) — a simplified *cover* of the
  Advanced dashboard. Same shell, same data, same AI safety watch. The
  sidebar collapses to icons, the jog pad / temp graph / power strip /
  upload stream are hidden, and what's left is: a big camera, a big
  progress gauge, three big buttons (Pause / Stop / Cooldown), preheat
  presets, and "Print a file". For people who just want to hit print.
- **Advanced Mode** (`index.html`) — everything ankerctl can do today,
  modernised. This is the default and the source of truth.
- **Beginner Mode** (`beginner.html`) — the existing kid-mode wizard:
  describe an idea → AI writes OpenSCAD → auto-safe settings → confirm
  → print. Unchanged conceptually, just restyled.

The **mode-picker cover** (`mode-picker.html`) is the splash that asks
which of the three you want. It's full-screen, shown on first launch
(unless "Remember my choice" is set) and re-invokeable from the sidebar's
"Switch Mode" item.

## What's new vs. the current UI

The current app is Bootstrap 5 with a top tab bar. This redesign proposes:

1. **Fixed left sidebar** with the brand, a printer selector, grouped nav,
   and live system status (version / uptime / ambient temp) in the footer.
   Gives a more "pro tool" feel and frees vertical space.
2. **Persistent topbar** with the page title, live connection pills
   (MQTT · PPPP · CAMERA · CTRL), a theme toggle, and a **signature
   pulsing E-STOP** that's always one tap away.
3. **Modern dark-first theme** with the project's existing signature
   green (`#88f387`) as the accent — plus a one-click **light theme** and
   a user-pickable accent colour (preserved in `localStorage`).
4. **Circular gauges** for print progress and nozzle/bed temperatures,
   with a real (SVG) temperature history chart instead of just Chart.js.
5. **Status tiles** — a cleaner successor to the existing `state-strip`,
   used consistently across pages.
6. **Dedicated AI panels** (violet-tinted) wherever AI shows up —
   print monitor, slice sanity check, history evidence — so AI is
   discoverable but never confused with hard data.
7. **Camera overlay** — light, snapshot, quality, rewind slider, AI scan
   line, "animal watch armed" chip, all on the frame itself instead of
   a separate card below.
8. **Beginner Mode as a real 3-step wizard** with per-pass status so
   users never wonder if it's frozen.
9. **Self-contained** — all SVG icons are inlined, all CSS/JS is local.
   Zero network requests. Drop these on a USB stick and they still work.

## Design tokens worth knowing

From `assets/styles.css`:

```css
--accent: #88f387;       /* signature green (kept from the current UI) */
--danger: #ff5d6c;       /* E-STOP, failures */
--warning: #ffb340;      /* bed temps, cautions */
--info: #4dc4ff;
--violet: #b78bff;       /* AI surfaces */
--radius-lg: 16px;       /* card corners */
--font: Inter, system-ui, ...;
--mono: JetBrains Mono, ui-monospace, ...;
```

All colours are CSS variables — swap the accent in `:root` and the whole
mockup retints.

## Caveats

- These are **static mockups**. Numbers, printer name, file names, AI
  verdicts, and connection states are all placeholder data.
- Inter / JetBrains Mono aren't loaded — they fall back to system fonts
  unless you have them installed. Adding `<link>` Google Fonts tags in
  each `<head>` is a one-liner if you want to see the intended typography.
- No JS interactivity beyond the theme toggle. Clicking E-STOP / buttons
  does nothing. This is by design for a mockup.

## Mapping back to the real codebase

| Existing template                       | Mockup file              |
|-----------------------------------------|--------------------------|
| `internal/web/static/tabs/home.html`    | `index.html` (Advanced) · `basic-dashboard.html` (Basic) |
| `internal/web/static/tabs/slice.html`   | `slice.html`             |
| `internal/web/static/tabs/history.html` | `history.html`           |
| `internal/web/static/tabs/filaments.html` | `filaments.html`       |
| `internal/web/static/tabs/setup.html`   | `setup.html`             |
| `internal/web/static/index.html` (kid overlay) | `beginner.html` · `mode-picker.html` |
| `internal/web/static/tabs/debug.html`   | (not included — power-user only, deliberately de-emphasised) |

If you like a direction here, the next step is to pick one page, port its
component CSS back into `ankersrv.css`, and rebuild the template against
the new markup — page by page.
