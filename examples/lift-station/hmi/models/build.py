# The lift station's kinds as glTF: the wet well, a submersible pump, a float
# switch, a swing check valve, a flow meter and the force-main connector —
# generated with Blender so the models are derived, not hand-made, and can
# be regenerated when a mesh name or a dimension changes. The same recipe as
# hmi-3d/models/build.py; this file only adds the kinds a lift station has
# and the kit does not.
#
#     blender -b -P build.py            # writes ../static/models/*.glb
#     blender -b -P build.py -- pump    # one of: well pump float check meter main
#
# Metres, +y up in the export (Blender is z-up; the exporter converts).
# Mesh NAMES are the contract lift-station.scene.json's drives bind to:
#   wet-well          Fluid scales on y from its base with LIT101_Level
#   submersible-pump  Motor / Volute tint and Lamp lights while running;
#                     Impeller spins at the VFD's speed
#   float-switch      FloatDown shows while clear, FloatUp while tripped
#   flow-meter        Display lights while there is flow
# Each model's origin is where a node's `pos` puts it: the well's floor,
# the pump's feet, the float's hanger, a valve's or meter's body centre on
# its run, the connector's flange face.
import bpy
import bmesh
import math
import os
import sys
from mathutils import Vector

OUT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "static", "models"))


def reset():
    bpy.ops.wm.read_factory_settings(use_empty=True)


def pbr(name, color, metallic=0.0, roughness=0.5, alpha=1.0):
    m = bpy.data.materials.new(name)
    m.use_nodes = True
    bsdf = m.node_tree.nodes["Principled BSDF"]
    bsdf.inputs["Base Color"].default_value = (*color, 1.0)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    bsdf.inputs["Alpha"].default_value = alpha
    if alpha < 1.0:
        # Blender 4.2+ names it surface_render_method; older ones blend_method.
        if hasattr(m, "surface_render_method"):
            m.surface_render_method = "BLENDED"
        else:
            m.blend_method = "BLEND"
    return m


def select_only(o):
    bpy.ops.object.select_all(action="DESELECT")
    bpy.context.view_layer.objects.active = o
    o.select_set(True)


def named(obj, name, mat):
    obj.name = name
    obj.data.name = name
    obj.data.materials.append(mat)
    # Every node carries its own translation and local vertices, so a drive
    # that spins or turns a mesh does it about the mesh's own centre.
    select_only(obj)
    bpy.ops.object.origin_set(type="ORIGIN_GEOMETRY")
    return obj


# Blender's frame is z-up; scene metres, y-up, are what the exporter writes.
# Every dimension below is given in the SCENE frame (x, y-up, z) and mapped
# to Blender's (x, -z, y) by these helpers.
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


def cone(name, mat, r1, r2, height, axis="y", pos=(0, 0, 0), segments=32):
    # A cone (or frustum) along `axis`, r1 at its base, r2 at its top / tip.
    bpy.ops.mesh.primitive_cone_add(vertices=segments, radius1=r1, radius2=r2, depth=height, location=at(*pos))
    o = bpy.context.active_object
    if axis == "x":
        o.rotation_euler = (0, math.pi / 2, 0)
    elif axis == "z":
        o.rotation_euler = (math.pi / 2, 0, 0)
    bpy.ops.object.transform_apply(rotation=True)
    return named(o, name, mat)


def box(name, mat, size, pos=(0, 0, 0), yaw=0.0):
    # A box of scene size (w, h, d) centred on pos, turned `yaw` about y.
    bpy.ops.mesh.primitive_cube_add(size=1, location=at(*pos))
    o = bpy.context.active_object
    o.scale = (size[0], size[2], size[1])
    bpy.ops.object.transform_apply(scale=True)
    if yaw:
        o.rotation_euler = (0, 0, yaw)
        bpy.ops.object.transform_apply(rotation=True)
    return named(o, name, mat)


def sphere(name, mat, radius, pos=(0, 0, 0)):
    bpy.ops.mesh.primitive_uv_sphere_add(radius=radius, segments=32, ring_count=16, location=at(*pos))
    o = bpy.context.active_object
    bpy.ops.object.shade_smooth()
    return named(o, name, mat)


def strut(name, mat, radius, p0, p1, segments=12):
    # A thin cylinder from scene point p0 to p1 (a cable, a tether).
    a, b = Vector(at(*p0)), Vector(at(*p1))
    d = b - a
    bpy.ops.mesh.primitive_cylinder_add(vertices=segments, radius=radius, depth=d.length, location=(a + b) / 2)
    o = bpy.context.active_object
    o.rotation_euler = Vector((0, 0, 1)).rotation_difference(d.normalized()).to_euler()
    bpy.ops.object.transform_apply(rotation=True)
    return named(o, name, mat)


def arc_ring(name, mat, r_in, r_out, y0, y1, a0, a1, segments=72):
    # A ring sector: inner radius r_in, outer r_out, from y0 up to y1, over
    # the scene azimuths a0..a1 (0 = +x, pi/2 = +z). A wall with a quarter
    # cut away, a collar with the same opening.
    bm = bmesh.new()
    angles = [a0 + (a1 - a0) * i / segments for i in range(segments + 1)]
    ring = lambda r, y: [bm.verts.new(at(r * math.cos(a), y, r * math.sin(a))) for a in angles]
    io, it, oo, ot = ring(r_in, y0), ring(r_in, y1), ring(r_out, y0), ring(r_out, y1)
    for ra, rb in ((oo, ot), (it, io), (ot, it), (io, oo)):
        for i in range(segments):
            bm.faces.new((ra[i], ra[i + 1], rb[i + 1], rb[i]))
    bm.faces.new((io[0], it[0], ot[0], oo[0]))
    bm.faces.new((oo[-1], ot[-1], it[-1], io[-1]))
    bmesh.ops.recalc_face_normals(bm, faces=bm.faces)
    me = bpy.data.meshes.new(name)
    bm.to_mesh(me)
    bm.free()
    o = bpy.data.objects.new(name, me)
    bpy.context.collection.objects.link(o)
    return named(o, name, mat)


def smooth(o):
    select_only(o)
    bpy.ops.object.shade_smooth()


def parent(child, to):
    # The child must move WITH its parent (a vane with the hub it is on, a
    # tether with its float), so a drive on the parent carries it.
    child.parent = to
    child.matrix_parent_inverse = to.matrix_world.inverted()


def origin_to(o, pos):
    # Put an object's origin at a scene point: a fluid column scales from
    # its base, a hanging part swings from its hanger.
    bpy.context.scene.cursor.location = at(*pos)
    select_only(o)
    bpy.ops.object.origin_set(type="ORIGIN_CURSOR")


def export(name):
    path = os.path.join(OUT, name)
    os.makedirs(OUT, exist_ok=True)
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


# ── colours: the kit palette's greys (hmi-3d palette.ts), plus concrete. ──
STEEL = (0.263, 0.275, 0.290)
STEEL_DARK = (0.094, 0.100, 0.107)
SHELL = (0.470, 0.490, 0.520)
FLUID = (0.064, 0.212, 0.455)
CONCRETE = (0.46, 0.45, 0.42)
CONCRETE_DARK = (0.30, 0.30, 0.28)
FLOAT = (0.80, 0.80, 0.76)
CABLE = (0.02, 0.02, 0.02)


def build_well():
    # The wet well: a concrete cylinder 2.5 m across and 3 m deep, three
    # quarters of its wall and collar drawn so the open quarter (between +x
    # and +z, the camera's side) shows the pumps and the water. Origin at
    # the floor's centre; the rim is at y = 3.
    reset()
    concrete = pbr("Concrete", CONCRETE, metallic=0.0, roughness=0.95)
    concrete_dark = pbr("ConcreteDark", CONCRETE_DARK, metallic=0.0, roughness=0.95)
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    fluid = pbr("Fluid", FLUID, metallic=0.0, roughness=0.3, alpha=0.35)
    R, WALL, DEPTH = 1.25, 0.2, 3.0
    cylinder("Floor", concrete_dark, R + WALL, 0.15, axis="y", pos=(0, -0.075, 0), segments=72)
    arc_ring("Wall", concrete, R, R + WALL, 0, DEPTH, math.pi / 2, 2 * math.pi)
    arc_ring("Collar", concrete, R, R + 0.6, DEPTH - 0.25, DEPTH, math.pi / 2, 2 * math.pi)
    # The gravity sewer comes in high on the back wall.
    cylinder("Inlet", steel, 0.15, 0.7, axis="z", pos=(0, 2.3, -1.35), segments=32)
    # An access ladder down the -x wall.
    box("RailA", steel, (0.04, 2.85, 0.04), (-1.17, 1.525, 0.18))
    box("RailB", steel, (0.04, 2.85, 0.04), (-1.17, 1.525, -0.18))
    for i in range(9):
        cylinder(f"Rung{i}", steel, 0.015, 0.36, axis="z", pos=(-1.17, 0.3 + 0.3 * i, 0), segments=12)
    # Fluid: a unit-height column whose ORIGIN IS ITS BASE, so a scale drive
    # on y from LIT101_Level (0.0296 m per %) fills it to just under the rim.
    f = cylinder("Fluid", fluid, R - 0.02, 1.0, axis="y", pos=(0, 0.52, 0), segments=72)
    smooth(f)
    origin_to(f, (0, 0.02, 0))
    # Start at 60 % so the file looks right in a viewer that applies no
    # drives; the drive overrides the scale on the first frame.
    f.scale = (1, 1, 1.78)
    export("wet-well.glb")


def build_pump():
    # A submersible sewage pump stood on the well floor: volute on three
    # feet, a long motor can, a cap with the running lamp and the cable, and
    # the discharge elbow turning up into a short riser (its flange, where
    # the scene's pipe starts, is at (0, 0.89, -0.5)). Origin at the feet.
    reset()
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    cable = pbr("Cable", CABLE, metallic=0.0, roughness=0.8)
    lamp = pbr("Lamp", SHELL, metallic=0.1, roughness=0.3)
    for i in range(3):
        a = math.pi / 2 + i * 2 * math.pi / 3
        cylinder(f"Foot{i}", dark, 0.03, 0.14, axis="y", pos=(math.cos(a) * 0.22, 0.07, math.sin(a) * 0.22), segments=12)
    # Impeller: a hub with three vanes under the volute, the part that turns.
    hub = cylinder("Impeller", dark, 0.04, 0.03, axis="y", pos=(0, 0.075, 0), segments=12)
    for i in range(3):
        parent(box(f"Vane{i}", dark, (0.22, 0.025, 0.05), (0, 0.075, 0), yaw=i * math.pi / 3), hub)
    v = cylinder("Volute", steel, 0.3, 0.3, axis="y", pos=(0, 0.29, 0))
    smooth(v)
    # The discharge: out of the volute low down, a base elbow turning it
    # up, and a short flanged stub. The scene's own pipe takes over at the
    # flange top, local (0, 0.43, -0.5), and runs straight up past the
    # motor — one continuous pipe from the pump's base in every look,
    # rather than a riser in the model that a drawn water surface hides.
    cylinder("Discharge", steel, 0.075, 0.32, axis="z", pos=(0, 0.2, -0.36), segments=24)
    sphere("Elbow", steel, 0.08, pos=(0, 0.2, -0.5))
    cylinder("Stub", steel, 0.075, 0.2, axis="y", pos=(0, 0.3, -0.5), segments=24)
    cylinder("Flange", dark, 0.11, 0.03, axis="y", pos=(0, 0.415, -0.5), segments=24)
    # A strap from the motor to where that pipe passes it.
    box("Strap", dark, (0.06, 0.05, 0.34), (0, 1.1, -0.33))
    m = cylinder("Motor", steel, 0.21, 0.75, axis="y", pos=(0, 0.815, 0))
    smooth(m)
    cone("Cap", dark, 0.21, 0.09, 0.12, axis="y", pos=(0, 1.25, 0))
    sphere("Lamp", lamp, 0.035, pos=(-0.06, 1.33, 0))
    cylinder("Cable", cable, 0.012, 0.6, axis="y", pos=(0.06, 1.6, 0), segments=8)
    export("submersible-pump.glb")


def build_float():
    # A float switch hung from its cable clamp (the origin): the ball hangs
    # straight down while clear (FloatDown) and swings up on a rising level
    # (FloatUp); the scene shows one or the other. Each tether moves with
    # its float.
    reset()
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    cable = pbr("Cable", CABLE, metallic=0.0, roughness=0.8)
    shell = pbr("Float", FLOAT, metallic=0.0, roughness=0.6)
    box("Clamp", dark, (0.05, 0.03, 0.05), (0, -0.015, 0))
    down = sphere("FloatDown", shell, 0.09, pos=(0, -0.34, 0))
    parent(cylinder("TetherDown", cable, 0.01, 0.26, axis="y", pos=(0, -0.13, 0), segments=8), down)
    up = sphere("FloatUp", shell, 0.09, pos=(0.29, -0.16, 0))
    parent(strut("TetherUp", cable, 0.01, (0, 0, 0), (0.29, -0.16, 0)), up)
    export("float-switch.glb")


def build_check():
    # A swing check valve on a horizontal run along x: body, two flanges, a
    # bonnet, and the outside lever with its counterweight. Nothing moves.
    reset()
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    b = cylinder("Body", steel, 0.11, 0.34, axis="x", pos=(0, 0, 0), segments=32)
    smooth(b)
    cylinder("FlangeIn", dark, 0.16, 0.03, axis="x", pos=(-0.185, 0, 0), segments=32)
    cylinder("FlangeOut", dark, 0.16, 0.03, axis="x", pos=(0.185, 0, 0), segments=32)
    box("Bonnet", steel, (0.2, 0.14, 0.2), (0, 0.15, 0))
    box("Lever", dark, (0.3, 0.025, 0.025), (0.12, 0.24, 0))
    box("Weight", dark, (0.08, 0.08, 0.08), (0.26, 0.24, 0))
    export("check-valve.glb")


def build_meter():
    # An electromagnetic flow meter: a spool with flanges, the transmitter
    # head on a stem, and its display facing +z, lit while there is flow.
    reset()
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    shell = pbr("Shell", SHELL, metallic=0.1, roughness=0.3)
    b = cylinder("Body", steel, 0.1, 0.32, axis="x", pos=(0, 0, 0), segments=32)
    smooth(b)
    cylinder("FlangeIn", dark, 0.15, 0.03, axis="x", pos=(-0.175, 0, 0), segments=32)
    cylinder("FlangeOut", dark, 0.15, 0.03, axis="x", pos=(0.175, 0, 0), segments=32)
    cylinder("Stem", dark, 0.025, 0.2, axis="y", pos=(0, 0.18, 0), segments=12)
    box("Head", dark, (0.16, 0.14, 0.14), (0, 0.35, 0))
    box("Display", shell, (0.1, 0.06, 0.006), (0, 0.36, 0.073))
    export("flow-meter.glb")


def build_main():
    # The force main leaving the drawing: a flange, a stub and an arrowhead
    # pointing +x — the mimic's ToProcess connector, in space.
    reset()
    steel = pbr("Steel", STEEL, metallic=0.75, roughness=0.35)
    dark = pbr("SteelDark", STEEL_DARK, metallic=0.6, roughness=0.55)
    cylinder("Flange", dark, 0.15, 0.03, axis="x", pos=(0.015, 0, 0), segments=32)
    cylinder("Stub", steel, 0.075, 0.12, axis="x", pos=(0.09, 0, 0), segments=24)
    cone("Arrow", dark, 0.16, 0.0, 0.4, axis="x", pos=(0.35, 0, 0))
    export("force-main.glb")


BUILDERS = {
    "well": build_well,
    "pump": build_pump,
    "float": build_float,
    "check": build_check,
    "meter": build_meter,
    "main": build_main,
}

if __name__ == "__main__":
    which = sys.argv[sys.argv.index("--") + 1 :] if "--" in sys.argv else list(BUILDERS)
    for w in which:
        BUILDERS[w]()
