# The server part library as glTF: a drive carrier, an M.2 stick, a DIMM,
# a PCIe card, a fan, a PSU and a CPU with its heatsink. Stylised, not
# photoreal: the bar is the right part in the right slot at the right
# proportions (docs/design/spatial-hmi.md §3e).
#
#     blender -b -P hardware.py            # writes hardware/*.glb here
#     blender -b -P hardware.py -- fan     # one part
#
# Every part is modelled at its nominal size in millimetres (below, /1000 to
# metres) and CENTRED on its origin, in the chassis frame: +z out of the
# front, so a carrier's bezel faces +z, a card's bracket and a PSU's handle
# face -z (the rear), and air runs front to back. The Svelte components
# scale a part to the profile's `size`, so these numbers are the look, and
# the profile's are the truth.
#
# Mesh NAMES are the contract the components drive: `Led` lights by state,
# `Accent` tints by state, `Rotor` spins, `Port1`…`Port4` show by port count.
import math
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from build import reset, pbr, box, cylinder, export, bpy  # noqa: E402

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "hardware")
MM = 0.001


def b(name, mat, size, pos=(0, 0, 0)):
    return box(name, mat, tuple(s * MM for s in size), tuple(p * MM for p in pos))


def c(name, mat, radius, height, axis="y", pos=(0, 0, 0), segments=32):
    return cylinder(name, mat, radius * MM, height * MM, axis=axis, pos=tuple(p * MM for p in pos), segments=segments)


def join(name, objs):
    """Merge several meshes into one named mesh (fewer draw calls)."""
    bpy.ops.object.select_all(action="DESELECT")
    for o in objs:
        o.select_set(True)
    bpy.context.view_layer.objects.active = objs[0]
    bpy.ops.object.join()
    o = bpy.context.active_object
    o.name = name
    o.data.name = name
    bpy.ops.object.origin_set(type="ORIGIN_GEOMETRY")
    return o


# Paint: dark metals and PCB greens, lifted enough to read under three lights.
METAL = (0.30, 0.31, 0.33)
METAL_DARK = (0.08, 0.085, 0.09)
PLASTIC = (0.035, 0.035, 0.04)
PCB = (0.02, 0.16, 0.08)
CHIP = (0.02, 0.02, 0.022)
GOLD = (0.83, 0.62, 0.22)
LABEL = (0.85, 0.85, 0.82)
ACCENT = (0.35, 0.36, 0.38)  # tinted by state at run time
LED = (0.2, 0.8, 0.3)


def mats():
    return {
        "metal": pbr("Metal", METAL, metallic=0.8, roughness=0.35),
        "dark": pbr("MetalDark", METAL_DARK, metallic=0.6, roughness=0.5),
        "plastic": pbr("Plastic", PLASTIC, metallic=0.0, roughness=0.6),
        "pcb": pbr("Pcb", PCB, metallic=0.0, roughness=0.45),
        "chip": pbr("Chip", CHIP, metallic=0.1, roughness=0.4),
        "gold": pbr("Gold", GOLD, metallic=1.0, roughness=0.3),
        "label": pbr("Label", LABEL, metallic=0.0, roughness=0.8),
        "accent": pbr("Accent", ACCENT, metallic=0.2, roughness=0.5),
        "led": pbr("Led", LED, metallic=0.0, roughness=0.3),
    }


def build_drive():
    """A 2.5in hot-swap carrier lying flat, 74 x 18.5 x 140, bezel at +z."""
    reset()
    M = mats()
    b("Body", M["dark"], (72, 16, 132), (0, 0, -4))
    b("Bezel", M["plastic"], (74, 18.5, 6), (0, 0, 67))
    # The release latch across the right of the bezel: the tab colour.
    b("Accent", M["accent"], (22, 5, 1.5), (22, -3, 70.5))
    vents = [b(f"Vent{i}", M["dark"], (2.2, 11, 1), (-32 + i * 4.4, 0, 70.2)) for i in range(9)]
    join("Vents", vents)
    b("Led", M["led"], (3, 3, 1.5), (-34, 5.5, 70.5))
    b("Led2", M["led"], (3, 3, 1.5), (-34, -5.5, 70.5))
    export(os.path.join(OUT, "drive.glb"))


def build_m2():
    """An M.2 2280 stick, 22 x 3 x 80, lying flat, connector at +z."""
    reset()
    M = mats()
    b("Pcb", M["pcb"], (22, 0.8, 80), (0, -1.1, 0))
    b("Gold", M["gold"], (20, 0.9, 3), (0, -1.1, 38.5))
    join("Chips", [b("Nand0", M["chip"], (12, 1.2, 14), (0, 0, 16)), b("Nand1", M["chip"], (12, 1.2, 14), (0, 0, -10))])
    b("Controller", M["chip"], (9, 1.2, 9), (0, 0, 3))
    b("Accent", M["label"], (18, 0.3, 20), (0, 0.8, -28))
    export(os.path.join(OUT, "m2.glb"))


def build_dimm():
    """A DDR5 RDIMM standing in its slot, 4 x 31 x 133, length along z."""
    reset()
    M = mats()
    b("Pcb", M["pcb"], (1.3, 29, 133), (0, 0.5, 0))
    b("Gold", M["gold"], (1.4, 3, 125), (0, -14, 0))
    chips = []
    for side in (-1, 1):
        for i in range(10):
            z = -58 + i * 12.8
            if abs(z) < 5:
                continue
            chips.append(b(f"Chip{side}{i}", M["chip"], (1.2, 9, 9), (side * 1.25, 4, z)))
    join("Chips", chips)
    b("Rcd", M["chip"], (1.2, 8, 8), (1.25, -6, 0))
    b("Accent", M["label"], (0.3, 7, 30), (-1.95, 10, 20))
    b("Clip", M["plastic"], (4, 6, 4), (0, -13, 64.5))
    export(os.path.join(OUT, "dimm.glb"))


def build_card():
    """A low-profile PCIe NIC lying flat on its riser, 69 x 12 x 168: PCB in
    the xz plane, parts on top (+y), bracket and cages at the rear (-z)."""
    reset()
    M = mats()
    b("Pcb", M["pcb"], (69, 1.6, 166), (0, -4, 1))
    b("Gold", M["gold"], (2, 1.7, 80), (-34, -4, 10))
    b("Heatsink", M["metal"], (34, 8, 34), (4, 1, 10))
    b("Accent", M["accent"], (34, 0.6, 34), (4, 5.3, 10))
    b("Bracket", M["metal"], (69, 12, 1.2), (0, 0, -83.4))
    for i in range(4):
        # SFP28 cages, left to right looking at the rear; Port3/4 hide on a
        # two-port card.
        b(f"Port{i + 1}", M["dark"], (14, 9, 48), (-24 + i * 16, 0.5, -58))
    b("Led", M["led"], (3, 2, 1.2), (28, 3.5, -84.2))
    export(os.path.join(OUT, "card.glb"))


def build_fan():
    """A 40 x 40 x 56 counter-rotating fan, air along -z."""
    reset()
    M = mats()
    w, d = 40, 56
    join("Frame", [
        b("Top", M["plastic"], (w, 3, d), (0, 18.5, 0)),
        b("Bottom", M["plastic"], (w, 3, d), (0, -18.5, 0)),
        b("Left", M["plastic"], (3, w, d), (-18.5, 0, 0)),
        b("Right", M["plastic"], (3, w, d), (18.5, 0, 0)),
    ])
    c("Hub", M["dark"], 6, d - 4, axis="z", pos=(0, 0, -2))
    blades = []
    for i in range(7):
        a = i * 2 * math.pi / 7
        # Radial along x, tangential along y, thin along the air axis (z),
        # pitched about its own radius, then set round the hub about z
        # (Blender's -y). Scene (x, y, z) is Blender (x, -z, y).
        o = b(f"Blade{i}", M["dark"], (15, 8, 1.5))
        o.rotation_euler = (0.5, -a, 0)
        o.location = (math.cos(a) * 12 * MM, 0, math.sin(a) * 12 * MM)
        bpy.context.view_layer.objects.active = o
        bpy.ops.object.select_all(action="DESELECT")
        o.select_set(True)
        bpy.ops.object.transform_apply(location=True, rotation=True)
        blades.append(o)
    join("Rotor", blades + [c("RotorHub", M["dark"], 7.5, 6, axis="z")])
    b("Accent", M["accent"], (10, 0.6, 20), (0, 20.2, 0))
    export(os.path.join(OUT, "fan.glb"))


def build_psu():
    """A 1U hot-swap PSU module, 52 x 40 x 220, handle and grille at -z."""
    reset()
    M = mats()
    b("Body", M["metal"], (52, 40, 216), (0, 0, 2))
    b("Grille", M["dark"], (44, 34, 1), (0, 0, -106.5))
    b("Handle", M["plastic"], (8, 26, 14), (-18, 0, -114))
    b("Accent", M["accent"], (14, 6, 1.5), (14, 14, -107))
    b("Led", M["led"], (4, 3, 1.5), (20, -14, -107))
    export(os.path.join(OUT, "psu.glb"))


def build_cpu():
    """The socket and a 1U passive heatsink, 80 x 27 x 108, fins along z."""
    reset()
    M = mats()
    b("Socket", M["plastic"], (78, 4, 100), (0, -11.5, 0))
    b("Base", M["metal"], (80, 3, 108), (0, -8, 0))
    fins = [b(f"Fin{i}", M["metal"], (0.6, 20, 108), (-39 + i * 2.6, 3.5, 0)) for i in range(31)]
    join("Fins", fins)
    b("Accent", M["accent"], (80, 0.6, 20), (0, 13.8, -44))
    export(os.path.join(OUT, "cpu.glb"))


PARTS = {"drive": build_drive, "m2": build_m2, "dimm": build_dimm, "card": build_card, "fan": build_fan, "psu": build_psu, "cpu": build_cpu}

if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    which = sys.argv[sys.argv.index("--") + 1 :] if "--" in sys.argv else list(PARTS)
    for w in which:
        PARTS[w]()
