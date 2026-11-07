"""Bundle playground examples as JSON: {name, title, description, open, files}.

Usage: examples.py OUTDIR NAME=DIR[:open1,open2[:Title[:Description]]] ...
An example with a built HMI in OUTDIR/../hmi/NAME/ gets "hmi": NAME.
Every text file a project carries is included (IEC sources, manifests, tag
files, test suites, mimic/scene JSON, READMEs); built HMIs, node_modules and
binaries are not.
"""
import json, os, sys

TEXT = ('.yaml', '.yml', '.st', '.ld', '.fbd', '.sfc', '.json', '.md')
SKIP_DIRS = {'hmi', 'node_modules', '.git', 'build', 'pki'}
SKIP_FILES = {'AGENTS.md', 'CLAUDE.md', 'seed.json', 'retain.json'}

def bundle(name, root, opens):
    files = {}
    for dirpath, dirs, names in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS and not d.startswith('.'))
        for n in sorted(names):
            if n.endswith(TEXT) and n not in SKIP_FILES:
                p = os.path.join(dirpath, n)
                files[os.path.relpath(p, root)] = open(p, encoding='utf-8').read()
    manifest = files.get('nautilus.yaml', '')
    title, desc = name, ''
    for line in manifest.splitlines():
        if line.startswith('# ') and not desc:
            desc = line[2:].strip()
        if line.startswith('name:'):
            title = line.split(':', 1)[1].strip().strip('"')
            break
    return {'name': name, 'title': title, 'description': desc, 'open': opens or ['nautilus.yaml'], 'files': files}

out = sys.argv[1]
os.makedirs(out, exist_ok=True)
index = []
for arg in sys.argv[2:]:
    name, rest = arg.split('=', 1)
    root, _, rest = rest.partition(':')
    opens, _, rest = rest.partition(':')
    title, _, desc = rest.partition(':')
    ex = bundle(name, root, [o for o in opens.split(',') if o])
    if title:
        ex['title'] = title
    if desc:
        ex['description'] = desc
    # A prebuilt HMI (build.sh puts it in dist/hmi/NAME/): sw.js serves it.
    if os.path.isfile(os.path.join(out, '..', 'hmi', name, 'index.html')):
        ex['hmi'] = name
    with open(os.path.join(out, name + '.json'), 'w', encoding='utf-8') as f:
        json.dump(ex, f)
    index.append({k: ex[k] for k in ('name', 'title', 'description')})
    print(f'  example {name}: {len(ex["files"])} files')
with open(os.path.join(out, 'index.json'), 'w', encoding='utf-8') as f:
    json.dump(index, f)
