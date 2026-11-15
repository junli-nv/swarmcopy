#!/usr/bin/env python3
"""Paired fresh-file, cold-OS-cache rsync/SCDP study. Requires explicit --drop-caches."""
import argparse,concurrent.futures,datetime,hashlib,json,os,pathlib,re,resource,secrets,shlex,statistics,subprocess,threading,time,traceback
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--hosts-file',required=True)
p.add_argument('--seed',required=True)
p.add_argument('--seed-file',required=True)
p.add_argument('--dest-root',required=True,help='node-local parent for unique per-case directories')
p.add_argument('--out',required=True,help='new local results directory')
p.add_argument('--swarmcopy',default='./swarmcopy')
p.add_argument('--bin-dir',default='./dist')
p.add_argument('--rounds',type=int,default=3)
p.add_argument('--timeout',type=int,default=1800,help='per-transfer timeout seconds')
p.add_argument('--drop-caches',action='store_true',required=True,help='authorize sync and drop_caches=3 on listed compute hosts')
a=p.parse_args()
assert 1<=a.rounds<=10 and a.timeout>0
assert a.dest_root.startswith('/') and a.seed_file.startswith('/')
hosts=[s.split('#',1)[0].strip() for s in pathlib.Path(a.hosts_file).read_text().splitlines()]
hosts=[h for h in hosts if h]
assert len(hosts)==len(set(hosts)) and a.seed in hosts and len(hosts)>1
assert all(re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.@-]*',h) for h in hosts)
out=pathlib.Path(a.out).resolve();out.mkdir(mode=0o700,parents=True,exist_ok=False)
soft,hard=resource.getrlimit(resource.RLIMIT_NOFILE)
if soft<8192:resource.setrlimit(resource.RLIMIT_NOFILE,(min(8192,hard),hard))
binary=pathlib.Path(a.swarmcopy).resolve();bindir=pathlib.Path(a.bin_dir).resolve()
binary_hash=hashlib.sha256(binary.read_bytes()).hexdigest()
dist_hashes={f.name:hashlib.sha256(f.read_bytes()).hexdigest() for f in sorted(bindir.glob('swarmcopy-linux-*'))}
study_id=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')+'-'+secrets.token_hex(4)
remote_study=a.dest_root.rstrip('/')+'/'+study_id
receivers=[h for h in hosts if h!=a.seed]
log_lock=threading.Lock();progress_lock=threading.Lock()
progress={'stage':'preflight','study_id':study_id,'nodes':len(hosts),'started':time.time()}
def save(name,value):
    target=out/name;tmp=target.with_name(target.name+'.next')
    tmp.write_text(json.dumps(value,indent=2));tmp.replace(target)
def event(kind,**kw):
    with log_lock:print(json.dumps({'event':kind,'time':time.time(),**kw}),flush=True)
def update(**kw):
    with progress_lock:progress.update(kw);save('progress.json',progress)
def ssh(host,command,input=None,timeout=None):
    event('ssh',target=host,reason='controlled benchmark operation',command=command)
    return subprocess.run(['ssh','-T','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=8','--',host,command],
        input=input,capture_output=True,text=True,timeout=timeout or a.timeout)
def parallel(fn,items,workers=16):
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:return list(pool.map(fn,items))
def require(result,context):
    if result.returncode:raise RuntimeError(context+': '+result.stderr.strip())
    return result.stdout
def address(host):return host.split('@')[-1]+':19700'
manifest=json.loads(require(ssh(a.seed,'cat -- '+shlex.quote(a.seed_file+'.manifest.json')),'read trusted manifest'))
size=manifest['Size'];expected_hash=manifest['SHA256'];name=manifest['Name']
# Source verification is common preparation, before all timed cold-cache cases.
source_hash=require(ssh(a.seed,'sha256sum -- '+shlex.quote(a.seed_file)),'verify prepared source').split()[0]
assert source_hash==expected_hash
key=require(ssh(a.seed,'cat /etc/ssh/ssh_host_ed25519_key.pub'),'read already authenticated seed host public key').split()
assert key[0]=='ssh-ed25519'
alias=a.seed.split('@')[-1].lower();entry=alias+' '+key[0]+' '+key[1]+'\n'
pin=remote_study+'/trusted_seed_known_hosts'
transport='ssh -T -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=8 -o Compression=no -o HostKeyAlias='+shlex.quote(alias)+' -o UserKnownHostsFile='+shlex.quote(pin)+' -o GlobalKnownHostsFile=/dev/null'
def prepare_host(host):
    cmd='set -e\ntest -w /proc/sys/vm/drop_caches\numask 077\nmkdir -p -- '+shlex.quote(remote_study)+'\ncat > '+shlex.quote(pin)
    require(ssh(host,cmd,input=entry,timeout=60),'cache permission and temporary host-key pin '+host)
parallel(prepare_host,hosts)
def auth_check(host):
    require(ssh(host,transport+' '+shlex.quote(a.seed)+' true',timeout=30),'receiver-to-seed SSH '+host)
parallel(auth_check,receivers,8)
orders=[['rsync','scdp'] if r%2==0 else ['scdp','rsync'] for r in range(a.rounds)]
cases=[{'round':r+1,'method':method,'case_id':f'r{r+1:02d}-{method}-'+secrets.token_hex(4)} for r,order in enumerate(orders) for method in order]
plan={'study_id':study_id,'created_utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),'hosts':hosts,'seed':a.seed,
 'host_set_sha256':hashlib.sha256('\n'.join(sorted(hosts)).encode()).hexdigest(),'nodes':len(hosts),'receivers':len(receivers),
 'file_bytes':size,'file_sha256':expected_hash,'driver_sha256':hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(),'binary_sha256':binary_hash,'dist_sha256':dist_hashes,
 'rounds':a.rounds,'orders':orders,'cases':cases,'cache_policy':'sync + drop_caches=3 on every participating compute host before each method',
 'fresh_start_policy':'unique case directory absent everywhere before timing; no cross-case data or partial reuse',
 'retry_policy':'normal TCP/chunk recovery and in-case retry allowed; all attempt time counted without resetting the case timer',
 'primary_metric':'time until all receivers verified; SCDP also includes deployment and owned-peer shutdown',
 'baseline_verification':'each receiver hashes immediately after its own rsync copy; verification overlaps across receivers',
 'transports':{'rsync':'SSH encrypted, compression off','scdp':'HTTP plaintext'},
 'source_policy':'same prepared verified immutable seed; verification sidecar retained; source verified once before study and cold-cache barriers',
 'limitations':['OS page cache is cleared; device/controller caches are not controlled.','Background cluster load is not isolated.','Three observations do not establish a universal performance guarantee.']}
save('study-plan.json',plan)
event('plan_frozen',nodes=len(hosts),receivers=len(receivers),orders=orders,binary_sha256=binary_hash)
def fresh_and_cold(case_dir):
    def clear(host):
        command='set -e\ntest ! -e '+shlex.quote(case_dir)+'\necho BEFORE\nawk \'/^(Cached|SReclaimable|MemAvailable):/ {print}\' /proc/meminfo\nsync\nprintf \'3\\n\' > /proc/sys/vm/drop_caches\necho AFTER\nawk \'/^(Cached|SReclaimable|MemAvailable):/ {print}\' /proc/meminfo\ncat /proc/loadavg'
        before=time.monotonic();r=ssh(host,command,timeout=120)
        text=require(r,'fresh-start/cache barrier '+host)
        return {'host':host,'exit_code':r.returncode,'seconds':time.monotonic()-before,'output':text}
    return parallel(clear,hosts,16)
def rsync_case(case,case_dir):
    case_start=time.monotonic();completed=[];done_lock=threading.Lock()
    marker='SWARMCOPY_COPY_DONE_'+case['case_id']
    def transfer(item):
        index,host=item;time.sleep(index*.1)
        target=case_dir+'/'+name
        command='set -e\nmkdir -p -- '+shlex.quote(case_dir)+'\nLC_ALL=C rsync --whole-file --stats -e '+shlex.quote(transport)+' -- '+shlex.quote(a.seed+':'+a.seed_file)+' '+shlex.quote(target)+'\nprintf \'\\n'+marker+'\\n\'\nsha256sum -- '+shlex.quote(target)
        attempts=[];copy_done=None;verified=False
        for attempt in range(3):
            event('ssh',target=host,reason='fresh-file rsync plus immediate SHA256',command=command,attempt=attempt+1)
            process=subprocess.Popen(['ssh','-T','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=8','--',host,command],stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
            lines=[];attempt_copy=None;attempt_hash=None
            timer=threading.Timer(a.timeout,process.kill);timer.start()
            try:
                for line in process.stdout:
                    lines.append(line)
                    if line.strip()==marker:attempt_copy=time.monotonic()-case_start
                    parts=line.split()
                    if attempt_copy is not None and parts and parts[0]==expected_hash:attempt_hash=parts[0]
                rc=process.wait()
            finally:timer.cancel()
            text=''.join(lines)
            literal=re.search(r'Literal data:\s*([\d,]+) bytes',text)
            transferred=re.search(r'Total transferred file size:\s*([\d,]+) bytes',text)
            attempts.append({'exit_code':rc,'copy_done_seconds':attempt_copy,'verified':attempt_hash==expected_hash,
             'literal_bytes':int(literal.group(1).replace(',','')) if literal else None,
             'transferred_file_bytes':int(transferred.group(1).replace(',','')) if transferred else None,'output':text})
            if rc==0 and attempt_hash==expected_hash:
                copy_done=attempt_copy;verified=True;break
            time.sleep(attempt+1)
        result={'host':host,'verified':verified,'copy_done_seconds':copy_done,'verified_done_seconds':time.monotonic()-case_start,'attempts':attempts}
        if len(attempts)==1 and verified:
            assert attempts[0]['literal_bytes']==size and attempts[0]['transferred_file_bytes']==size,(host,'not full literal transfer')
        with done_lock:
            completed.append(result);update(completed=len(completed),total=len(receivers),elapsed_seconds=time.monotonic()-case_start)
        event('rsync_receiver_complete',host=host,verified=verified,attempts=len(attempts))
        return result
    rows=parallel(transfer,list(enumerate(receivers)),len(receivers))
    result={'round':case['round'],'method':'rsync','fresh_start':True,'rows':rows,'verified':all(r['verified'] for r in rows)}
    save(case['case_id']+'.json',result)
    assert result['verified'],'rsync did not verify every receiver'
    result['copy_seconds']=max(r['copy_done_seconds'] for r in rows)
    result['verified_seconds']=max(r['verified_done_seconds'] for r in rows)
    result['attempts']=sum(len(r['attempts']) for r in rows)
    return result
def scdp_case(case,case_dir):
    job=out/(case['case_id']+'-job')
    command=[str(binary),'distribute','--hosts','@'+str(pathlib.Path(a.hosts_file).resolve()),'--seed',a.seed,
     '--seed-file',a.seed_file,'--dest',case_dir,'--job',str(job),'--bin-dir',str(bindir),
     '--upload-mib','0','--download-mib','0','--lifetime',str(a.timeout)+'s']
    event('scdp_start',command=command)
    start=time.monotonic()
    with (out/(case['case_id']+'.log')).open('w') as log:
        child=subprocess.Popen(command,stdout=log,stderr=subprocess.STDOUT)
        while True:
            try:
                child.wait(timeout=1)
                break
            except subprocess.TimeoutExpired:
                update(elapsed_seconds=time.monotonic()-start,controller_pid=child.pid)
    seconds=time.monotonic()-start
    assert child.returncode==0,'SCDP controller failed; see case log'
    report=json.loads((job/'result.json').read_text())
    rows=report['Nodes']
    for host in receivers:
        status=rows[address(host)]
        assert status['Complete'] and not status['Origin'] and status['Downloaded']==size,(host,'not a fresh full transfer')
    downloaded=sum(s['Downloaded'] for s in rows.values())
    assert downloaded==size*len(receivers)
    return {'round':case['round'],'method':'scdp','fresh_start':True,'verified':True,'verified_seconds':seconds,
     'downloaded_bytes':downloaded,'seed_valid_bytes':sum(s['From'].get(address(a.seed),0) for s in rows.values()),
     'receiver_origin_count':sum(rows[address(h)]['Origin'] for h in receivers),'job_directory':str(job)}
results=[]
try:
    for case in cases:
        assert hashlib.sha256(binary.read_bytes()).hexdigest()==binary_hash,'binary changed during study'
        assert {f.name:hashlib.sha256(f.read_bytes()).hexdigest() for f in sorted(bindir.glob('swarmcopy-linux-*'))}==dist_hashes,'deployment binaries changed'
        case_dir=remote_study+'/'+case['case_id']
        update(stage='cold-cache barrier',round=case['round'],method=case['method'],case_id=case['case_id'],completed=0,total=len(hosts),elapsed_seconds=0)
        barrier=fresh_and_cold(case_dir)
        save(case['case_id']+'-cache-barrier.json',barrier)
        update(stage='timed transfer',completed=0,total=len(receivers),elapsed_seconds=0)
        result=rsync_case(case,case_dir) if case['method']=='rsync' else scdp_case(case,case_dir)
        result.update(case_id=case['case_id'],remote_directory=case_dir)
        results.append(result);save(case['case_id']+'.json',result);save('results.json',results)
        event('case_complete',case_id=case['case_id'],verified_seconds=result['verified_seconds'],copy_seconds=result.get('copy_seconds'))
    summary={'study_id':study_id,'nodes':len(hosts),'receivers':len(receivers),'rounds':a.rounds,'file_bytes':size,'binary_sha256':binary_hash,'cases':[]}
    for result in results:
        summary['cases'].append({k:v for k,v in result.items() if k not in ['rows','job_directory','remote_directory']})
    for method in ['rsync','scdp']:
        values=[r['verified_seconds'] for r in results if r['method']==method]
        summary[method]={'verified_seconds':values,'median':statistics.median(values),'min':min(values),'max':max(values)}
    copies=[r['copy_seconds'] for r in results if r['method']=='rsync']
    summary['rsync']['copy_seconds']=copies
    summary['rsync']['copy_median']=statistics.median(copies)
    summary['speedup_verified_median']=summary['rsync']['median']/summary['scdp']['median']
    summary['speedup_vs_copy_median']=summary['rsync']['copy_median']/summary['scdp']['median']
    save('summary.json',summary);update(stage='complete',finished=time.time())
    event('study_complete',summary=summary)
except BaseException as error:
    save('failure.json',{'error':repr(error),'traceback':traceback.format_exc(),'completed_cases':len(results),'time':time.time()})
    update(stage='failed',error=repr(error));raise

