"""Tessellate Opto 22's published groov EPIC STEP files straight to small GLBs for the bench screen.

Every triangle lies on a CAD face, every vertex normal is the CAD surface's own normal at that point (so flat
faces shade flat and fillets shade smooth, with no welding or decimation), and the parts come out in the
screen's frame: metres, x along the chassis from its left edge, y up from the chassis floor, z out from its back.

Three steps (cadquery for the first and last, Blender's Python for the middle one):

    python tess.py <cad dir> <out dir>            tessellate each part into <out dir>/.stage  (mkdir it first)
    blender -b -P vis.py -- <out dir>/.stage      find the CAD faces that can be seen once the unit is assembled
    python tess.py <cad dir> <out dir> --finish   write <out dir>/<part>.glb from those faces

`--list` prints each file's solids and which are kept.
"""
import json, struct, sys, time
import numpy as np
import cadquery as cq
from OCP.BRepMesh import BRepMesh_IncrementalMesh
from OCP.BRep import BRep_Tool
from OCP.BRepAdaptor import BRepAdaptor_Surface
from OCP.BRepLProp import BRepLProp_SLProps
from OCP.TopLoc import TopLoc_Location
from OCP.TopAbs import TopAbs_REVERSED

CAD, OUT = sys.argv[1], sys.argv[2]
STAGE = OUT + "/.stage"  # intermediate arrays, between the two stages
LIST = "--list" in sys.argv
PITCH = 21.35
# name: (STEP file, rows of the rotation taking CAD axes to the screen's, offset mm, linear tolerance mm, angular tolerance rad, keep(solid index, bbox) -> bool)
PARTS = {
    "chassis": ("EPIC chassis/GRV-EPIC-CHS4.stp", ((1, 0, 0), (0, 0, -1), (0, 1, 0)), (91.52, 57.5, 2.8), 1.0, 0.6),
    "processor": ("EPIC controller/GRV-EPIC-PR1.stp", ((1, 0, 0), (0, 1, 0), (0, 0, 1)), (76.7, 70.0, 72.2), 0.5, 0.6),
    "power-supply": ("EPIC power supplies/GRV-EPIC-PSAC.stp", ((1, 0, 0), (0, 1, 0), (0, 0, 1)), (76.7 - 137.2, 70.0 - 13.7, 72.2 + 111.4), 0.5, 0.6),
    # in y the module sits where its hold-down screw is over the hole in the chassis's slot tab (y = -5.3)
    "module": ("EPIC IO/GRV-EPIC-GenericModule_coverLow.stp", ((0, 0, -1), (-1, 0, 0), (0, 1, 0)), (99.8 - 104.675, 54.35, -102.06 + 4.0), 0.5, 0.6),
}
# The module's status light is the light pipe that shows along the wave of its cover. Its faces go to a model
# of their own (module-light.glb), so the screen lights the CAD's window and not a shape drawn over it:
# the faces that lie wholly inside this box (mm, screen frame, slot 0) and are larger than 8 mm².
LIGHT = ((92.3, 107.5), (92.5, 107.5), (137.9, 141.6))
ONLY = [a for a in sys.argv[3:] if not a.startswith("--")]

def keep(name, i, bb, nfaces):
    """Solids the screen can never show are left out, rather than thinning the ones it does."""
    size = sorted((bb.xlen, bb.ylen, bb.zlen))
    if size[2] < 12: return False                      # fasteners, pins, single terminal points
    if name == "processor" and size[2] < 40: return False   # the power button, behind the closed screen
    if name == "module" and bb.ylen < 30 and bb.xlen > 80 and bb.zlen < 16: return False   # the terminal strip under the closed cover
    return True

def tessellate(shape, lin, ang, skip):
    BRepMesh_IncrementalMesh(shape.wrapped, lin, False, ang, True)
    P, N, T, F = [], [], [], []; base = 0
    for fid, face in enumerate(shape.Faces()):
        if face.hashCode() in skip: continue
        loc = TopLoc_Location(); tri = BRep_Tool.Triangulation_s(face.wrapped, loc)
        if tri is None: continue
        trsf = loc.Transformation(); rev = face.wrapped.Orientation() == TopAbs_REVERSED
        surf = BRepAdaptor_Surface(face.wrapped); n = tri.NbNodes()
        pts = np.empty((n, 3)); nrm = np.zeros((n, 3))
        for k in range(1, n + 1):
            p = tri.Node(k).Transformed(trsf); pts[k - 1] = (p.X(), p.Y(), p.Z())
            if tri.HasUVNodes():
                uv = tri.UVNode(k); props = BRepLProp_SLProps(surf, uv.X(), uv.Y(), 1, 1e-6)
                if props.IsNormalDefined():
                    d = props.Normal(); nrm[k - 1] = (d.X(), d.Y(), d.Z())
        if rev: nrm = -nrm
        idx = np.empty((tri.NbTriangles(), 3), dtype=np.int64)
        for k in range(1, tri.NbTriangles() + 1):
            a, b, c = tri.Triangle(k).Get(); idx[k - 1] = (a - 1, c - 1, b - 1) if rev else (a - 1, b - 1, c - 1)
        # a node where the surface has no normal (a cone's tip, a pole) takes its triangles' own
        fn = np.cross(pts[idx[:, 1]] - pts[idx[:, 0]], pts[idx[:, 2]] - pts[idx[:, 0]])
        bad = np.where(np.linalg.norm(nrm, axis=1) < 0.5)[0]
        if len(bad):
            acc = np.zeros((n, 3)); np.add.at(acc, idx[:, 0], fn); np.add.at(acc, idx[:, 1], fn); np.add.at(acc, idx[:, 2], fn)
            nrm[bad] = acc[bad]
        # slivers of zero area carry no shading and only cost bytes
        idx = idx[np.linalg.norm(fn, axis=1) > 1e-9]
        P.append(pts); N.append(nrm); T.append(idx + base); F.append(np.full(len(idx), fid)); base += n
    return np.vstack(P), np.vstack(N), np.vstack(T), np.concatenate(F)

def write_glb(path, P, N, T):
    """Positions as float32 metres, normals as normalised signed bytes (KHR_mesh_quantization), indices uint32."""
    P = P.astype("<f4"); Q = np.zeros((len(N), 4), dtype=np.int8); Q[:, :3] = np.rint(N * 127); T = T.astype("<u4")
    blobs = [T.tobytes(), P.tobytes(), Q.tobytes()]; off = 0; views = []
    for b, target, stride in zip(blobs, (34963, 34962, 34962), (None, 12, 4)):
        v = {"buffer": 0, "byteOffset": off, "byteLength": len(b), "target": target}
        if stride: v["byteStride"] = stride
        views.append(v); off += len(b)
    doc = {"asset": {"version": "2.0", "generator": "tess.py"}, "extensionsUsed": ["KHR_mesh_quantization"], "extensionsRequired": ["KHR_mesh_quantization"],
           "scene": 0, "scenes": [{"nodes": [0]}], "nodes": [{"mesh": 0, "name": path.split("/")[-1][:-4]}],
           "meshes": [{"primitives": [{"attributes": {"POSITION": 1, "NORMAL": 2}, "indices": 0}]}],
           "buffers": [{"byteLength": off}], "bufferViews": views,
           "accessors": [{"bufferView": 0, "componentType": 5125, "count": int(T.size), "type": "SCALAR"},
                         {"bufferView": 1, "componentType": 5126, "count": len(P), "type": "VEC3", "min": P.min(0).tolist(), "max": P.max(0).tolist()},
                         {"bufferView": 2, "componentType": 5120, "normalized": True, "count": len(Q), "type": "VEC3"}]}
    js = json.dumps(doc, separators=(",", ":")).encode(); js += b" " * (-len(js) % 4); bin_ = b"".join(blobs)
    with open(path, "wb") as f:
        f.write(struct.pack("<III", 0x46546C67, 2, 12 + 8 + len(js) + 8 + len(bin_)))
        f.write(struct.pack("<II", len(js), 0x4E4F534A)); f.write(js); f.write(struct.pack("<II", len(bin_), 0x004E4942)); f.write(bin_)

def finish(name, P, N, T, nfaces, allfaces, lin, ang):
    """Stage 2 for one model: merge identical vertices, check facing, write."""
    import os
    key = np.concatenate([np.rint(P * 1e6).astype(np.int64), np.rint(N * 127).astype(np.int64)], axis=1)
    used = np.unique(T); _, first, inv = np.unique(key[used], axis=0, return_index=True, return_inverse=True)
    remap = np.zeros(len(P), dtype=np.int64); remap[used] = inv.ravel(); P = P[used][first]; N = N[used][first]; T = remap[T]
    T = T[(T[:, 0] != T[:, 1]) & (T[:, 1] != T[:, 2]) & (T[:, 0] != T[:, 2])]
    fn = np.cross(P[T[:, 1]] - P[T[:, 0]], P[T[:, 2]] - P[T[:, 0]]); fn /= np.maximum(np.linalg.norm(fn, axis=1, keepdims=True), 1e-18)
    against = np.einsum("ij,ij->i", fn, N[T].mean(1)) < -0.2
    # a triangle wound against its surface's normal would be culled as a hole: turn it to agree
    T[against] = T[against][:, ::-1]
    write_glb(f"{OUT}/{name}.glb", P, N, T); size = os.path.getsize(f"{OUT}/{name}.glb")
    print(f"PART {name}: faces {nfaces} of {allfaces}, triangles {len(T)}, vertices {len(P)}, {size} bytes, tolerance {lin} mm / {ang} rad, box mm {np.round(P.min(0) * 1000, 1).tolist()} {np.round(P.max(0) * 1000, 1).tolist()}, re-wound {int(against.sum())}")
    return size

if "--finish" in sys.argv:
    # stage 2: leave out the faces vis.py found buried
    total = 0
    for name, (src, rot, off, lin, ang) in PARTS.items():
        d = np.load(f"{STAGE}/{name}.npz"); seen = np.load(f"{STAGE}/{name}_seen.npy")
        # the module's light pipe is written apart from its body
        for out, faces in ((name, np.setdiff1d(seen, d["L"])), (name + "-light", d["L"])):
            if len(faces): total += finish(out, d["P"], d["N"], d["T"][np.isin(d["F"], faces)], len(faces), int(d["F"].max()) + 1, lin, ang)
    print("TOTAL", total, "bytes"); sys.exit()


for name, (src, rot, off, lin, ang) in PARTS.items():
    if ONLY and name not in ONLY: continue
    t = time.time(); model = cq.importers.importStep(f"{CAD}/{src}"); solids = model.solids().vals()
    # everything in the file is drawn (some bodies are shells, not solids) except the faces of the solids left out
    skip = set(); kept = 0
    for i, s in enumerate(solids):
        bb = s.BoundingBox(); k = keep(name, i, bb, len(s.Faces())); kept += k
        if LIST: print(f"  {name} solid {i}: faces {len(s.Faces())} box x {bb.xmin:.1f}..{bb.xmax:.1f} y {bb.ymin:.1f}..{bb.ymax:.1f} z {bb.zmin:.1f}..{bb.zmax:.1f} {'keep' if k else 'DROP'}")
        if not k: skip.update(f.hashCode() for f in s.Faces())
    whole = cq.Compound.makeCompound(model.vals())
    if name == "processor":
        # loose construction geometry in the file: a sketch plane out past the housing's side, a needle out of its front
        for f in whole.Faces():
            fb = f.BoundingBox()
            if fb.xmax > 12.5 or fb.zmax > 64.2: skip.add(f.hashCode())
    if LIST: print(f"  {name}: {len(whole.Faces())} faces in all, {len(skip)} left out"); continue
    P, N, T, F = tessellate(whole, lin, ang, skip)
    R = np.array(rot, dtype=float); P = (P @ R.T + np.array(off)) / 1000.0; N = N @ R.T
    N /= np.maximum(np.linalg.norm(N, axis=1, keepdims=True), 1e-12)
    L = []
    if name == "module":
        area = np.linalg.norm(np.cross(P[T[:, 1]] - P[T[:, 0]], P[T[:, 2]] - P[T[:, 0]]), axis=1) * 5e5   # mm²
        for f in np.unique(F):
            pts = P[T[F == f]].reshape(-1, 3) * 1000
            if all(lo <= pts[:, k].min() and pts[:, k].max() <= hi for k, (lo, hi) in enumerate(LIGHT)) and area[F == f].sum() > 8:
                L.append(int(f)); print(f"  light face {f}: {area[F == f].sum():.1f} mm², box {np.round(pts.min(0), 1).tolist()} {np.round(pts.max(0), 1).tolist()}")
    np.savez(f"{STAGE}/{name}.npz", P=P, N=N, T=T, F=F, L=np.array(L, dtype=np.int64))
    print(f"STAGED {name}: faces {int(F.max()) + 1} tris {len(T)} tol {lin} mm / {ang} rad ({time.time() - t:.0f}s)", flush=True)
