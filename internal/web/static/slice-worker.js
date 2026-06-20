// Polyslice slicing worker — runs the CPU-heavy STL→gcode slice off the main
// thread so even a complex model never freezes the UI (orbit/drag stay smooth).
//
// Module workers do NOT use the document's importmap, so three and polyslice are
// imported by their vendored absolute paths. Polyslice reads window.THREE at
// runtime, so we point window at the worker global and expose THREE before the
// (dynamic) polyslice import evaluates.
import * as THREE from "/static/vendor/three/three.module.min.js";
self.window = self;
self.THREE = THREE;
self.window.THREE = THREE;

// Kick off the polyslice import now (after THREE is in place) but don't block
// module evaluation on it — onmessage below awaits this promise, so a slice
// request that arrives before the import settles is handled correctly.
// NB: the worker build is a copy of polyslice with its bare `from "three"`
// import rewritten to the absolute vendored URL, because module workers have no
// importmap to resolve bare specifiers.
const polyslicePromise = import("/static/vendor/polyslice/index.worker.esm.js").then((m) => m.default);

self.onmessage = async (e) => {
    const { id, positions, normals, index, cfg } = e.data || {};
    try {
        const Polyslice = await polyslicePromise;
        const geo = new THREE.BufferGeometry();
        geo.setAttribute("position", new THREE.BufferAttribute(positions, 3));
        if (normals) {
            geo.setAttribute("normal", new THREE.BufferAttribute(normals, 3));
        }
        if (index) {
            geo.setIndex(new THREE.BufferAttribute(index, 1));
        }
        if (!normals) {
            geo.computeVertexNormals();
        }
        const mesh = new THREE.Mesh(geo, new THREE.MeshBasicMaterial());
        const gcode = await Promise.resolve(new Polyslice(cfg).slice(mesh));
        if (typeof gcode !== "string") {
            self.postMessage({ id, error: "slicer returned no gcode" });
            return;
        }
        self.postMessage({ id, gcode });
    } catch (err) {
        self.postMessage({ id, error: String((err && err.message) || err) });
    }
};
