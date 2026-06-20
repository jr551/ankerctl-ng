// Slice & Build tab — in-browser STL/OpenSCAD slicing via polyslice + three.js.
// three/polyslice/openscad are dynamically imported on first use (see the
// importmap in base.html) so they don't weigh down other pages.
//
// Flow: STL file or pasted OpenSCAD -> THREE mesh -> Polyslice(settings).slice()
// -> Marlin gcode -> preview on the printer bed (drag to reposition) -> a
// sanity check (off-bed/empty + AI vision) -> Download or POST /api/files/local.

const $ = (id) => document.getElementById(id);
const fileInput = $("slice-stl-file");
if (fileInput) {
    const els = {
        run: $("slice-run"), status: $("slice-status"),
        previewWrap: $("slice-preview-wrap"), canvas: $("slice-canvas"),
        layer: $("slice-layer"), layerLabel: $("slice-layer-label"), travel: $("slice-travel"),
        print: $("slice-print"), continueBtn: $("slice-continue"), warning: $("slice-warning"),
        download: $("slice-download"),
        scad: $("slice-scad"), scadRender: $("slice-scad-render"),
        scadLive: $("slice-scad-live"), scadPreviewCanvas: $("scad-preview-canvas"),
        scadAiPrompt: $("scad-ai-prompt"), scadAiGo: $("scad-ai-go"), scadAiUndo: $("scad-ai-undo"),
        scadAiIters: $("scad-ai-iters"),
        scadAiImages: $("scad-ai-images"), scadAiImagesNote: $("scad-ai-images-note"),
        model: $("slice-printer-model"), layerHeight: $("slice-layer-height"),
        infill: $("slice-infill"), infillLabel: $("slice-infill-label"), pattern: $("slice-pattern"),
        supports: $("slice-supports"), adhesion: $("slice-adhesion"),
        nozzleTemp: $("slice-nozzle-temp"), bedTemp: $("slice-bed-temp"),
        gcodeFile: $("slice-gcode-file"), gcodeSend: $("slice-gcode-send"),
    };

    const M5C = {
        nozzleDiameter: 0.4, filamentDiameter: 1.75,
        perimeterSpeed: 50, infillSpeed: 60, travelSpeed: 120,
        retractionDistance: 1.0, retractionSpeed: 40, extrusionMultiplier: 1.0, fanSpeed: 100,
    };
    const MODELS = {
        m5c: { buildPlateWidth: 220, buildPlateLength: 220 },
        m5: { buildPlateWidth: 235, buildPlateLength: 235 },
    };
    const ANKER_PREAMBLE = "M4899 T3 ; ankerctl-ng: enable v3 jerk + S-curve acceleration\n";

    let libs = null, openscadFactory = null;
    let currentGeometry = null;     // loaded model geometry
    let baseGcode = "";             // sliced gcode (model as polyslice placed it)
    let parsed = null;              // parsed toolpath of baseGcode
    let offset = { x: 0, y: 0 };    // mm offset applied by dragging
    let bedW = 220, bedL = 220, bedScale = 1;
    let baseName = "model";

    const setStatus = (msg, kind) => {
        els.status.textContent = msg || "";
        els.status.className = "form-text mb-0" + (kind === "error" ? " text-danger" : kind === "ok" ? " text-success" : "");
    };
    const clampInt = (v, lo, hi, d) => { v = parseInt(v, 10); return Number.isFinite(v) ? Math.min(hi, Math.max(lo, v)) : d; };
    // Escape all HTML metacharacters before inserting (possibly AI/model-derived)
    // text into innerHTML, to prevent XSS.
    const escapeHtml = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#x27;" }[c]));

    // ── Upload verification + complexity thresholds ──────────────────────
    const TRI_WARN = 400000;          // triangles: slicing gets slow beyond this
    const MOVE_WARN = 1000000;        // gcode moves: long print + large upload
    const STL_SIZE_WARN = 60 * 1048576;
    const GCODE_SIZE_WARN = 80 * 1048576;
    let modelWarnings = [];           // complexity heads-ups, merged into the slice verdict

    // Verify a file really is an STL by structure, not just its extension:
    // binary STL is exactly 84 + 50*N bytes; ASCII STL starts with "solid" + has facets.
    const inspectSTL = (buf, size) => {
        if (size < 15) return { ok: false, error: "the file is too small to be an STL" };
        if (size >= 84) {
            const n = new DataView(buf).getUint32(80, true);
            if (n > 10000000) return { ok: false, error: "binary STL triangle count is implausibly large" };
            if (84 + n * 50 === size) return { ok: true, kind: "binary" };
        }
        const head = new TextDecoder().decode(new Uint8Array(buf, 0, Math.min(size, 2048))).trim().toLowerCase();
        if (head.startsWith("solid") && (head.includes("facet") || head.includes("vertex"))) return { ok: true, kind: "ascii" };
        if (head.startsWith("solid") && size > 200) return { ok: true, kind: "ascii" }; // facets past the 2KB head
        return { ok: false, error: "this does not look like a valid STL (binary or ASCII)" };
    };

    // Sanity-check that an uploaded file is text gcode, not binary or some other format.
    const looksLikeGcode = (text) => {
        const sample = text.slice(0, 8192);
        if (sample.indexOf("\u0000") >= 0) return false; // binary
        return /(^|\n)\s*(;|G0\b|G1\b|G28|G90|G91|G92|M10[49]|M140|M8\d|T\d)/i.test(sample);
    };

    const triangleCount = (geometry) => {
        const pos = geometry && geometry.getAttribute && geometry.getAttribute("position");
        return pos ? Math.round(pos.count / 3) : 0;
    };

    const loadLibs = async () => {
        if (libs) return libs;
        setStatus("Loading slicer (one-time)…");
        const THREE = await import("three");
        window.THREE = THREE;
        const Polyslice = (await import("@jgphilpott/polyslice")).default;
        const { STLLoader } = await import("three/examples/jsm/loaders/STLLoader.js");
        const { OrbitControls } = await import("three/examples/jsm/controls/OrbitControls.js");
        libs = { THREE, Polyslice, STLLoader, OrbitControls };
        return libs;
    };
    // OpenSCAD compiles in a Web Worker so the ~7 MB WASM + CGAL never freeze the
    // UI (the 3D preview's orbit controls stay smooth during a compile).
    let scadWorker = null, scadReqId = 0;
    const scadPending = new Map();
    const getScadWorker = () => {
        if (scadWorker) return scadWorker;
        scadWorker = new Worker("/static/openscad-worker.js", { type: "module" });
        scadWorker.onmessage = (e) => {
            const { id, stl, error } = e.data || {};
            const p = scadPending.get(id);
            if (!p) return;
            scadPending.delete(id);
            if (error) p.reject(new Error(error)); else p.resolve(stl);
        };
        scadWorker.onerror = (e) => {
            scadPending.forEach((p) => p.reject(new Error(e.message || "OpenSCAD worker error")));
            scadPending.clear();
            scadWorker = null;
        };
        return scadWorker;
    };
    const compileScad = (src) => new Promise((resolve, reject) => {
        const id = ++scadReqId;
        scadPending.set(id, { resolve, reject });
        getScadWorker().postMessage({ id, scad: src });
    });

    // Polyslice runs in its own Web Worker so slicing a complex model never
    // freezes the UI. Falls back to main-thread slicing if the worker can't start.
    let sliceWorker = null, sliceReqId = 0;
    const slicePending = new Map();
    const getSliceWorker = () => {
        if (sliceWorker) return sliceWorker;
        sliceWorker = new Worker("/static/slice-worker.js", { type: "module" });
        sliceWorker.onmessage = (e) => {
            const { id, gcode, error } = e.data || {};
            const p = slicePending.get(id);
            if (!p) return;
            slicePending.delete(id);
            if (error) p.reject(new Error(error)); else p.resolve(gcode);
        };
        sliceWorker.onerror = (e) => {
            slicePending.forEach((p) => p.reject(new Error(e.message || "slice worker error")));
            slicePending.clear();
            sliceWorker = null;
        };
        return sliceWorker;
    };
    // Slice a geometry off the main thread; copies the typed arrays so the
    // source geometry (used for re-slicing and the 3D preview) stays intact.
    const sliceInWorker = (geometry, cfg) => new Promise((resolve, reject) => {
        const pos = geometry.getAttribute("position");
        if (!pos) { reject(new Error("geometry has no vertices")); return; }
        const positions = pos.array.slice();
        const normAttr = geometry.getAttribute("normal");
        const normals = normAttr ? normAttr.array.slice() : null;
        const idxAttr = geometry.getIndex();
        const index = idxAttr ? idxAttr.array.slice() : null;
        const id = ++sliceReqId;
        slicePending.set(id, { resolve, reject });
        const transfer = [positions.buffer];
        if (normals) transfer.push(normals.buffer);
        if (index) transfer.push(index.buffer);
        getSliceWorker().postMessage({ id, positions, normals, index, cfg }, transfer);
    });
    // Worker first; on any worker failure fall back to slicing on the main thread
    // so a stricter browser still works (just less smooth).
    const sliceGeometry = async (geometry, cfg) => {
        try {
            return await sliceInWorker(geometry, cfg);
        } catch (err) {
            let g = new libs.Polyslice(cfg).slice(new libs.THREE.Mesh(geometry.clone(), new libs.THREE.MeshBasicMaterial()));
            if (g && typeof g.then === "function") g = await g;
            return g;
        }
    };
    const showScadLoading = (on) => {
        const l = document.getElementById("scad-preview-loading");
        if (l) l.classList.toggle("d-none", !on);
    };

    const readConfig = () => {
        const model = MODELS[els.model.value] || MODELS.m5c;
        bedW = model.buildPlateWidth; bedL = model.buildPlateLength;
        const adhesion = els.adhesion.value;
        return {
            ...M5C, ...model,
            layerHeight: parseFloat(els.layerHeight.value) || 0.2,
            infillDensity: clampInt(els.infill.value, 0, 100, 20),
            infillPattern: els.pattern.value || "grid",
            supportEnabled: els.supports.checked,
            adhesionEnabled: adhesion !== "none",
            adhesionType: adhesion === "none" ? "skirt" : adhesion,
            nozzleTemperature: clampInt(els.nozzleTemp.value, 150, 275, 215),
            bedTemperature: clampInt(els.bedTemp.value, 0, 100, 60),
        };
    };

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
            if (cmd === "G92") { for (let j = 1; j < p.length; j++) { const v = parseFloat(p[j].slice(1)); if (isNaN(v)) continue; const c = p[j][0].toUpperCase(); if (c === "X") x = v; else if (c === "Y") y = v; else if (c === "Z") z = v; else if (c === "E") e = v; } continue; }
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
        if (!isFinite(minX)) { minX = 0; minY = 0; maxX = bedW; maxY = bedL; }
        return { layers, bbox: { minX, minY, maxX, maxY } };
    };

    // Draw the whole printer bed (grid + border) with the toolpath placed on it.
    const drawBed = () => {
        const cv = els.canvas, ctx = cv.getContext("2d"), W = cv.width, H = cv.height, pad = 18;
        ctx.fillStyle = "#0d1117"; ctx.fillRect(0, 0, W, H);
        bedScale = Math.min((W - 2 * pad) / bedW, (H - 2 * pad) / bedL);
        const ox = (W - bedW * bedScale) / 2, oy = (H - bedL * bedScale) / 2;
        const tx = (x) => ox + x * bedScale, ty = (y) => H - (oy + y * bedScale); // flip Y: bed front at bottom
        // bed plate
        ctx.fillStyle = "#11181f"; ctx.fillRect(tx(0), ty(bedL), bedW * bedScale, bedL * bedScale);
        ctx.strokeStyle = "rgba(136,243,135,0.4)"; ctx.lineWidth = 1.5; ctx.strokeRect(tx(0), ty(bedL), bedW * bedScale, bedL * bedScale);
        ctx.strokeStyle = "rgba(255,255,255,0.06)"; ctx.lineWidth = 1; ctx.beginPath();
        for (let g = 20; g < bedW; g += 20) { ctx.moveTo(tx(g), ty(0)); ctx.lineTo(tx(g), ty(bedL)); }
        for (let g = 20; g < bedL; g += 20) { ctx.moveTo(tx(0), ty(g)); ctx.lineTo(tx(bedW), ty(g)); }
        ctx.stroke();
        if (!parsed) return;
        const top = parseInt(els.layer.value, 10) || 0, showTravel = els.travel.checked, oX = offset.x, oY = offset.y;
        for (let li = 0; li <= top && li < parsed.layers.length; li++) {
            const isTop = li === top, segs = parsed.layers[li].segs;
            if (showTravel) { ctx.strokeStyle = "rgba(120,160,255,0.22)"; ctx.lineWidth = 0.5; ctx.beginPath(); for (const s of segs) if (!s[4]) { ctx.moveTo(tx(s[0] + oX), ty(s[1] + oY)); ctx.lineTo(tx(s[2] + oX), ty(s[3] + oY)); } ctx.stroke(); }
            ctx.strokeStyle = isTop ? "#88f387" : "rgba(136,243,135,0.32)"; ctx.lineWidth = isTop ? 1.4 : 0.7; ctx.beginPath();
            for (const s of segs) if (s[4]) { ctx.moveTo(tx(s[0] + oX), ty(s[1] + oY)); ctx.lineTo(tx(s[2] + oX), ty(s[3] + oY)); } ctx.stroke();
        }
    };

    const showPreview = () => {
        parsed = parseGcode(baseGcode);
        els.previewWrap.classList.remove("d-none");
        els.layer.max = Math.max(0, parsed.layers.length - 1);
        els.layer.value = parsed.layers.length - 1;
        els.layer.disabled = parsed.layers.length <= 1;
        els.layerLabel.textContent = `${parsed.layers.length} / ${parsed.layers.length}`;
        drawBed();
    };

    const offBedIssues = () => {
        const issues = [];
        if (!parsed.layers.some((l) => l.segs.some((s) => s[4]))) issues.push("No extruded toolpath was produced — the model may be empty.");
        const b = parsed.bbox, tol = 0.5;
        if (b.minX + offset.x < -tol || b.minY + offset.y < -tol || b.maxX + offset.x > bedW + tol || b.maxY + offset.y > bedL + tol)
            issues.push(`The model is off the ${bedW}×${bedL} mm bed — drag it back onto the plate.`);
        return issues;
    };

    const applyVerdict = (issues) => {
        if (issues.length) {
            els.warning.innerHTML = "<strong>Heads up:</strong> " + issues.map((i) => escapeHtml(i)).join(" ");
            els.warning.classList.remove("d-none"); els.continueBtn.classList.remove("d-none"); els.print.disabled = true;
        } else {
            els.warning.classList.add("d-none"); els.continueBtn.classList.add("d-none"); els.print.disabled = false;
        }
    };

    const sliceCurrent = async () => {
        if (!currentGeometry) { setStatus("Load an STL or render OpenSCAD first.", "error"); return; }
        const cfg = readConfig();
        const runLabel = els.run.innerHTML;
        els.run.disabled = true; els.print.disabled = true; els.download.disabled = true;
        els.run.innerHTML = '<span class="spinner-border spinner-border-sm me-1" role="status"></span>Slicing…';
        setStatus("Slicing in the background — the UI stays responsive…");
        try {
            await loadLibs();
            const t0 = performance.now();
            // Runs in a Web Worker (with a main-thread fallback) so the page never freezes.
            const g = await sliceGeometry(currentGeometry, cfg);
            const dt = Math.round(performance.now() - t0);
            if (typeof g !== "string" || g.length < 50) throw new Error("slicer returned no gcode");
            baseGcode = ANKER_PREAMBLE + g;
            offset = { x: 0, y: 0 };
            const moves = (baseGcode.match(/^G1[ ]/gm) || []).length;
            showPreview();
            els.download.disabled = false;
            const issues = offBedIssues();
            if (modelWarnings.length) issues.push(...modelWarnings);
            const tris = triangleCount(currentGeometry);
            if (tris > TRI_WARN) issues.push(`Very complex model (~${tris.toLocaleString()} triangles) — slicing is slow and the print may take a long time.`);
            if (moves > MOVE_WARN) issues.push(`Very large toolpath (${moves.toLocaleString()} moves) — long print and a big upload; inspect carefully first.`);
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves. Running AI check…`);
            let aiChecked = false;
            try {
                const r = await fetch("/api/slice/check", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ image: els.canvas.toDataURL("image/jpeg", 0.6) }) });
                if (r.ok) { const d = await r.json(); aiChecked = !d.skipped; if (d.serious && d.issue) issues.push("AI flagged: " + d.issue); }
            } catch (e) { /* AI optional — surfaced below */ }
            applyVerdict(issues);
            const aiNote = aiChecked ? "" : " (AI check unavailable)";
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves.` + (issues.length ? " A check flagged an issue (below)." : " Download & inspect before printing a new model.") + aiNote, issues.length ? "error" : "ok");
        } catch (err) { setStatus("Slice failed: " + (err && err.message ? err.message : err), "error"); }
        finally { els.run.disabled = false; els.run.innerHTML = runLabel; }
    };

    // Apply the drag offset to every move for the final output gcode.
    const finalGcode = () => {
        if (!offset.x && !offset.y) return baseGcode;
        const dx = offset.x, dy = offset.y;
        return baseGcode.replace(/^G[01] [^\n]*/gm, (line) =>
            line.replace(/X(-?\d*\.?\d+)/, (_, v) => "X" + (parseFloat(v) + dx).toFixed(3))
                .replace(/Y(-?\d*\.?\d+)/, (_, v) => "Y" + (parseFloat(v) + dy).toFixed(3)));
    };

    const uploadGcode = async (text, name, btn) => {
        if (btn) btn.disabled = true;
        setStatus("Uploading to printer…");
        try {
            const fd = new FormData();
            fd.append("file", new Blob([text], { type: "text/plain" }), name);
            fd.append("print", "true");
            const resp = await fetch("/api/files/local", { method: "POST", body: fd });
            if (resp.ok) { setStatus("Sent to printer — printing started.", "ok"); if (typeof flash_message === "function") flash_message("Sent to printer", "success"); }
            else { const d = await resp.json().catch(() => ({})); setStatus("Upload failed: " + (d.error || resp.statusText), "error"); }
        } catch (err) { setStatus("Upload error: " + (err && err.message ? err.message : err), "error"); }
        finally { if (btn) btn.disabled = false; }
    };

    // ── Drag-to-reposition on the bed ────────────────────────────────────
    let dragging = null;
    const pxToMm = () => bedScale * (els.canvas.clientWidth / els.canvas.width || 1);
    els.canvas.addEventListener("pointerdown", (e) => {
        if (!parsed) return;
        dragging = { sx: e.clientX, sy: e.clientY, ox: offset.x, oy: offset.y };
        els.canvas.setPointerCapture(e.pointerId);
    });
    els.canvas.addEventListener("pointermove", (e) => {
        if (!dragging) return;
        const f = pxToMm();
        offset.x = dragging.ox + (e.clientX - dragging.sx) / f;
        offset.y = dragging.oy - (e.clientY - dragging.sy) / f; // canvas Y is flipped vs bed Y
        drawBed();
    });
    const endDrag = (e) => {
        if (!dragging) return;
        dragging = null;
        try { els.canvas.releasePointerCapture(e.pointerId); } catch (_) {}
        applyVerdict(offBedIssues()); // re-check bed bounds after the move
    };
    els.canvas.addEventListener("pointerup", endDrag);
    els.canvas.addEventListener("pointercancel", endDrag);

    // ── Inputs ───────────────────────────────────────────────────────────
    fileInput.addEventListener("change", async () => {
        const file = fileInput.files && fileInput.files[0];
        if (!file) return;
        if (!/\.stl$/i.test(file.name)) { setStatus("Please choose a .stl file.", "error"); fileInput.value = ""; return; }
        baseName = file.name.replace(/\.(stl|scad)$/i, "") || "model";
        els.run.disabled = true;
        try {
            await loadLibs();
            const buf = await file.arrayBuffer();
            const v = inspectSTL(buf, file.size);
            if (!v.ok) { setStatus("Invalid STL — " + v.error + ". The file may be corrupt or not really an STL.", "error"); currentGeometry = null; fileInput.value = ""; return; }
            modelWarnings = file.size > STL_SIZE_WARN ? [`Large file (${(file.size / 1048576).toFixed(0)} MB) — slicing may be slow.`] : [];
            currentGeometry = new libs.STLLoader().parse(buf);
            await sliceCurrent();
        } catch (err) { setStatus("Could not read STL: " + (err && err.message ? err.message : err), "error"); }
        finally { els.run.disabled = !currentGeometry; }
    });

    els.run.addEventListener("click", sliceCurrent);

    els.infill.addEventListener("input", () => { els.infillLabel.textContent = els.infill.value; });
    // Re-slice when a setting is committed.
    [els.model, els.layerHeight, els.pattern, els.adhesion, els.supports, els.infill, els.nozzleTemp, els.bedTemp]
        .forEach((el) => el && el.addEventListener("change", () => { if (currentGeometry) sliceCurrent(); }));

    els.layer.addEventListener("input", () => { if (parsed) els.layerLabel.textContent = `${(parseInt(els.layer.value, 10) || 0) + 1} / ${parsed.layers.length}`; drawBed(); });
    els.travel.addEventListener("change", drawBed);

    els.continueBtn.addEventListener("click", () => {
        els.warning.classList.add("d-none"); els.continueBtn.classList.add("d-none"); els.print.disabled = false;
        setStatus("Override accepted — inspect carefully before/while printing.", "ok");
    });

    els.download.addEventListener("click", () => {
        if (!baseGcode) return;
        const a = document.createElement("a");
        a.href = URL.createObjectURL(new Blob([finalGcode()], { type: "text/plain" }));
        a.download = baseName + ".gcode"; a.click(); URL.revokeObjectURL(a.href);
    });

    els.print.addEventListener("click", () => {
        if (!baseGcode) return;
        if (!confirm("Send this sliced gcode to the printer and start printing?")) return;
        uploadGcode(finalGcode(), baseName + ".gcode", els.print);
    });

    // ── OpenSCAD: live 3D preview + slice ────────────────────────────────
    let preview = null, scadTimer = null;
    const PREVIEW_BED = { w: 220, l: 220, h: 250 };
    const initScadPreview = () => {
        if (preview || !els.scadPreviewCanvas) return;
        const THREE = libs.THREE, cv = els.scadPreviewCanvas, size = cv.clientWidth || 300;
        const renderer = new THREE.WebGLRenderer({ canvas: cv, antialias: true, alpha: true, preserveDrawingBuffer: true });
        renderer.setPixelRatio(window.devicePixelRatio || 1);
        renderer.setSize(size, size, false);
        const scene = new THREE.Scene();
        const camera = new THREE.PerspectiveCamera(45, 1, 0.5, 8000);
        camera.up.set(0, 0, 1); // STL / OpenSCAD is Z-up
        scene.add(new THREE.AmbientLight(0xffffff, 0.8));
        const dir = new THREE.DirectionalLight(0xffffff, 0.85); dir.position.set(0.6, -1, 1.4); scene.add(dir);

        // Ghosted AnkerMake-style printer reference, for context (and fun).
        const B = PREVIEW_BED;
        const bed = new THREE.Mesh(new THREE.BoxGeometry(B.w, B.l, 2),
            new THREE.MeshBasicMaterial({ color: 0x88f387, transparent: true, opacity: 0.05, side: THREE.DoubleSide }));
        bed.position.set(0, 0, -1); scene.add(bed);
        const grid = new THREE.GridHelper(B.w, 11, 0x88f387, 0x335c39);
        grid.rotation.x = Math.PI / 2; grid.material.transparent = true; grid.material.opacity = 0.3; scene.add(grid);
        const ghostLine = (geom) => new THREE.LineSegments(new THREE.EdgesGeometry(geom),
            new THREE.LineBasicMaterial({ color: 0x88f387, transparent: true, opacity: 0.18 }));
        const volume = ghostLine(new THREE.BoxGeometry(B.w, B.l, B.h)); volume.position.set(0, 0, B.h / 2); scene.add(volume);
        const gantry = ghostLine(new THREE.BoxGeometry(B.w, 14, 14)); gantry.position.set(0, 0, B.h - 7); scene.add(gantry);

        const controls = new libs.OrbitControls(camera, renderer.domElement);
        controls.enableDamping = true;
        camera.position.set(340, -370, 300);
        camera.updateProjectionMatrix();
        controls.target.set(0, 0, 95); controls.update();
        preview = { renderer, scene, camera, controls, mesh: null };
        const loop = () => { if (!preview) return; controls.update(); renderer.render(scene, camera); requestAnimationFrame(loop); };
        loop();
    };
    const setScadPreviewGeometry = (geo) => {
        const THREE = libs.THREE;
        initScadPreview();
        if (!preview) return;
        if (preview.mesh) { preview.scene.remove(preview.mesh); preview.mesh.geometry.dispose(); }
        geo.computeVertexNormals(); geo.computeBoundingBox();
        const bb = geo.boundingBox;
        // Centre in X/Y and sit the base on the bed (z = 0).
        geo.translate(-(bb.min.x + bb.max.x) / 2, -(bb.min.y + bb.max.y) / 2, -bb.min.z);
        const mesh = new THREE.Mesh(geo, new THREE.MeshStandardMaterial({ color: 0x88f387, metalness: 0.1, roughness: 0.6, flatShading: true }));
        preview.scene.add(mesh); preview.mesh = mesh;
    };
    // Compile + render the current SCAD and grab the preview canvas as an image,
    // so the AI can SEE what it produced during iterative refinement.
    const renderScadToImage = async (src) => {
        try {
            await loadLibs();
            const stl = await compileScad(src);
            setScadPreviewGeometry(new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength)));
            initScadPreview();
            if (!preview) return null;
            preview.controls.update();
            preview.renderer.render(preview.scene, preview.camera);
            return preview.renderer.domElement.toDataURL("image/jpeg", 0.7);
        } catch (e) { return null; }
    };
    const scheduleLivePreview = () => {
        if (!els.scadLive || !els.scadLive.checked || !els.scad.value.trim()) return;
        clearTimeout(scadTimer);
        scadTimer = setTimeout(async () => {
            const src = els.scad.value.trim();
            if (!src) return;
            showScadLoading(true);
            try {
                await loadLibs();
                setStatus("Rendering preview…");
                const stl = await compileScad(src);
                setScadPreviewGeometry(new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength)));
                setStatus("Live preview updated — click ‘Slice this’ to slice for printing.", "ok");
            } catch (err) { setStatus("OpenSCAD: " + (err && err.message ? err.message : err), "error"); }
            finally { showScadLoading(false); }
        }, 900);
    };
    if (els.scad && els.scadRender) {
        els.scadRender.disabled = !els.scad.value.trim();
        els.scad.addEventListener("input", () => { els.scadRender.disabled = !els.scad.value.trim(); scheduleLivePreview(); });
        if (els.scadLive) els.scadLive.addEventListener("change", scheduleLivePreview);
        els.scadRender.addEventListener("click", async () => {
            const src = els.scad.value.trim();
            if (!src) { setStatus("Paste some OpenSCAD first.", "error"); return; }
            baseName = "openscad-model";
            els.scadRender.disabled = true;
            showScadLoading(true);
            try {
                await loadLibs();
                setStatus("Compiling OpenSCAD…");
                const stl = await compileScad(src);
                currentGeometry = new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength));
                await sliceCurrent();
            } catch (err) { setStatus("OpenSCAD: " + (err && err.message ? err.message : err), "error"); }
            finally { els.scadRender.disabled = !els.scad.value.trim(); showScadLoading(false); }
        });
    }

    // ── OpenSCAD AI edit (with undo) + reference images ──────────────────
    const scadUndo = [];
    let scadRefImages = [];
    if (els.scadAiImages) {
        els.scadAiImages.addEventListener("change", async () => {
            const files = Array.from(els.scadAiImages.files || []).slice(0, 4);
            scadRefImages = [];
            for (const f of files) {
                try { scadRefImages.push(await new Promise((res, rej) => { const fr = new FileReader(); fr.onload = () => res(fr.result); fr.onerror = rej; fr.readAsDataURL(f); })); } catch (e) { /* skip */ }
            }
            if (els.scadAiImagesNote) els.scadAiImagesNote.textContent = scadRefImages.length ? `${scadRefImages.length} reference image${scadRefImages.length > 1 ? "s" : ""} attached` : "";
        });
    }
    if (els.scadAiGo) {
        const aiGoLabel = els.scadAiGo.innerHTML;
        els.scadAiGo.addEventListener("click", async () => {
            const prompt = (els.scadAiPrompt.value || "").trim();
            if (!prompt) { setStatus("Describe the change for the AI first.", "error"); els.scadAiPrompt.focus(); return; }
            const iterations = clampInt(els.scadAiIters && els.scadAiIters.value, 1, 5, 1);
            // Loading indicator: spinner on the button + a live elapsed counter and
            // frequent phase updates, because each AI pass can take 20-60s and the
            // user must never think it has frozen.
            els.scadAiGo.disabled = true;
            els.scadAiPrompt.disabled = true;
            if (els.scadAiIters) els.scadAiIters.disabled = true;
            els.scadAiGo.innerHTML = '<span class="spinner-border spinner-border-sm" role="status" aria-hidden="true"></span>';
            const t0 = performance.now();
            let phase = "Asking the AI to edit the model", settled = false;
            // settled guards against the 400ms ticker overwriting a terminal (error/done) message.
            const tick = () => { if (settled) return; const s = Math.round((performance.now() - t0) / 1000); setStatus(`${phase}… ${s}s`); };
            const setPhase = (p) => { phase = p; tick(); };
            const timer = setInterval(tick, 400);
            tick();
            let pushedUndo = false, scad = els.scad.value, applied = false, didConverge = false;
            try {
                for (let i = 1; i <= iterations; i++) {
                    let promptForCall, images;
                    if (i === 1) {
                        setPhase(iterations > 1 ? `AI editing — pass 1 of ${iterations}` : "Asking the AI to edit the model");
                        promptForCall = prompt; images = scadRefImages;
                    } else {
                        setPhase(`Rendering the result so the AI can review it — pass ${i} of ${iterations}`);
                        const img = await renderScadToImage(scad);
                        setPhase(`AI reviewing its own result & refining — pass ${i} of ${iterations}`);
                        promptForCall = `You previously wrote this OpenSCAD for the request: "${prompt}". The code above is your current version` + (img ? ", and the attached image is a render of it" : "") + `. Critically review it and improve it so it better satisfies the request — fix mistakes, add missing detail, refine proportions and geometry. If it already fully satisfies the request, return it unchanged. Reply with ONLY the complete updated OpenSCAD.`;
                        images = img ? [img] : [];
                    }
                    const resp = await fetch("/api/openscad/edit", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ scad, prompt: promptForCall, images }) });
                    if (!resp.ok) { settled = true; setStatus(`AI request failed (HTTP ${resp.status}) on pass ${i} of ${iterations}.`, "error"); break; }
                    const d = await resp.json().catch(() => ({}));
                    if (d.skipped) { settled = true; setStatus(d.error ? `AI edit failed on pass ${i}: ${d.error}` : "AI not configured (set it up under Camera & AI).", "error"); break; }
                    if (!d.scad) { settled = true; setStatus(`AI returned no code on pass ${i}.`, "error"); break; }
                    if (!pushedUndo) { scadUndo.push(els.scad.value); els.scadAiUndo.disabled = false; pushedUndo = true; }
                    const converged = d.scad.trim() === scad.trim();
                    scad = d.scad;
                    els.scad.value = scad;
                    els.scadRender.disabled = !scad.trim();
                    applied = true;
                    const det = document.getElementById("slice-scad-details"); if (det) det.open = true;
                    if (converged && i > 1) { didConverge = true; break; }
                    if (i < iterations) setPhase(`Pass ${i} of ${iterations} applied — continuing to refine`);
                }
                if (applied) {
                    settled = true;
                    scheduleLivePreview();
                    const s = Math.round((performance.now() - t0) / 1000);
                    setStatus(`${didConverge ? `AI refined the model and converged in ${s}s` : `AI finished in ${s}s`} — rendering preview. Use Undo to revert.`, "ok");
                }
            } catch (err) { settled = true; setStatus("AI edit error: " + (err && err.message ? err.message : err), "error"); }
            finally { clearInterval(timer); els.scadAiGo.disabled = false; els.scadAiGo.innerHTML = aiGoLabel; els.scadAiPrompt.disabled = false; if (els.scadAiIters) els.scadAiIters.disabled = false; }
        });
    }
    if (els.scadAiUndo) {
        els.scadAiUndo.addEventListener("click", () => {
            if (!scadUndo.length) return;
            els.scad.value = scadUndo.pop();
            els.scadAiUndo.disabled = scadUndo.length === 0;
            els.scadRender.disabled = !els.scad.value.trim();
            scheduleLivePreview();
            setStatus("Reverted the last AI edit.", "ok");
        });
    }

    // Raw .gcode upload (moved here from the old GCode tab)
    if (els.gcodeFile && els.gcodeSend) {
        els.gcodeSend.addEventListener("click", async () => {
            const file = els.gcodeFile.files && els.gcodeFile.files[0];
            if (!file) { setStatus("Choose a .gcode file first.", "error"); return; }
            if (!/\.(gcode|gco|g)$/i.test(file.name)) { setStatus("Please choose a .gcode file.", "error"); return; }
            const text = await file.text();
            if (!looksLikeGcode(text)) { setStatus("That file does not look like gcode — it has no recognisable G/M commands. Not sending it to the printer.", "error"); return; }
            const warn = file.size > GCODE_SIZE_WARN ? `\n\nNote: this is a large file (${(file.size / 1048576).toFixed(0)} MB) and may take a while to upload.` : "";
            if (!confirm("Upload and print " + file.name + "?" + warn)) return;
            uploadGcode(text, file.name, els.gcodeSend);
        });
    }
}
