// Slice & Print tab — in-browser STL slicing via polyslice + three.js.
// Loaded as an ES module (see base.html importmap). three/polyslice are
// dynamically imported on first use so they don't weigh down other pages.
//
// Flow: STL file -> STLLoader -> THREE.Mesh -> Polyslice(M5C defaults).slice()
// -> Marlin gcode string -> preview (2D toolpath) -> download or POST to the
// existing /api/files/local upload path.

const $ = (id) => document.getElementById(id);
const fileInput = $("slice-stl-file");
if (fileInput) {
    const els = {
        run: $("slice-run"),
        status: $("slice-status"),
        previewWrap: $("slice-preview-wrap"),
        canvas: $("slice-canvas"),
        layer: $("slice-layer"),
        layerLabel: $("slice-layer-label"),
        travel: $("slice-travel"),
        print: $("slice-print"),
        continueBtn: $("slice-continue"),
        warning: $("slice-warning"),
        download: $("slice-download"),
    };

    // AnkerMake M5C PLA defaults (researched; clamps nozzle<=275, bed<=100).
    const M5C = {
        buildPlateWidth: 220, buildPlateLength: 220,
        nozzleDiameter: 0.4, filamentDiameter: 1.75,
        nozzleTemperature: 215, bedTemperature: 60,
        layerHeight: 0.2, extrusionMultiplier: 1.0,
        perimeterSpeed: 50, infillSpeed: 60, travelSpeed: 120,
        retractionDistance: 1.0, retractionSpeed: 40,
        infillDensity: 20, infillPattern: "grid",
        adhesionEnabled: true, adhesionType: "skirt",
        fanSpeed: 100,
    };
    const ANKER_PREAMBLE = "M4899 T3 ; ankerctl-ng: enable v3 jerk + S-curve acceleration\n";

    // Per-model bed sizes (selected in the UI). Merged over the M5C defaults.
    const MODELS = {
        m5c: { buildPlateWidth: 220, buildPlateLength: 220 },
        m5: { buildPlateWidth: 235, buildPlateLength: 235 },
    };

    let libs = null;
    let gcode = "";
    let parsed = null;
    let baseName = "model";

    const setStatus = (msg, kind) => {
        els.status.textContent = msg || "";
        els.status.className = "form-text" + (kind === "error" ? " text-danger" : kind === "ok" ? " text-success" : "");
    };

    const loadLibs = async () => {
        if (libs) return libs;
        setStatus("Loading slicer (one-time)…");
        const THREE = await import("three");
        window.THREE = THREE; // polyslice reads window.THREE at runtime
        const Polyslice = (await import("@jgphilpott/polyslice")).default;
        const { STLLoader } = await import("three/examples/jsm/loaders/STLLoader.js");
        libs = { THREE, Polyslice, STLLoader };
        return libs;
    };

    // ── Minimal 2D toolpath parser/renderer (shared shape with the history
    //    gcode viewer; kept local so this module stays self-contained). ──
    const parseGcode = (text) => {
        const lines = text.split("\n");
        const layers = [];
        let cur = null;
        const startLayer = (z) => { cur = []; layers.push({ z, segs: cur }); };
        startLayer(0);
        let absolute = true, absE = true, x = 0, y = 0, z = 0, e = 0;
        let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
        for (let i = 0; i < lines.length; i++) {
            let line = lines[i];
            const semi = line.indexOf(";");
            if (semi >= 0) line = line.slice(0, semi);
            line = line.trim();
            if (!line) continue;
            const p = line.split(/\s+/);
            const cmd = p[0].toUpperCase();
            if (cmd === "G90") { absolute = true; continue; }
            if (cmd === "G91") { absolute = false; continue; }
            if (cmd === "M82") { absE = true; continue; }
            if (cmd === "M83") { absE = false; continue; }
            if (cmd === "G92") {
                for (let j = 1; j < p.length; j++) { const v = parseFloat(p[j].slice(1)); if (isNaN(v)) continue; const c = p[j][0].toUpperCase(); if (c === "X") x = v; else if (c === "Y") y = v; else if (c === "Z") z = v; else if (c === "E") e = v; }
                continue;
            }
            if (cmd !== "G0" && cmd !== "G1") continue;
            let nx = x, ny = y, nz = z, ne = e, hasE = false;
            for (let j = 1; j < p.length; j++) { const c = p[j][0].toUpperCase(); const v = parseFloat(p[j].slice(1)); if (isNaN(v)) continue; if (c === "X") nx = absolute ? v : x + v; else if (c === "Y") ny = absolute ? v : y + v; else if (c === "Z") nz = absolute ? v : z + v; else if (c === "E") { hasE = true; ne = absE ? v : e + v; } }
            if (nz > z + 0.0001) startLayer(nz);
            const extruding = hasE && (ne - e) > 0.0001 && (nx !== x || ny !== y);
            if (nx !== x || ny !== y) {
                cur.push([x, y, nx, ny, extruding ? 1 : 0]);
                if (extruding) { if (x < minX) minX = x; if (x > maxX) maxX = x; if (y < minY) minY = y; if (y > maxY) maxY = y; if (nx < minX) minX = nx; if (nx > maxX) maxX = nx; if (ny < minY) minY = ny; if (ny > maxY) maxY = ny; }
            }
            x = nx; y = ny; z = nz; if (hasE) e = ne;
        }
        if (!isFinite(minX)) { minX = 0; minY = 0; maxX = 220; maxY = 220; }
        return { layers, bbox: { minX, minY, maxX, maxY } };
    };

    const draw = () => {
        if (!parsed) return;
        const cv = els.canvas, ctx = cv.getContext("2d"), W = cv.width, H = cv.height;
        ctx.fillStyle = "#0d1117"; ctx.fillRect(0, 0, W, H);
        const b = parsed.bbox, bw = Math.max(1, b.maxX - b.minX), bh = Math.max(1, b.maxY - b.minY), pad = 14;
        const scale = Math.min((W - 2 * pad) / bw, (H - 2 * pad) / bh);
        const ox = (W - bw * scale) / 2, oy = (H - bh * scale) / 2;
        const tx = (px) => ox + (px - b.minX) * scale, ty = (py) => H - (oy + (py - b.minY) * scale);
        const top = parseInt(els.layer.value, 10) || 0, showTravel = els.travel.checked;
        for (let li = 0; li <= top && li < parsed.layers.length; li++) {
            const isTop = li === top, segs = parsed.layers[li].segs;
            if (showTravel) { ctx.strokeStyle = "rgba(120,160,255,0.22)"; ctx.lineWidth = 0.5; ctx.beginPath(); for (const s of segs) if (!s[4]) { ctx.moveTo(tx(s[0]), ty(s[1])); ctx.lineTo(tx(s[2]), ty(s[3])); } ctx.stroke(); }
            ctx.strokeStyle = isTop ? "#88f387" : "rgba(136,243,135,0.32)"; ctx.lineWidth = isTop ? 1.3 : 0.7; ctx.beginPath();
            for (const s of segs) if (s[4]) { ctx.moveTo(tx(s[0]), ty(s[1])); ctx.lineTo(tx(s[2]), ty(s[3])); } ctx.stroke();
        }
    };

    const showPreview = () => {
        parsed = parseGcode(gcode);
        els.previewWrap.classList.remove("d-none");
        els.layer.max = Math.max(0, parsed.layers.length - 1);
        els.layer.value = parsed.layers.length - 1;
        els.layer.disabled = parsed.layers.length <= 1;
        els.layerLabel.textContent = `${parsed.layers.length} / ${parsed.layers.length}`;
        draw();
    };

    // Fast, deterministic pre-print sanity check on the sliced result — no gcode
    // is sent anywhere. Flags the serious issues that ruin a print (nothing
    // extruded, or the toolpath runs off the bed). Returns a list of issues.
    const sanityCheck = (bedW, bedL) => {
        const issues = [];
        const hasExtrusion = parsed.layers.some((l) => l.segs.some((s) => s[4]));
        if (!hasExtrusion) issues.push("No extruded toolpath was produced — the model may be empty or not watertight.");
        const b = parsed.bbox, tol = 0.5;
        if (b.minX < -tol || b.minY < -tol || b.maxX > bedW + tol || b.maxY > bedL + tol) {
            issues.push(`The toolpath extends beyond the ${bedW}×${bedL} mm bed (X ${b.minX.toFixed(0)}–${b.maxX.toFixed(0)}, Y ${b.minY.toFixed(0)}–${b.maxY.toFixed(0)} mm) — it would print off the plate.`);
        }
        return issues;
    };

    const sliceMesh = async (mesh) => {
        const { Polyslice } = libs;
        const modelSel = $("slice-printer-model");
        const model = (modelSel && MODELS[modelSel.value]) || {};
        const cfg = { ...M5C, ...model };
        const slicer = new Polyslice(cfg);
        const t0 = performance.now();
        let g = slicer.slice(mesh);
        if (g && typeof g.then === "function") g = await g;
        const dt = Math.round(performance.now() - t0);
        if (typeof g !== "string" || g.length < 50) throw new Error("slicer returned no gcode");
        gcode = ANKER_PREAMBLE + g;
        const moves = (gcode.match(/^G1[ ]/gm) || []).length;
        showPreview();
        els.download.disabled = false;

        // Pre-print sanity check: only block (warn + Continue anyway) on serious issues.
        const issues = sanityCheck(cfg.buildPlateWidth, cfg.buildPlateLength);
        // Efficient AI vision check on the rendered preview (image only, never the
        // gcode). Optional — if no AI is configured or it errors, we proceed.
        setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves. Running AI check…`);
        try {
            const resp = await fetch("/api/slice/check", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ image: els.canvas.toDataURL("image/jpeg", 0.6) }),
            });
            if (resp.ok) {
                const d = await resp.json();
                if (d.serious && d.issue) issues.push("AI flagged: " + d.issue);
            }
        } catch (e) { /* AI check is optional */ }
        if (issues.length) {
            els.warning.innerHTML = "<strong>Possible problem:</strong> " + issues.map((i) => i.replace(/</g, "&lt;")).join(" ");
            els.warning.classList.remove("d-none");
            els.continueBtn.classList.remove("d-none");
            els.print.disabled = true;
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves, but a check flagged an issue (see below).`, "error");
        } else {
            els.warning.classList.add("d-none");
            els.continueBtn.classList.add("d-none");
            els.print.disabled = false;
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves. Download & inspect before printing a new model.`, "ok");
        }
    };

    els.run.addEventListener("click", async () => {
        const file = fileInput.files && fileInput.files[0];
        if (!file) { setStatus("Choose an STL file first.", "error"); return; }
        baseName = file.name.replace(/\.(stl|scad)$/i, "") || "model";
        els.run.disabled = true;
        try {
            await loadLibs();
            setStatus("Slicing…");
            const buf = await file.arrayBuffer();
            const geometry = new libs.STLLoader().parse(buf);
            const mesh = new libs.THREE.Mesh(geometry, new libs.THREE.MeshBasicMaterial());
            await sliceMesh(mesh);
        } catch (err) {
            setStatus("Slice failed: " + (err && err.message ? err.message : err), "error");
        } finally {
            els.run.disabled = false;
        }
    });

    els.layer.addEventListener("input", () => {
        if (parsed) els.layerLabel.textContent = `${(parseInt(els.layer.value, 10) || 0) + 1} / ${parsed.layers.length}`;
        draw();
    });
    els.travel.addEventListener("change", draw);

    fileInput.addEventListener("change", () => { els.run.disabled = !(fileInput.files && fileInput.files[0]); });

    els.continueBtn.addEventListener("click", () => {
        els.warning.classList.add("d-none");
        els.continueBtn.classList.add("d-none");
        els.print.disabled = false;
        setStatus("Override accepted — you can send to the printer. Inspect carefully.", "ok");
    });

    els.download.addEventListener("click", () => {
        if (!gcode) return;
        const a = document.createElement("a");
        a.href = URL.createObjectURL(new Blob([gcode], { type: "text/plain" }));
        a.download = baseName + ".gcode";
        a.click();
        URL.revokeObjectURL(a.href);
    });

    els.print.addEventListener("click", async () => {
        if (!gcode) return;
        if (!confirm("Send this sliced gcode to the printer and start printing?")) return;
        els.print.disabled = true;
        setStatus("Uploading to printer…");
        try {
            const fd = new FormData();
            fd.append("file", new Blob([gcode], { type: "text/plain" }), baseName + ".gcode");
            fd.append("print", "true");
            const resp = await fetch("/api/files/local", { method: "POST", body: fd });
            if (resp.ok) {
                setStatus("Sent to printer — printing started.", "ok");
                if (typeof flash_message === "function") flash_message("Sliced model sent to printer", "success");
            } else {
                const data = await resp.json().catch(() => ({}));
                setStatus("Upload failed: " + (data.error || resp.statusText), "error");
            }
        } catch (err) {
            setStatus("Upload error: " + (err && err.message ? err.message : err), "error");
        } finally {
            els.print.disabled = false;
        }
    });
}
