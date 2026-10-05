# The built-in kinds as glTF: a tank, a pump and a valve, generated with
# Blender so the models are derived, not hand-made, and can be regenerated
# when a mesh name or a dimension changes.
#
#     blender -b -P build.py            # writes tank.glb, pump.glb, valve.glb here
#
# Metres, +y up in the export (Blender is z-up; the exporter converts).
# Mesh NAMES are the contract kinds.json's drives bind to: Coupling spins,
# Motor/Volute tint, Fluid scales on y from its base, Handle turns about z.
# Each model's origin is where a node's `pos` puts it: the tank's base, the
# pump's skid, the valve's body centre — the same origins the Svelte
# built-ins use, so swapping one for the other moves nothing.
import bpy
import math
import os
import sys

OUT = os.path.dirname(os.path.abspath(__file__))


def reset():
    bpy.ops.wm.read_factory_settings(use_empty=True)


def pbr(name, color, metallic=0.0, roughness=0.5, alpha=1.0, emission=None):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    bsdf = m.node_tree.nodes["Principled BSDF"]
    bsdf.inputs["Base Color"].default_value = (*color, 1.0)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    bsdf.inputs["Alpha"].default_value = alpha
    if alpha < 1.0:
        m.blend_method = "BLEND"
        m.show_transparent_back = False
    return m


def named(obj, name, mat):
    obj.name = name
    obj.data.name = name
    obj.data.materials.append(mat)
    # Every node carries its own translation and local vertices, so a drive
    # that spins or turns a mesh does it about the mesh's own centre.
    bpy.ops.object.origin_set(type="ORIGIN_GEOMETRY")
    return obj


# Blender's frame is z-up; scene metres, y-up, are what the exporter writes.
# Every dimension below is given in the SCENE frame (x, y-up, z) and mapped
# to Blender's (x, -z, y) by these helpers, so the numbers match the Svelte
# built-ins one for one.
def at(x, y, z):
    return (x, -z, y)


def cylinder(name, mat, radius, height, axis="y", pos=(0, 0, 0), segments=48):
    # A cylinder along the scene's `axis` through `pos` (its centre).
    bpy.ops.mesh.primitive_cylinder_add(vertices=segments, radius=radius, depth=height, location=at(*pos))
    o = bpy.context.active_object
    # Blender's cylinder stands along Blender z, which IS the scene's y.
    if axis == "x":
        o.rotation_euler = (0, math.pi / 2, 0)
    elif axis == "z":  # scene z is Blender -y
        o.rotation_euler = (math.pi / 2, 0, 0)
    bpy.ops.object.transform_apply(rotation=True)
    return named(o, name, mat)


def box(name, mat, size, pos=(0, 0, 0)):
    bpy.ops.mesh.primitive_cube_add(size=1, location=at(*pos))
    o = bpy.context.active_object
    o.scale = (size[0], size[2], size[1])
    bpy.ops.object.transform_apply(scale=True)
    return named(o, name, mat)


def sphere(name, mat, radius, pos=(0, 0, 0)):
    bpy.ops.mesh.primitive_uv_sphere_add(radius=radius, segments=32, ring_count=16, location=at(*pos))
    o = bpy.context.active_object
    bpy.ops.object.shade_smooth()
    return named(o, name, mat)


def smooth(o):
    bpy.context.view_layer.objects.active = o
    o.select_set(True)
    bpy.ops.object.shade_smooth()
    o.select_set(False)


def export(path):
    bpy.ops.object.select_all(action="SELECT")
    bpy.ops.export_scene.gltf(
        filepath=path,
        export_format="GLB",
        export_yup=True,
        export_apply=True,
        export_animations=False,
        export_skins=False,
        export_cameras=False,
        export_lights=False,
    )
    print("wrote", path)


# ── colours: the palette's greys (palette.ts). Paint, not UI. ───────────
STEEL = (0.263, 0.275, 0.290)
STEEL_DARK = (0.094, 0.100, 0.107)
SHELL = (0.470, 0.490, 0.520)
FLUID = (0.064, 0.212, 0.455)
HANDLE = (0.584, 0.356, 0.019)


def build_pump():
    reset()
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    shell = pbr("Shell", SHELL, metallic=0.9, roughness=0.25)
    # Skid: a channel-iron base plate with two feet.
    box("Skid", dark, (0.36, 0.02, 0.14), (0, 0.01, 0))
    box("Foot1", dark, (0.04, 0.03, 0.14), (-0.14, 0.035, 0))
    box("Foot2", dark, (0.04, 0.03, 0.14), (0.10, 0.035, 0))
    # Motor: a finned cylinder along x with a terminal box on top.
    m = cylinder("Motor", steel, 0.055, 0.16, axis="x", pos=(-0.08, 0.08, 0))
    smooth(m)
    box("TerminalBox", dark, (0.05, 0.03, 0.04), (-0.08, 0.145, 0))
    for i in range(6):
        cylinder(f"Fin{i}", steel, 0.058, 0.004, axis="x", pos=(-0.15 + i * 0.024, 0.08, 0), segments=32)
    # Coupling: the visible part that turns, a hex hub with a bar across it.
    cylinder("Coupling", dark, 0.03, 0.04, axis="x", pos=(0.02, 0.08, 0), segments=6)
    bar = box("CouplingBar", shell, (0.042, 0.075, 0.008), (0.02, 0.08, 0))
    # The bar must turn WITH the hub: parent it, so a spin drive on Coupling
    # carries the bar.
    bar.parent = bpy.data.objects["Coupling"]
    bar.matrix_parent_inverse = bpy.data.objects["Coupling"].matrix_world.inverted()
    # Volute: the pump casing, with a discharge flange up and suction ahead.
    v = cylinder("Volute", steel, 0.065, 0.06, axis="x", pos=(0.10, 0.08, 0))
    smooth(v)
    cylinder("Suction", steel, 0.03, 0.05, axis="x", pos=(0.155, 0.08, 0), segments=32)
    cylinder("SuctionFlange", dark, 0.04, 0.008, axis="x", pos=(0.176, 0.08, 0), segments=32)
    cylinder("Discharge", steel, 0.022, 0.05, axis="y", pos=(0.10, 0.155, 0), segments=32)
    cylinder("DischargeFlange", dark, 0.032, 0.008, axis="y", pos=(0.10, 0.178, 0), segments=32)
    export(os.path.join(OUT, "pump.glb"))


def build_tank():
    reset()
    glass = pbr("Glass", SHELL, metallic=0.0, roughness=0.08, alpha=0.22)
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    fluid = pbr("Fluid", FLUID, metallic=0.0, roughness=0.15, alpha=0.85)
    radius, height = 0.2, 0.4
    s = cylinder("Shell", glass, radius, height, axis="y", pos=(0, height / 2, 0))
    smooth(s)
    cylinder("Base", dark, radius, 0.005, axis="y", pos=(0, 0.0025, 0))
    # An open top: a rolled rim, not a lid.
    bpy.ops.mesh.primitive_torus_add(major_radius=radius, minor_radius=0.006, major_segments=64, minor_segments=12, location=at(0, height, 0), rotation=(0, 0, 0))
    rim = bpy.context.active_object
    bpy.ops.object.shade_smooth()
    named(rim, "Rim", steel)
    # Three legs so it reads as a vessel, not a bucket.
    for i in range(3):
        a = i * 2 * math.pi / 3
        cylinder(f"Leg{i}", dark, 0.008, 0.02, axis="y", pos=(math.cos(a) * (radius - 0.02), -0.01, math.sin(a) * (radius - 0.02)), segments=12)
    # Fluid: a unit-height column whose ORIGIN IS ITS BASE, so a scale drive
    # on y from Level (0.01 per %) fills it to the shell.
    f = cylinder("Fluid", fluid, radius - 0.01, 1.0, axis="y", pos=(0, 0.505, 0))
    smooth(f)
    bpy.context.scene.cursor.location = at(0, 0.005, 0)
    bpy.context.view_layer.objects.active = f
    f.select_set(True)
    bpy.ops.object.origin_set(type="ORIGIN_CURSOR")
    f.select_set(False)
    # Start at 39 % of the shell so the file looks right in a viewer that
    # applies no drives; the drive overrides the scale on the first frame.
    f.scale = (1, 1, 0.39)
    export(os.path.join(OUT, "tank.glb"))


def build_valve():
    reset()
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    handle = pbr("Handle", HANDLE, metallic=0.2, roughness=0.5)
    b = sphere("Body", steel, 0.045)
    cylinder("Run", steel, 0.022, 0.16, axis="y", pos=(0, 0, 0), segments=24)
    cylinder("FlangeTop", dark, 0.03, 0.008, axis="y", pos=(0, 0.076, 0), segments=24)
    cylinder("FlangeBottom", dark, 0.03, 0.008, axis="y", pos=(0, -0.076, 0), segments=24)
    cylinder("Stem", dark, 0.006, 0.06, axis="z", pos=(0, 0, 0.07), segments=12)
    # Handle: origin on the stem axis at z = 0.1, so a turn drive about z
    # swings it. Closed (Pos 0) lies across the run: +90°; open along it: 0°.
    h = box("Handle", handle, (0.016, 0.11, 0.01), (0, 0.045, 0.1))
    bpy.context.scene.cursor.location = at(0, 0, 0.1)
    bpy.context.view_layer.objects.active = h
    h.select_set(True)
    bpy.ops.object.origin_set(type="ORIGIN_CURSOR")
    h.select_set(False)
    h.rotation_euler = (0, 0, 0)
    export(os.path.join(OUT, "valve.glb"))


if __name__ == "__main__":
    which = sys.argv[sys.argv.index("--") + 1 :] if "--" in sys.argv else ["pump", "tank", "valve"]
    for w in which:
        {"pump": build_pump, "tank": build_tank, "valve": build_valve}[w]()
