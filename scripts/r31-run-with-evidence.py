#!/usr/bin/env python3
"""Launch the unchanged R31 tool, retaining its already-collected frozen file hashes.
Run only by the maintainer in an interactive root SSH terminal.
"""
import argparse
import importlib.util
import os
import json
from pathlib import Path
import sys
sys.dont_write_bytecode=True
root=Path(__file__).resolve().parent
parser=argparse.ArgumentParser(add_help=False)
parser.add_argument('--evidence-dir',type=Path,default=root/'evidence')
options,remaining=parser.parse_known_args()
sys.argv=[sys.argv[0],*remaining]
out=options.evidence_dir
if not out.is_dir() or out.is_symlink(): raise SystemExit('private evidence directory not prepared')
if os.name=='posix' and out.stat().st_mode&0o777!=0o700: raise SystemExit('evidence directory must be 0700')
if any(out.iterdir()): raise SystemExit('existing run evidence; ask Codex before rerunning')
spec=importlib.util.spec_from_file_location('r31_upgrade',root/'r31-upgrade.py')
r31=importlib.util.module_from_spec(spec); spec.loader.exec_module(r31)
original_tree=r31.tree_state
original_restore=r31.restore_installation

def save(name,data):
    with (out/name).open('x',encoding='utf-8') as stream: stream.write(json.dumps(data,indent=2)+'\n')
    (out/name).chmod(0o600)

def capture_before(paths):
    state=original_tree(paths)
    if list(paths)==r31.PATHS: save('before-frozen.json',state)
    return state

def capture_after(paths):
    state=original_tree(paths)
    save('after-restored-frozen.json',state)
    before=json.loads((out/'before-frozen.json').read_text())
    differences=[p for p in sorted(set(before)|set(state)) if before.get(p)!=state.get(p)]
    save('frozen-compare.json',dict(equal=not differences,changed_paths=differences,
        before_entries=len(before),after_entries=len(state),comparison='after restore, before original Server restart'))
    return state

def observed_restore(*args,**kwargs):
    kwargs['state']=capture_after
    return original_restore(*args,**kwargs)

r31.tree_state=capture_before
r31.restore_installation=observed_restore
raise SystemExit(r31.main())
