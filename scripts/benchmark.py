#!/usr/bin/env python3
"""Sequential rsync vs SCDP fan-out benchmark; optional development tool."""
import argparse,concurrent.futures,json,pathlib,shlex,subprocess,time
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--hosts-file',required=True,help='literal hostnames, one per line; includes seed')
p.add_argument('--seed',required=True)
p.add_argument('--seed-file',required=True,help='prepared seed file with .manifest.json')
p.add_argument('--dest-root',required=True,help='new node-local benchmark directory')
p.add_argument('--out',required=True,help='new local report directory')
p.add_argument('--swarmcopy',default='./swarmcopy')
p.add_argument('--bin-dir',default='./dist')
p.add_argument('--timeout',type=int,default=1800)
p.add_argument('--pin-seed-host-key',action='store_true',help='pin seed ED25519 key via already authenticated controller SSH in temporary benchmark known_hosts files')
a=p.parse_args()
out=pathlib.Path(a.out).resolve();out.mkdir(mode=0o700,parents=True,exist_ok=False)
hosts=[s.strip() for s in pathlib.Path(a.hosts_file).read_text().splitlines() if s.strip() and not s.lstrip().startswith('#')]
assert len(hosts)==len(set(hosts)) and a.seed in hosts and len(hosts)>1,'need unique hosts including seed'
receivers=[h for h in hosts if h!=a.seed]
def save(name,data):
    tmp=out/(name+'.next');tmp.write_text(json.dumps(data,indent=2));tmp.replace(out/name)
def ssh(host,command,timeout=None,input=None):
    print('TARGET:',host,'REASON: benchmark; COMMAND:',command,flush=True)
    return subprocess.run(['ssh','-T','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=8','--',host,command],
        capture_output=True,text=True,input=input,timeout=timeout or a.timeout)
manifest=ssh(a.seed,'cat -- '+shlex.quote(a.seed_file+'.manifest.json'))
assert manifest.returncode==0,manifest.stderr
m=json.loads(manifest.stdout)
rsync_dir=a.dest_root+'/rsync'
ssh_transport='ssh -T -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=8 -o Compression=no'
if a.pin_seed_host_key:
    key=ssh(a.seed,'cat /etc/ssh/ssh_host_ed25519_key.pub')
    assert key.returncode==0,key.stderr
    fields=key.stdout.split()
    assert len(fields)>=2 and fields[0]=='ssh-ed25519','invalid ED25519 public key'
    entry=a.seed.split('@')[-1].lower()+' '+fields[0]+' '+fields[1]+'\n'
    pin=a.dest_root+'/known_hosts'
    def install_pin(host):
        command='umask 077; mkdir -p -- '+shlex.quote(a.dest_root)+'; cat > '+shlex.quote(pin)
        r=ssh(host,command,input=entry)
        assert r.returncode==0,(host,r.stderr)
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        list(pool.map(install_pin,receivers))
    ssh_transport+=' -o UserKnownHostsFile='+shlex.quote(pin)+' -o GlobalKnownHostsFile=/dev/null'
    def preflight(host):
        r=ssh(host,ssh_transport+' '+shlex.quote(a.seed)+' true')
        assert r.returncode==0,(host,r.stderr)
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        list(pool.map(preflight,receivers))
    print('SEED_HOST_KEY: authenticated through controller SSH and pinned only in benchmark directory',flush=True)
def pull(item):
    index,host=item
    time.sleep(index*.1)  # avoid bursting SSH MaxStartups on a single seed
    target=rsync_dir+'/'+m['Name']
    command='mkdir -p -- '+shlex.quote(rsync_dir)+' && test ! -e '+shlex.quote(target)+' && rsync --whole-file --stats -e '+shlex.quote(ssh_transport)+' -- '+shlex.quote(a.seed+':'+a.seed_file)+' '+shlex.quote(target)
    start=time.monotonic()
    attempts=[]
    for attempt in range(3):
        r=ssh(host,command);attempts.append({'returncode':r.returncode,'stdout':r.stdout,'stderr':r.stderr})
        if r.returncode==0:break
        time.sleep(1+attempt)
    return {'host':host,'seconds':time.monotonic()-start,'returncode':r.returncode,'attempts':attempts}
print('PHASE: baseline rsync,',len(receivers),'parallel full-file receivers',flush=True)
start=time.monotonic();results=[]
with concurrent.futures.ThreadPoolExecutor(max_workers=len(receivers)) as pool:
    for result in pool.map(pull,enumerate(receivers)):
        results.append(result);print('RSYNC_RESULT',json.dumps(result),flush=True)
baseline={'nodes':len(hosts),'receivers':len(receivers),'file_bytes':m['Size'],'copy_seconds':time.monotonic()-start,'results':results}
save('rsync.json',baseline)
assert all(r['returncode']==0 for r in results),'rsync failure; SCDP not started'
def verify(host):
    r=ssh(host,'sha256sum -- '+shlex.quote(rsync_dir+'/'+m['Name']))
    return {'host':host,'verified':r.returncode==0 and bool(r.stdout.split()) and r.stdout.split()[0]==m['SHA256']}
with concurrent.futures.ThreadPoolExecutor(max_workers=len(receivers)) as pool:
    baseline['checks']=list(pool.map(verify,receivers))
baseline['copy_plus_sha256_seconds']=time.monotonic()-start
baseline['verified']=all(r['verified'] for r in baseline['checks'])
save('rsync.json',baseline)
assert baseline['verified'],'baseline SHA256 failed'
print('PHASE: SCDP, baseline fully finished; no overlapping transfers',flush=True)
command=[str(pathlib.Path(a.swarmcopy).resolve()),'distribute','--hosts','@'+str(pathlib.Path(a.hosts_file).resolve()),
 '--seed',a.seed,'--seed-file',a.seed_file,'--dest',a.dest_root+'/scdp','--job',str(out/'scdp-job'),
 '--bin-dir',str(pathlib.Path(a.bin_dir).resolve()),'--upload-mib','0','--download-mib','0','--lifetime',str(a.timeout)+'s']
start=time.monotonic()
r=subprocess.run(command)
scdp={'seconds':time.monotonic()-start,'returncode':r.returncode}
save('scdp.json',scdp)
assert r.returncode==0,'SCDP failed'
detail=json.loads((out/'scdp-job/result.json').read_text())
scdp['verified']=all(n['Complete'] for n in detail['Nodes'].values())
scdp['received_bytes']=sum(n['Downloaded'] for n in detail['Nodes'].values())
scdp['seed_bytes']=sum(n['From'].get(a.seed.split('@')[-1]+':19700',0) for n in detail['Nodes'].values())
assert scdp['verified'] and scdp['received_bytes']==m['Size']*len(receivers),'not a verified fresh full transfer'
save('scdp.json',scdp)
summary={'nodes':len(hosts),'receivers':len(receivers),'file_bytes':m['Size'],
 'rsync_copy_seconds':baseline['copy_seconds'],'rsync_copy_plus_sha256_seconds':baseline['copy_plus_sha256_seconds'],
 'scdp_verified_seconds':scdp['seconds'],'speedup_vs_copy':baseline['copy_seconds']/scdp['seconds'],
 'speedup_vs_copy_plus_sha256':baseline['copy_plus_sha256_seconds']/scdp['seconds'],
 'scdp_seed_bytes':scdp['seed_bytes'],'scdp_received_bytes':scdp['received_bytes']}
save('summary.json',summary)
print('BENCHMARK_SUMMARY',json.dumps(summary,indent=2),flush=True)
