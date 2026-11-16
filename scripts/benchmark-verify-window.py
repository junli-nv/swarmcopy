#!/usr/bin/env python3
"""Fresh-target, cold-cache comparison: published baseline / pipeline with global rarity / pipeline with prefix window."""
import argparse,concurrent.futures,datetime,hashlib,json,pathlib,secrets,shlex,statistics,subprocess,time,traceback
p=argparse.ArgumentParser(description=__doc__)
for name in ['hosts-file','seed','seed-file','dest-root','out','baseline','candidate']:p.add_argument('--'+name,required=True)
p.add_argument('--rounds',type=int,default=2)
p.add_argument('--timeout',type=int,default=900)
p.add_argument('--port',type=int,default=19871)
p.add_argument('--drop-caches',action='store_true',required=True)
a=p.parse_args()
assert 1<=a.rounds<=6 and 1<=a.port<=65535
hosts=[h.strip() for h in pathlib.Path(a.hosts_file).read_text().splitlines() if h.strip()]
assert len(hosts)==len(set(hosts)) and len(hosts)>1 and a.seed in hosts
out=pathlib.Path(a.out).resolve();out.mkdir(parents=True,mode=0o700,exist_ok=False)
bins={'baseline':pathlib.Path(a.baseline).resolve(),'plain':pathlib.Path(a.candidate).resolve(),'windowed':pathlib.Path(a.candidate).resolve()}
study=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')+'-'+secrets.token_hex(4)
def save(name,value):
 f=out/name;tmp=f.with_name(f.name+'.next');tmp.write_text(json.dumps(value,indent=2));tmp.replace(f)
def ssh(host,command):
 print(json.dumps({'target':host,'reason':'verification comparison operation','command':command}),flush=True)
 r=subprocess.run(['ssh','-T','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=8','--',host,command],capture_output=True,text=True,timeout=120)
 assert r.returncode==0,(host,r.stderr)
 return r.stdout
def parallel(fn):
 with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:return list(pool.map(fn,hosts))
manifest=json.loads(ssh(a.seed,'cat -- '+shlex.quote(a.seed_file+'.manifest.json')))
assert ssh(a.seed,'sha256sum -- '+shlex.quote(a.seed_file)).split()[0]==manifest['SHA256']
frozen={}
for method,binary in bins.items():
 frozen[method]={'binary':hashlib.sha256(binary.read_bytes()).hexdigest(),'dist':{f.name:hashlib.sha256(f.read_bytes()).hexdigest() for f in (binary.parent/'dist').glob('swarmcopy-linux-*')}}
orders=[['baseline','plain','windowed'],['windowed','plain','baseline']]
cases=[{'round':n+1,'method':method,'case_id':f'r{n+1:02d}-{method}-'+secrets.token_hex(4)} for n in range(a.rounds) for method in orders[n%2]]
plan={'study_id':study,'nodes':len(hosts),'hosts':hosts,'seed':a.seed,'file_bytes':manifest['Size'],'file_sha256':manifest['SHA256'],
 'rounds':a.rounds,'cases':cases,'frozen':frozen,'driver_sha256':hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest(),
 'policy':'Unique absent case directory; sync/drop_caches=3 on every participant before every method; prepared common seed; unlimited bandwidth; full SHA256/fsync; timing through peer shutdown; retries remain in timed case.'}
save('plan.json',plan);results=[];jobs=[]
try:
 for case in cases:
  method=case['method'];binary=bins[method];cid=case['case_id']
  assert hashlib.sha256(binary.read_bytes()).hexdigest()==frozen[method]['binary']
  assert {f.name:hashlib.sha256(f.read_bytes()).hexdigest() for f in (binary.parent/'dist').glob('swarmcopy-linux-*')}==frozen[method]['dist']
  dest=a.dest_root.rstrip('/')+'/'+study+'/'+cid;job=out/(cid+'-job')
  save('progress.json',{**case,'stage':'fresh-cache-barrier','completed_cases':len(results),'nodes':len(hosts)})
  def barrier(host):
   command='set -e\ntest ! -e '+shlex.quote(dest)+"\necho BEFORE\nawk '/^(Cached|SReclaimable|MemAvailable):/ {print}' /proc/meminfo\nsync\necho 3 > /proc/sys/vm/drop_caches\necho AFTER\nawk '/^(Cached|SReclaimable|MemAvailable):/ {print}' /proc/meminfo\ncat /proc/loadavg"
   return {'host':host,'exit_code':0,'output':ssh(host,command)}
  save(cid+'-barrier.json',parallel(barrier))
  command=[str(binary),'distribute','--hosts','@'+str(pathlib.Path(a.hosts_file).resolve()),'--seed',a.seed,'--seed-file',a.seed_file,
   '--dest',dest,'--job',str(job),'--bin-dir',str(binary.parent/'dist'),'--port',str(a.port),'--lifetime',str(a.timeout)+'s']
  if method!='baseline':command+=['--pipeline-verify','--verify-buffer-mib','4']
  if method=='windowed':command+=['--verify-window-chunks','256']
  save(cid+'-command.json',command)
  print(json.dumps({'case':case,'command':command}),flush=True)
  started=time.monotonic()
  with (out/(cid+'.log')).open('w') as log:
   child=subprocess.Popen(command,stdout=log,stderr=subprocess.STDOUT)
   while True:
    try:
     child.wait(timeout=1)
     break
    except subprocess.TimeoutExpired:
     save('progress.json',{**case,'stage':'transfer','elapsed_seconds':time.monotonic()-started,'completed_cases':len(results),'nodes':len(hosts),'pid':child.pid})
  elapsed=time.monotonic()-started
  assert child.returncode==0,(cid,'controller failed; case log retained')
  report=json.loads((job/'result.json').read_text());rows=report['Nodes']
  for host in hosts:
   s=rows[host+':'+str(a.port)]
   assert s['Complete'] and s['VerifiedBytes']==manifest['Size'],(cid,host)
   if host!=a.seed:assert not s['Origin'] and s['Downloaded']==manifest['Size'],(cid,host,'not fresh')
   if method!='baseline':
    assert s['Performance']['PipelineVerify'] and s['Performance']['VerifyWindowChunks']==(256 if method=='windowed' else 0) and not s['Performance']['Adaptive'],(cid,host,'deployment flag mismatch')
    if host!=a.seed:assert s['Performance']['FileHashBytes']==manifest['Size'],(cid,host,'whole SHA not completed')
  downloaded=sum(s['Downloaded'] for s in rows.values());assert downloaded==manifest['Size']*(len(hosts)-1)
  perfs=[s['Performance'] for s in rows.values() if s.get('Performance')]
  metrics={}
  for key in ['ScanSeconds','TransferSeconds','FinalSyncSeconds','FinalHashSeconds','PublishSeconds','FileHashReadSeconds','FileHashComputeSeconds','FileHashWaitSeconds','FileHashWallSeconds']:
   metrics[key+'_max']=max((s[key] for s in perfs if key in s),default=None)
  for key in ['Requests','Retries','BusyResponses','HedgeRequests','CanceledRequests','WorkerChanges','ReceivedBytes','DuplicateBytes','FileHashBytes','FileHashBytesAtTransferEnd']:
   metrics[key+'_sum']=sum(s[key] for s in perfs if key in s) if any(key in s for s in perfs) else None
  result={**case,'seconds':elapsed,'verified':True,'fresh_start':True,'downloaded_bytes':downloaded,'metrics':metrics}
  results.append(result);jobs.append((cid,json.loads((job/'job.json').read_text())))
  save('results.json',results);print(json.dumps({'completed':result}),flush=True)
 # Audit every completed job and independent kernel identities after timing.
 def audit(host):
  commands=['set -e','cat /proc/sys/kernel/random/boot_id']
  for cid,j in jobs:
   remote=j['Dest']+'/.swarmcopy/'+j['ID']
   commands += [shlex.quote(remote+'/swarmcopy')+' agent inspect --config '+shlex.quote(remote+'/peer.json')]
  lines=ssh(host,'\n'.join(commands)).splitlines()
  assert len(lines)==len(jobs)+1
  records=[json.loads(line) for line in lines[1:]]
  assert all(not r['Alive'] and r['PID']>0 for r in records),(host,'peer still active')
  return {'host':host,'boot_id':lines[0],'processes':records}
 audits=parallel(audit);assert len({r['boot_id'] for r in audits})==len(hosts)
 save('process-audit.json',audits)
 summary={'nodes':len(hosts),'receivers':len(hosts)-1,'file_bytes':manifest['Size'],'rounds':a.rounds,'cases':results,'methods':{},
  'unique_boot_ids':len(hosts),'owned_peers_stopped':len(hosts)*len(jobs)}
 for method in bins:
  values=[r['seconds'] for r in results if r['method']==method]
  summary['methods'][method]={'seconds':values,'median':statistics.median(values),'min':min(values),'max':max(values)}
 summary['windowed_speedup_vs_baseline']=summary['methods']['baseline']['median']/summary['methods']['windowed']['median']
 summary['windowed_speedup_vs_plain']=summary['methods']['plain']['median']/summary['methods']['windowed']['median']
 save('summary.json',summary);save('progress.json',{'stage':'complete','completed_cases':len(results),'nodes':len(hosts)})
 print(json.dumps(summary),flush=True)
except BaseException as error:
 save('failure.json',{'error':repr(error),'traceback':traceback.format_exc(),'completed_cases':len(results)})
 save('progress.json',{'stage':'failed','error':repr(error)})
 raise

