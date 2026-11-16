#!/usr/bin/env python3
"""CLI contract checks; never connects to SSH targets."""
import os,pathlib,subprocess,tempfile
root=pathlib.Path(__file__).resolve().parents[1]
binary=os.environ.get('SWARMCOPY_BINARY',str(root/'swarmcopy'))
commands=['distribute','jobs','inspect','watch','status','resume','stop','cleanup','remove','delete','prepare','peer','hashbench']
with tempfile.TemporaryDirectory(prefix='swarmcopy-cli-') as tmp:
    env=os.environ.copy();env['XDG_STATE_HOME']=tmp
    def call(args):
        return subprocess.run([binary]+args,env=env,text=True,capture_output=True,timeout=10)
    r=call([])
    assert r.returncode==0,r.stderr
    for command in commands:
        assert command in r.stdout,command
        for args in [[command,'--help'],['help',command]]:
            result=call(args)
            assert result.returncode==0,(args,result.stderr)
            assert 'Example' in result.stdout+result.stderr,(args,result.stdout,result.stderr)
    for command in ['distribute','inspect','resume','stop','cleanup','remove','delete','prepare','peer']:
        r=call([command])
        assert r.returncode!=0 and 'Usage:' in r.stdout+r.stderr,command
    for command in ['watch','status']:
        r=call([command])
        assert r.returncode!=0 and 'no saved jobs' in r.stderr and 'Example' in r.stdout+r.stderr,(command,r)
    r=call(['jobs','--json'])
    assert r.returncode==0
print('PASS: root help, per-command help, help COMMAND, missing arguments, empty job directory')
