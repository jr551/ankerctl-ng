// OpenSCAD compile worker — runs the ~7 MB WASM + CGAL off the main thread so
// the UI (and the 3D preview's orbit controls) stay responsive during a compile.
import OpenSCAD from "/static/vendor/openscad/openscad.js";

self.onmessage = async (e) => {
    const { id, scad } = e.data || {};
    try {
        const errs = [];
        const inst = await OpenSCAD({ noInitialRun: true, printErr: (l) => errs.push(l) });
        inst.FS.writeFile("/in.scad", scad);
        const code = inst.callMain(["/in.scad", "-o", "/out.stl"]);
        if (code !== 0) {
            self.postMessage({ id, error: (errs.join(" ").trim().slice(0, 400)) || ("OpenSCAD exited " + code) });
            return;
        }
        const stl = inst.FS.readFile("/out.stl"); // Uint8Array over the wasm heap
        const out = new Uint8Array(stl);           // copy into a fresh buffer we can transfer
        self.postMessage({ id, stl: out }, [out.buffer]);
    } catch (err) {
        self.postMessage({ id, error: String((err && err.message) || err) });
    }
};
