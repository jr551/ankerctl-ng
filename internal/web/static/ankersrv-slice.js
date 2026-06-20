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
    const loadOpenscad = async () => {
        if (openscadFactory) return openscadFactory;
        setStatus("Loading OpenSCAD (one-time, ~7 MB)…");
        openscadFactory = (await import("/static/vendor/openscad/openscad.js")).default;
        return openscadFactory;
    };
    const compileScad = async (src) => {
        const OpenSCAD = await loadOpenscad();
        const errs = [];
        const inst = await OpenSCAD({ noInitialRun: true, printErr: (l) => errs.push(l) });
        inst.FS.writeFile("/in.scad", src);
        const code = inst.callMain(["/in.scad", "-o", "/out.stl"]);
        if (code !== 0) throw new Error(errs.length ? errs.join(" ").slice(0, 400) : "OpenSCAD exited " + code);
        return inst.FS.readFile("/out.stl");
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
            els.warning.innerHTML = "<strong>Heads up:</strong> " + issues.map((i) => i.replace(/</g, "&lt;")).join(" ");
            els.warning.classList.remove("d-none"); els.continueBtn.classList.remove("d-none"); els.print.disabled = true;
        } else {
            els.warning.classList.add("d-none"); els.continueBtn.classList.add("d-none"); els.print.disabled = false;
        }
    };

    const sliceCurrent = async () => {
        if (!currentGeometry) { setStatus("Load an STL or render OpenSCAD first.", "error"); return; }
        const cfg = readConfig();
        els.run.disabled = true; els.print.disabled = true; els.download.disabled = true;
        setStatus("Slicing…");
        try {
            await loadLibs();
            const mesh = new libs.THREE.Mesh(currentGeometry.clone(), new libs.THREE.MeshBasicMaterial());
            const t0 = performance.now();
            let g = new libs.Polyslice(cfg).slice(mesh);
            if (g && typeof g.then === "function") g = await g;
            const dt = Math.round(performance.now() - t0);
            if (typeof g !== "string" || g.length < 50) throw new Error("slicer returned no gcode");
            baseGcode = ANKER_PREAMBLE + g;
            offset = { x: 0, y: 0 };
            const moves = (baseGcode.match(/^G1[ ]/gm) || []).length;
            showPreview();
            els.download.disabled = false;
            const issues = offBedIssues();
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves. Running AI check…`);
            try {
                const r = await fetch("/api/slice/check", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ image: els.canvas.toDataURL("image/jpeg", 0.6) }) });
                if (r.ok) { const d = await r.json(); if (d.serious && d.issue) issues.push("AI flagged: " + d.issue); }
            } catch (e) { /* AI optional */ }
            applyVerdict(issues);
            setStatus(`Sliced in ${dt} ms — ${parsed.layers.length} layers, ${moves} moves.` + (issues.length ? " A check flagged an issue (below)." : " Download & inspect before printing a new model."), issues.length ? "error" : "ok");
        } catch (err) { setStatus("Slice failed: " + (err && err.message ? err.message : err), "error"); }
        finally { els.run.disabled = false; }
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
        baseName = file.name.replace(/\.(stl|scad)$/i, "") || "model";
        els.run.disabled = true;
        try {
            await loadLibs();
            const buf = await file.arrayBuffer();
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
    const initScadPreview = () => {
        if (preview || !els.scadPreviewCanvas) return;
        const THREE = libs.THREE, cv = els.scadPreviewCanvas, size = cv.clientWidth || 300;
        const renderer = new THREE.WebGLRenderer({ canvas: cv, antialias: true, alpha: true });
        renderer.setPixelRatio(window.devicePixelRatio || 1);
        renderer.setSize(size, size, false);
        const scene = new THREE.Scene();
        const camera = new THREE.PerspectiveCamera(45, 1, 0.5, 8000);
        camera.up.set(0, 0, 1); // STL / OpenSCAD is Z-up
        scene.add(new THREE.AmbientLight(0xffffff, 0.75));
        const dir = new THREE.DirectionalLight(0xffffff, 0.85); dir.position.set(1, -1, 1.5); scene.add(dir);
        const controls = new libs.OrbitControls(camera, renderer.domElement);
        controls.enableDamping = true;
        preview = { renderer, scene, camera, controls, mesh: null };
        const loop = () => { if (!preview) return; controls.update(); renderer.render(scene, camera); requestAnimationFrame(loop); };
        loop();
    };
    const setScadPreviewGeometry = (geo) => {
        const THREE = libs.THREE;
        initScadPreview();
        if (!preview) return;
        if (preview.mesh) { preview.scene.remove(preview.mesh); preview.mesh.geometry.dispose(); }
        geo.computeVertexNormals(); geo.center(); geo.computeBoundingSphere();
        const mesh = new THREE.Mesh(geo, new THREE.MeshStandardMaterial({ color: 0x88f387, metalness: 0.1, roughness: 0.65, flatShading: true }));
        preview.scene.add(mesh); preview.mesh = mesh;
        const r = (geo.boundingSphere && geo.boundingSphere.radius) || 50, d = r * 2.6;
        preview.camera.position.set(d, -d, d * 0.7);
        preview.camera.near = Math.max(0.1, r / 50); preview.camera.far = d * 12; preview.camera.updateProjectionMatrix();
        preview.controls.target.set(0, 0, 0); preview.controls.update();
    };
    const scheduleLivePreview = () => {
        if (!els.scadLive || !els.scadLive.checked || !els.scad.value.trim()) return;
        clearTimeout(scadTimer);
        scadTimer = setTimeout(async () => {
            const src = els.scad.value.trim();
            if (!src) return;
            try {
                await loadLibs();
                setStatus("Rendering preview…");
                const stl = await compileScad(src);
                setScadPreviewGeometry(new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength)));
                setStatus("Live preview updated — click ‘Slice this’ to slice for printing.", "ok");
            } catch (err) { setStatus("OpenSCAD: " + (err && err.message ? err.message : err), "error"); }
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
            try {
                await loadLibs();
                setStatus("Compiling OpenSCAD…");
                const stl = await compileScad(src);
                currentGeometry = new libs.STLLoader().parse(stl.buffer.slice(stl.byteOffset, stl.byteOffset + stl.byteLength));
                await sliceCurrent();
            } catch (err) { setStatus("OpenSCAD: " + (err && err.message ? err.message : err), "error"); }
            finally { els.scadRender.disabled = !els.scad.value.trim(); }
        });
    }

    // Raw .gcode upload (moved here from the old GCode tab)
    if (els.gcodeFile && els.gcodeSend) {
        els.gcodeSend.addEventListener("click", async () => {
            const file = els.gcodeFile.files && els.gcodeFile.files[0];
            if (!file) { setStatus("Choose a .gcode file first.", "error"); return; }
            if (!confirm("Upload and print " + file.name + "?")) return;
            uploadGcode(await file.text(), file.name, els.gcodeSend);
        });
    }
}
