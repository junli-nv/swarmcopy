#!/usr/bin/env python3
"""Local three-process chain: interruption, corruption repair, and partial forwarding."""
import hashlib, json, os, pathlib, shlex, socket, subprocess, tempfile, time
EXTRA = shlex.split(os.environ.get("SWARMCOPY_TEST_FLAGS",""))
BINARY = os.environ.get("SWARMCOPY_BINARY", str(pathlib.Path(__file__).resolve().parents[1] / "swarmcopy"))
SEED_BINARY = os.environ.get("SWARMCOPY_SEED_BINARY", BINARY)
def freeport():
    with socket.socket() as s:
        s.bind(("127.0.0.1",0))
        return s.getsockname()[1]
def check(condition, message):
    if not condition: raise AssertionError(message)
with tempfile.TemporaryDirectory(prefix="swarmcopy-test-") as tmp:
    root=pathlib.Path(tmp)
    source=root/"source"; source.write_bytes(os.urandom(32*1024*1024+123))
    token=root/"token"; token.write_text(os.urandom(32).hex()); token.chmod(0o600)
    seed=root/"seed"/"image.sqsh"
    subprocess.run([BINARY,"prepare","--source",str(source),"--out",str(seed),"--chunk-mib","1"],check=True)
    manifest=pathlib.Path(str(seed)+".manifest.json")
    ports=[freeport() for _ in range(3)]
    check(len(set(ports))==3,"port collision")
    files=[seed,root/"b"/"image.sqsh",root/"c"/"image.sqsh"]
    procs=[]; logs=[]
    def start(i):
        peers=[]
        if i==1: peers=[f"127.0.0.1:{ports[0]}",f"127.0.0.1:{ports[2]}"]
        if i==2: peers=[f"127.0.0.1:{ports[1]}"]
        f=open(root/f"{i}-{len(logs)}.log","w");logs.append(f)
        executable = SEED_BINARY if i == 0 else BINARY
        flags = EXTRA if executable == BINARY else []
        p=subprocess.Popen([executable,"peer","--file",str(files[i]),"--manifest",str(manifest),
          "--token-file",str(token),"--listen",f"127.0.0.1:{ports[i]}","--peers",",".join(peers),
          "--download-mib","8","--upload-mib","8","--lifetime","60s","--stall-timeout","20s"]+flags,
          stdout=f,stderr=f)
        procs.append(p);return p
    try:
        a=start(0);b=start(1);c=start(2)
        time.sleep(2)
        check(b.poll() is None,"receiver exited unexpectedly")
        b.terminate();b.wait(timeout=5)
        parts=list(files[1].parent.glob("*.part"));check(len(parts)==1,"missing resume file")
        with parts[0].open("r+b") as f: f.seek(0);f.write(b"CORRUPTED")
        b=start(1)
        deadline=time.monotonic()+35
        while time.monotonic()<deadline and not (files[1].exists() and files[2].exists()):
            check(all(p.poll() is None for p in [a,b,c]),"peer died")
            time.sleep(.25)
        expected=hashlib.sha256(source.read_bytes()).hexdigest()
        for f in files:
            check(f.exists(),"incomplete transfer")
            check(hashlib.sha256(f.read_bytes()).hexdigest()==expected,"final hash mismatch")
        import urllib.request
        def status(i):
            req=urllib.request.Request(f"http://127.0.0.1:{ports[i]}/status",headers={"Authorization":"Bearer "+token.read_text()})
            with urllib.request.urlopen(req) as response: return json.load(response)
        for i in [1,2]:
            st=status(i)
            if "Performance" in st:
                check(st["Performance"]["Stage"]=="complete","missing completed phase")
                check(st["Performance"]["FinalHashSeconds"]>0,"missing full hash timing")
                if "--pipeline-verify" in EXTRA:
                    check(st["Performance"]["PipelineVerify"],"pipeline flag not propagated")
                    check(st["Performance"]["FileHashBytes"]==source.stat().st_size,"whole-file pipeline did not hash every byte")
                if "--adaptive" in EXTRA: check(st["Performance"]["Adaptive"],"adaptive flag not propagated")
        check(status(1)["Uploaded"]>0,"middle node did not forward")
        check(status(2)["From"].get(f"127.0.0.1:{ports[1]}",0)==source.stat().st_size,"chain bypassed")
        print("PASS: interrupted receiver resumed, corrupted block repaired, partial peer forwarded, all SHA256 match")
    except BaseException:
        for f in logs: f.flush()
        for path in root.glob("*.log"): print(path.name, path.read_text())
        raise
    finally:
        for p in procs:
            if p.poll() is None: p.terminate()
        for p in procs:
            try: p.wait(timeout=5)
            except subprocess.TimeoutExpired: p.kill();p.wait()
        for f in logs:f.close()
        for path in root.glob("*.log"):
            content=path.read_text()
            check("WARNING: DATA RACE" not in content,content)

