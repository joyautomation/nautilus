# Which CAD faces can be seen at all once the unit is assembled? From points on each face, cast rays outward in
# every direction against the whole assembly (chassis, processor, power supply, a module in each of the four
# slots); a face none of whose rays gets out is buried and is left out of the model. A module face counts if it
# shows in any slot.      blender -b -P vis.py -- <stage dir>
import sys, math, numpy as np
from mathutils import Vector
from mathutils.bvhtree import BVHTree
STAGE = sys.argv[sys.argv.index("--") + 1]
PITCH = 0.02135
names = ["chassis", "processor", "power-supply", "module"]
D = {n: dict(np.load(f"{STAGE}/{n}.npz")) for n in names}
verts, tris = [], []; base = 0
def add(P, T):
    global base
    verts.extend(Vector(p) for p in P); tris.extend(tuple(int(i) + base for i in t) for t in T); base += len(P)
for n in names[:3]: add(D[n]["P"], D[n]["T"])
for slot in range(4): add(D["module"]["P"] + np.array([slot * PITCH, 0, 0]), D["module"]["T"])
tree = BVHTree.FromPolygons(verts, tris)
k = 140; dirs = []
for i in range(k):
    z = 1 - 2 * (i + 0.5) / k; a = i * math.pi * (3 - 5 ** 0.5); dirs.append(Vector((math.cos(a) * (1 - z * z) ** 0.5, math.sin(a) * (1 - z * z) ** 0.5, z)))
for n in names:
    P, T, F = D[n]["P"], D[n]["T"], D[n]["F"]
    fn = np.cross(P[T[:, 1]] - P[T[:, 0]], P[T[:, 2]] - P[T[:, 0]]); area = np.linalg.norm(fn, axis=1); fn /= np.maximum(area[:, None], 1e-18)
    cen = P[T].mean(1); order = np.argsort(-area); seen = set(); tried = {}
    shifts = [Vector((s * PITCH, 0, 0)) for s in range(4)] if n == "module" else [Vector((0, 0, 0))]
    for t in order:
        f = int(F[t])
        if f in seen or tried.get(f, 0) >= 10: continue          # a face's ten largest triangles speak for it
        tried[f] = tried.get(f, 0) + 1
        nrm = Vector(fn[t])
        for sh in shifts:
            o = Vector(cen[t]) + sh + nrm * 2e-5
            if any(dv.dot(nrm) > 0.05 and tree.ray_cast(o, dv)[0] is None for dv in dirs): seen.add(f); break
    np.save(f"{STAGE}/{n}_seen.npy", np.array(sorted(seen)))
    print("SEEN", n, len(seen), "of", int(F.max()) + 1, "faces;", int(np.isin(F, list(seen)).sum()), "of", len(T), "triangles", flush=True)
