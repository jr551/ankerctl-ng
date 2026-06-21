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
        skirtGap: $("slice-skirt-gap"),
        scale: $("slice-scale"), scaleLabel: $("slice-scale-label"),
        addPart: $("slice-add-part"), addFile: $("slice-add-file"), partsList: $("slice-parts"),
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
    let currentGeometry = null;     // merged geometry actually sliced (built from parts)
    let parts = [];                 // bed components: [{ geo, name, scale, dx, dy }]
    let selectedPart = -1;          // index of the part the resize slider / drag acts on
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
            // Skirt gap: how far the skirt sits from the model. Tighter (default 2mm)
            // means a smaller footprint, so more parts fit on the bed.
            skirtDistance: els.skirtGap ? Math.max(0, parseFloat(els.skirtGap.value) || 2) : 2,
            skirtLineCount: 2,
            nozzleTemperature: clampInt(els.nozzleTemp.value, 150, 275, 215),
            bedTemperature: clampInt(els.bedTemp.value, 0, 100, 60),
        };
    };

    // ── Multi-part bed: components are merged into one geometry, then sliced ──
    // together so a single layer-by-layer toolpath covers every part.
    const mergeParts = () => {
        if (!parts.length || !libs) return null;
        const THREE = libs.THREE;
        const built = parts.map((p) => {
            const g = p.geo.clone();
            if (p.scale && p.scale !== 1) g.scale(p.scale, p.scale, p.scale);
            g.computeBoundingBox();
            const b = g.boundingBox;
            // centre in XY, drop base to z=0, then apply the part's layout offset
            g.translate(-(b.min.x + b.max.x) / 2 + (p.dx || 0), -(b.min.y + b.max.y) / 2 + (p.dy || 0), -b.min.z);
            return g.getAttribute("position").array;
        });
        let total = 0;
        built.forEach((a) => (total += a.length));
        const pos = new Float32Array(total);
        let o = 0;
        built.forEach((a) => { pos.set(a, o); o += a.length; });
        const merged = new THREE.BufferGeometry();
        merged.setAttribute("position", new THREE.BufferAttribute(pos, 3));
        merged.computeVertexNormals();
        return merged;
    };

    const renderParts = () => {
        if (!els.partsList) return;
        if (parts.length <= 1) { els.partsList.classList.add("d-none"); els.partsList.innerHTML = ""; return; }
        els.partsList.classList.remove("d-none");
        els.partsList.innerHTML = parts.map((p, i) =>
            `<span class="badge ${i === selectedPart ? "bg-success" : "bg-secondary"} slice-part-chip me-1" data-i="${i}" role="button">`
            + `${escapeHtml(p.name || ("part " + (i + 1)))}<span class="ms-1 slice-part-x" data-x="${i}" title="Remove">&times;</span></span>`
        ).join("");
    };
    const syncScaleSlider = () => {
        if (!els.scale) return;
        const s = parts[selectedPart] ? parts[selectedPart].scale : 1;
        els.scale.value = String(Math.round(s * 100));
        if (els.scaleLabel) els.scaleLabel.textContent = Math.round(s * 100);
    };
    // Replace all parts with one (STL / OpenSCAD / AI result).
    const setParts = (geo, name) => {
        parts = [{ geo, name: name || "model", scale: 1, dx: 0, dy: 0 }];
        selectedPart = 0;
        renderParts(); syncScaleSlider();
    };
    // Append a component, auto-placed to the right so it doesn't overlap.
    const addPart = (geo, name) => {
        let dx = 0;
        if (parts.length) {
            geo.computeBoundingBox();
            const w = parts.reduce((m, p) => { p.geo.computeBoundingBox(); const b = p.geo.boundingBox; return Math.max(m, (b.max.x - b.min.x) * (p.scale || 1)); }, 0);
            dx = parts[parts.length - 1].dx + Math.max(20, w);
        }
        parts.push({ geo, name: name || ("part " + (parts.length + 1)), scale: 1, dx, dy: 0 });
        selectedPart = parts.length - 1;
        renderParts(); syncScaleSlider();
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
        if (!parts.length) { setStatus("Load an STL or render OpenSCAD first.", "error"); return; }
        const cfg = readConfig();
        const runLabel = els.run.innerHTML;
        els.run.disabled = true; els.print.disabled = true; els.download.disabled = true;
        els.run.innerHTML = '<span class="spinner-border spinner-border-sm me-1" role="status"></span>Slicing…';
        setStatus(parts.length > 1 ? `Slicing ${parts.length} parts in the background…` : "Slicing in the background — the UI stays responsive…");
        try {
            await loadLibs();
            currentGeometry = mergeParts();
            if (!currentGeometry) throw new Error("no parts to slice");
            currentGeometry.computeBoundingBox();
            const bb = currentGeometry.boundingBox;
            const dimStr = ` · ${Math.round(bb.max.x - bb.min.x)}×${Math.round(bb.max.y - bb.min.y)}×${Math.round(bb.max.z - bb.min.z)} mm`;
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
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves${dimStr}. Running AI check…`);
            let aiChecked = false;
            try {
                const r = await fetch("/api/slice/check", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ image: els.canvas.toDataURL("image/jpeg", 0.6) }) });
                if (r.ok) { const d = await r.json(); aiChecked = !d.skipped; if (d.serious && d.issue) issues.push("AI flagged: " + d.issue); }
            } catch (e) { /* AI optional — surfaced below */ }
            applyVerdict(issues);
            const aiNote = aiChecked ? "" : " (AI check unavailable)";
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves${dimStr}.` + (issues.length ? " A check flagged an issue (below)." : " Download & inspect before printing a new model.") + aiNote, issues.length ? "error" : "ok");
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
    const endDrag = async (e) => {
        if (!dragging) return;
        const dx = offset.x - dragging.ox, dy = offset.y - dragging.oy;
        const multi = parts.length > 1 && selectedPart >= 0 && parts[selectedPart];
        dragging = null;
        try { els.canvas.releasePointerCapture(e.pointerId); } catch (_) {}
        if (multi && (Math.abs(dx) > 0.5 || Math.abs(dy) > 0.5)) {
            // Move just the selected part relative to the others, then re-slice the layout.
            parts[selectedPart].dx += dx; parts[selectedPart].dy += dy;
            offset = { x: 0, y: 0 };
            await sliceCurrent();
        } else {
            applyVerdict(offBedIssues()); // single part: keep the cheap post-slice offset
        }
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
            if (!v.ok) { setStatus("Invalid STL — " + v.error + ". The file may be corrupt or not really an STL.", "error"); fileInput.value = ""; return; }
            modelWarnings = file.size > STL_SIZE_WARN ? [`Large file (${(file.size / 1048576).toFixed(0)} MB) — slicing may be slow.`] : [];
            setParts(new libs.STLLoader().parse(buf), baseName);
            await sliceCurrent();
        } catch (err) { setStatus("Could not read STL: " + (err && err.message ? err.message : err), "error"); }
        finally { els.run.disabled = !parts.length; }
    });

    els.run.addEventListener("click", sliceCurrent);

    els.infill.addEventListener("input", () => { els.infillLabel.textContent = els.infill.value; });
    // Re-slice when a setting is committed.
    [els.model, els.layerHeight, els.pattern, els.adhesion, els.supports, els.infill, els.nozzleTemp, els.bedTemp, els.skirtGap]
        .forEach((el) => el && el.addEventListener("change", () => { if (parts.length) sliceCurrent(); }));

    // ── Resize the selected part ─────────────────────────────────────────
    if (els.scale) {
        els.scale.addEventListener("input", () => { if (els.scaleLabel) els.scaleLabel.textContent = els.scale.value; });
        els.scale.addEventListener("change", () => {
            if (!parts.length) return;
            const i = selectedPart >= 0 ? selectedPart : 0;
            parts[i].scale = Math.max(0.1, (parseInt(els.scale.value, 10) || 100) / 100);
            sliceCurrent();
        });
    }
    // ── Add another part (STL) ───────────────────────────────────────────
    if (els.addPart && els.addFile) {
        els.addPart.addEventListener("click", () => els.addFile.click());
        els.addFile.addEventListener("change", async () => {
            const file = els.addFile.files && els.addFile.files[0];
            els.addFile.value = "";
            if (!file) return;
            if (!/\.stl$/i.test(file.name)) { setStatus("Add a .stl file.", "error"); return; }
            try {
                await loadLibs();
                const buf = await file.arrayBuffer();
                if (!inspectSTL(buf, file.size).ok) { setStatus("That STL looks corrupt — not added.", "error"); return; }
                addPart(new libs.STLLoader().parse(buf), file.name.replace(/\.stl$/i, ""));
                await sliceCurrent();
            } catch (err) { setStatus("Could not add part: " + (err && err.message ? err.message : err), "error"); }
        });
    }
    // ── Parts list: select / remove ──────────────────────────────────────
    if (els.partsList) {
        els.partsList.addEventListener("click", async (e) => {
            const x = e.target.closest(".slice-part-x");
            if (x) {
                e.stopPropagation();
                const i = parseInt(x.getAttribute("data-x"), 10);
                if (i >= 0 && i < parts.length) {
                    parts.splice(i, 1);
                    if (selectedPart >= parts.length) selectedPart = parts.length - 1;
                    renderParts(); syncScaleSlider();
                    if (parts.length) await sliceCurrent(); else { baseGcode = ""; els.previewWrap.classList.add("d-none"); els.print.disabled = true; els.download.disabled = true; setStatus("All parts removed.", ""); }
                }
                return;
            }
            const chip = e.target.closest(".slice-part-chip");
            if (chip) { selectedPart = parseInt(chip.getAttribute("data-i"), 10); renderParts(); syncScaleSlider(); }
        });
    }

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
    // Compile a candidate SCAD: validates it actually builds (the worker rejects
    // with the OpenSCAD error on failure) and, on success, updates the 3D preview
    // and grabs a render so the AI can SEE its result. Returns {ok, image, error}.
    const compileAndRender = async (src) => {
        try {
            await loadLibs();
            const stl = await compileScad(src);
            setScadPreviewGeometry(new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength)));
            initScadPreview();
            let image = null;
            if (preview) {
                preview.controls.update();
                preview.renderer.render(preview.scene, preview.camera);
                image = preview.renderer.domElement.toDataURL("image/jpeg", 0.7);
            }
            return { ok: true, image };
        } catch (e) {
            return { ok: false, error: String((e && e.message) || e).slice(0, 400) };
        }
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
            clearTimeout(scadTimer); // don't let a pending live-preview clobber the slice result status
            baseName = "openscad-model";
            els.scadRender.disabled = true;
            showScadLoading(true);
            try {
                await loadLibs();
                setStatus("Compiling OpenSCAD…");
                const stl = await compileScad(src);
                setParts(new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength)), "openscad-model");
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
            const MAX_PASSES = 2; // capped at 2 for reliability + speed
            const iterations = clampInt(els.scadAiIters && els.scadAiIters.value, 1, MAX_PASSES, MAX_PASSES);
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
            // Reliability model: never apply OpenSCAD that doesn't compile. Each pass
            // is validated by actually building it; a good build becomes the new
            // "best" (and is applied + previewed). A bad build is NOT applied — the
            // next pass is told to fix it (with the real compile error). We always
            // end on the best compiling version, or report a clear failure.
            const original = els.scad.value;
            let best = original, bestImage = null;     // best compiling source so far
            let lastCandidate = null, lastError = null; // last AI output + its compile error
            let pushedUndo = false, appliedAny = false, didConverge = false;
            const callAI = (baseScad, p, imgs) => fetch("/api/openscad/edit", {
                method: "POST", headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ scad: baseScad, prompt: p, images: imgs || [] }),
            });
            try {
                for (let i = 1; i <= iterations; i++) {
                    let baseScad, promptForCall, images;
                    if (i === 1) {
                        setPhase(iterations > 1 ? `AI generating — pass 1 of ${iterations}` : "Asking the AI to build the model");
                        baseScad = original; promptForCall = prompt; images = scadRefImages;
                    } else if (lastError) {
                        // Previous candidate didn't compile — hand it back with the error to fix.
                        setPhase(`AI fixing a compile error — pass ${i} of ${iterations}`);
                        baseScad = lastCandidate;
                        promptForCall = `You wrote this OpenSCAD for the request "${prompt}", but it FAILED to compile with this OpenSCAD error:\n${lastError}\nReturn the COMPLETE corrected OpenSCAD that compiles and satisfies the request. Reply with ONLY the code.`;
                        images = [];
                    } else {
                        setPhase(`AI reviewing its result & refining — pass ${i} of ${iterations}`);
                        baseScad = best;
                        promptForCall = `You previously wrote this OpenSCAD for the request "${prompt}". The code above is your current version` + (bestImage ? ", and the attached image is a render of it" : "") + `. Improve it to better satisfy the request — fix mistakes, add missing detail, refine proportions. If it already fully satisfies the request, return it unchanged. Reply with ONLY the complete updated OpenSCAD.`;
                        images = bestImage ? [bestImage] : [];
                    }
                    const resp = await callAI(baseScad, promptForCall, images);
                    if (!resp.ok) { settled = true; setStatus(`AI request failed (HTTP ${resp.status}) on pass ${i} of ${iterations}.`, "error"); break; }
                    const d = await resp.json().catch(() => ({}));
                    if (d.skipped) { settled = true; setStatus(d.error ? `AI edit failed: ${d.error}` : "AI not configured (set it up under Camera & AI).", "error"); break; }
                    if (!d.scad) { settled = true; setStatus(`AI returned no code on pass ${i}.`, "error"); break; }
                    lastCandidate = d.scad;

                    setPhase(`Checking the model compiles — pass ${i} of ${iterations}`);
                    const built = await compileAndRender(d.scad);
                    if (built.ok) {
                        const converged = best && d.scad.trim() === best.trim();
                        if (!pushedUndo) { scadUndo.push(original); els.scadAiUndo.disabled = false; pushedUndo = true; }
                        best = d.scad; bestImage = built.image; lastError = null; appliedAny = true;
                        els.scad.value = best; els.scadRender.disabled = !best.trim();
                        const det = document.getElementById("slice-scad-details"); if (det) det.open = true;
                        if (converged) { didConverge = true; break; }
                        if (i < iterations) setPhase(`Pass ${i} compiled OK — refining further`);
                    } else {
                        lastError = built.error || "unknown compile error";
                        if (i < iterations) setPhase(`Pass ${i} didn't compile — asking the AI to fix it`);
                    }
                }
                if (!settled) {
                    settled = true;
                    const s = Math.round((performance.now() - t0) / 1000);
                    if (appliedAny && !lastError) {
                        scheduleLivePreview();
                        setStatus(`${didConverge ? `AI built & verified the model (converged) in ${s}s` : `AI built & verified the model in ${s}s`} — preview updated. Use Undo to revert.`, "ok");
                    } else if (appliedAny && lastError) {
                        scheduleLivePreview();
                        setStatus(`AI's last pass didn't compile — kept the best working version (verified). Use Undo to revert.`, "ok");
                    } else {
                        setStatus(`AI could not produce OpenSCAD that compiles${lastError ? ": " + lastError : ""}. Nothing was changed.`, "error");
                    }
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

    // ── 🧸 Kid Mode (Easy Maker): idea/STL -> AI -> auto-settings -> print ──
    const kid = {
        toggle: $("kid-mode-toggle"), overlay: $("kid-mode"), exit: $("kid-exit"),
        prompt: $("kid-prompt"), file: $("kid-file"), filename: $("kid-filename"), make: $("kid-make"),
        status: $("kid-status"), doneMsg: $("kid-done-msg"), donePreview: $("kid-done-preview"),
        print: $("kid-print"), again: $("kid-again"),
        stepChoose: $("kid-step-choose"), stepWork: $("kid-step-work"), stepDone: $("kid-step-done"),
    };
    if (kid.overlay && kid.make) {
        let kidFile = null;
        const kidShow = (on) => { kid.overlay.classList.toggle("d-none", !on); document.body.classList.toggle("kid-active", on); };
        const kidStep = (s) => { kid.stepChoose.classList.toggle("d-none", s !== "choose"); kid.stepWork.classList.toggle("d-none", s !== "work"); kid.stepDone.classList.toggle("d-none", s !== "done"); };
        const kidSay = (m) => { if (kid.status) kid.status.textContent = m; };
        const KID_FUN = ["Mixing the colours… 🎨", "Teaching the robot… 🤖", "Drawing your idea… ✏️", "Building it in 3D… 🧱", "Tidying it up… ✨"];
        if (kid.toggle) kid.toggle.addEventListener("click", () => { kidStep("choose"); kidShow(true); });
        if (kid.exit) kid.exit.addEventListener("click", () => kidShow(false));
        if (kid.file) kid.file.addEventListener("change", () => { kidFile = kid.file.files && kid.file.files[0]; kid.filename.textContent = kidFile ? "📦 " + kidFile.name : ""; });
        if (kid.again) kid.again.addEventListener("click", () => { kidStep("choose"); kid.make.disabled = false; kid.prompt.value = ""; kidFile = null; if (kid.file) kid.file.value = ""; kid.filename.textContent = ""; });

        // Kid-safe slice settings, written into the real form so sliceCurrent() picks them up.
        const kidSettings = () => {
            if (els.model) els.model.value = "m5c";
            if (els.layerHeight) els.layerHeight.value = "0.2";
            if (els.infill) { els.infill.value = "15"; if (els.infillLabel) els.infillLabel.textContent = "15"; }
            if (els.pattern) els.pattern.value = "grid";
            if (els.adhesion) els.adhesion.value = "skirt";
            if (els.supports) els.supports.checked = false;
            if (els.nozzleTemp) els.nozzleTemp.value = "215";
            if (els.bedTemp) els.bedTemp.value = "60";
        };
        // Keep kid prints small + quick: scale so the largest dimension is ~maxMM.
        const kidScaleSmall = (geo, maxMM) => {
            geo.computeBoundingBox(); const b = geo.boundingBox;
            const m = Math.max(b.max.x - b.min.x, b.max.y - b.min.y, b.max.z - b.min.z);
            if (m > maxMM && m > 0) geo.scale(maxMM / m, maxMM / m, maxMM / m);
        };
        // Build from an idea with one validate+fix retry (reuses the reliable compile check).
        const kidAIBuild = async (idea) => {
            const call = (scad, p) => fetch("/api/openscad/edit", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ scad, prompt: p, images: [] }) }).then((r) => (r.ok ? r.json() : {})).catch(() => ({}));
            const buildPrompt = `Design a simple, fun, 3D-printable model of: ${idea}. Make it a single solid object about 40-60mm, stable on a flat base, no text unless asked, printable without supports if possible. Reply with ONLY complete OpenSCAD code.`;
            let d = await call("", buildPrompt);
            if (d.skipped || !d.scad) return null;
            let built = await compileAndRender(d.scad);
            if (built.ok) return d.scad;
            kidSay("Fixing a little mistake… 🔧");
            d = await call(d.scad, `This OpenSCAD for "${idea}" failed to compile: ${built.error}. Return ONLY the corrected complete OpenSCAD.`);
            if (d.skipped || !d.scad) return null;
            built = await compileAndRender(d.scad);
            return built.ok ? d.scad : null;
        };

        const kidMake = async () => {
            const text = (kid.prompt.value || "").trim();
            if (!text && !kidFile) { kidSay("Type an idea or pick a file first! 😊"); return; }
            kid.make.disabled = true; kidStep("work"); kidSay("Getting started… 🚀");
            const funTimer = setInterval(() => kidSay(KID_FUN[Math.floor(performance.now() / 2500) % KID_FUN.length]), 2500);
            const bail = (msg) => { clearInterval(funTimer); kidSay(msg); kid.make.disabled = false; kidStep("choose"); };
            try {
                await loadLibs();
                let geo;
                if (kidFile) {
                    kidSay("Opening your file… 📂");
                    if (!/\.stl$/i.test(kidFile.name)) return bail("That's not a .stl file 🙈");
                    const buf = await kidFile.arrayBuffer();
                    if (!inspectSTL(buf, kidFile.size).ok) return bail("Hmm, that file looks broken 🙈");
                    geo = new libs.STLLoader().parse(buf);
                    baseName = (kidFile.name.replace(/\.stl$/i, "") || "my-model");
                } else {
                    kidSay("Asking the robot to design it… 🤖");
                    const scad = await kidAIBuild(text);
                    if (!scad) return bail("The robot got stuck 😅 try a simpler idea!");
                    const stl = await compileScad(scad);
                    geo = new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength));
                    baseName = "my-" + ((text.split(/\s+/)[0] || "model").replace(/[^a-z0-9]/gi, "").toLowerCase() || "model");
                    if (els.scad) els.scad.value = scad; // also reflect into power-mode editor
                }
                kidScaleSmall(geo, 60);
                setParts(geo, baseName);
                kidSay("Getting it ready to print… 🧱");
                kidSettings();
                await sliceCurrent(); // slices + AI check + verdict, draws the bed preview
                clearInterval(funTimer);
                if (!baseGcode) return bail("Oops, couldn't make that printable 😅 try another idea!");
                try { if (kid.donePreview && els.canvas) kid.donePreview.src = els.canvas.toDataURL("image/jpeg", 0.7); } catch (e) { /* preview optional */ }
                const warned = els.warning && !els.warning.classList.contains("d-none");
                kid.doneMsg.textContent = warned ? "Ready! It's a tricky one — ask a grown-up to watch 👀" : "Yay! Ready to print! 🎉";
                kidStep("done");
            } catch (err) {
                bail("Something went wrong 😅 try again!");
            } finally { kid.make.disabled = false; }
        };
        kid.make.addEventListener("click", kidMake);
        if (kid.print) kid.print.addEventListener("click", () => {
            if (!baseGcode) return;
            if (!confirm("Send your model to the printer?")) return;
            kid.print.disabled = true;
            kid.doneMsg.textContent = "Sending it to the printer… 🖨️";
            uploadGcode(finalGcode(), baseName + ".gcode", null).finally(() => {
                kid.print.disabled = false;
                kid.doneMsg.textContent = "Sent to the printer! 🎉 Go watch it print!";
            });
        });
    }
}
