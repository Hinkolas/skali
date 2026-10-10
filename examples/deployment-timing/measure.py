#!/usr/bin/env python3
"""Collect skali/Kubernetes timing evidence while deploying this example.

Requires Python 3, Ruby (YAML support), SSH, Docker, and a logged-in skali
remote. Reads credentials in memory; never writes them to the results.
"""
import argparse
import datetime
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

KUBE_COLLECTOR = r'''
import codecs,datetime,json,os,signal,subprocess,sys,threading,time
lock=threading.Lock()
children=[]
environment="__TIMING_ENVIRONMENT__"
def emit(value):
    value['received_at']=datetime.datetime.now(datetime.timezone.utc).isoformat()
    with lock: print(json.dumps(value,separators=(',',':')),flush=True)
def project(obj):
    meta=obj.get('metadata',{})
    out={'kind':obj.get('kind'),'metadata':{k:meta.get(k) for k in ('name','namespace','uid','creationTimestamp','resourceVersion','generation','labels','deletionTimestamp')},'status':obj.get('status',{})}
    spec=obj.get('spec',{})
    if obj.get('kind')=='Pod':
        out['spec']={'nodeName':spec.get('nodeName'),'containers':[{'name':c['name'],'image':c['image']} for c in spec.get('containers',[])],'initContainers':[{'name':c['name'],'image':c['image']} for c in spec.get('initContainers',[])]}
    elif obj.get('kind')=='Service': out['spec']={'selector':spec.get('selector')}
    elif obj.get('kind')=='Event':
        for k in ('reason','message','type','firstTimestamp','lastTimestamp','eventTime','count','involvedObject','series'): out[k]=obj.get(k)
    return out
def watch(resource,selector=True,namespace=None):
    args=['k3s','kubectl','get',resource,'--watch','--output-watch-events','-o','json']
    if resource == 'databases.postgresql.cnpg.io':
        args+=['-n','skali-platform','-l','skali.dev/environment='+environment]
    elif resource == 'events': args+=['-A']
    else:
        args+=['-n',namespace] if namespace else ['-n','skali-'+environment]
        if selector: args+=['-l','skali.dev/project=deployment-timing']
    p=subprocess.Popen(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    children.append(p)
    emit({'source':'watch_started','resource':resource})
    decoder=json.JSONDecoder(); utf8=codecs.getincrementaldecoder('utf-8')(); buffer=''
    while True:
        chunk=os.read(p.stdout.fileno(),65536)
        if not chunk: break
        buffer+=utf8.decode(chunk)
        while buffer.strip():
            buffer=buffer.lstrip()
            try: event,end=decoder.raw_decode(buffer)
            except json.JSONDecodeError: break
            buffer=buffer[end:]
            obj=event.get('object',{})
            if resource=='events' and 'deployment-timing' not in json.dumps(obj): continue
            emit({'source':'kubernetes','event_type':event.get('type'),'resource':resource,'object':project(obj)})
    emit({'source':'watch_stopped','resource':resource,'exit_code':p.wait(),'error':p.stderr.read().decode()})
def stop(*args):
    for p in children: p.terminate()
    sys.exit(0)
signal.signal(signal.SIGTERM,stop)
signal.signal(signal.SIGINT,stop)
signal.signal(signal.SIGHUP,stop)
emit({'source':'collector_ready','pid':os.getpid()})
for resource in ('pods','deployments','jobs','services','databases.postgresql.cnpg.io','certificates.cert-manager.io','certificaterequests.cert-manager.io','orders.acme.cert-manager.io','challenges.acme.cert-manager.io','endpointslices.discovery.k8s.io','ingressroutes.traefik.io'):
    threading.Thread(target=watch,args=(resource,resource not in ('certificaterequests.cert-manager.io','orders.acme.cert-manager.io','challenges.acme.cert-manager.io')),daemon=True).start()
threading.Thread(target=watch,args=('clusters.postgresql.cnpg.io',False,'skali-platform'),daemon=True).start()
threading.Thread(target=watch,args=('events',False),daemon=True).start()
deadline=time.monotonic()+1800
while time.monotonic()<deadline: time.sleep(1)
stop()
'''


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


class API:
    def __init__(self, remote):
        config = Path(os.environ.get('XDG_CONFIG_HOME', Path.home() / '.config')) / 'skali/config.yaml'
        parsed = json.loads(subprocess.check_output(['ruby', '-ryaml', '-rjson', '-e', 'puts JSON.generate(YAML.load_file(ARGV[0]))', str(config)]))
        selected = parsed['remotes'][remote]
        self.master = selected['master'].rstrip('/')
        # The daemon requires the installed CLI's own release, which the
        # remote's recorded skalid version only matches until either moves.
        cli = subprocess.check_output(['skali', 'version'], text=True).split()[2]
        self.headers = {'Authorization': 'Bearer ' + selected['token'], 'Skali-Client-Version': cli}

    def request(self, path):
        return urllib.request.Request(self.master + path, headers=self.headers)

    def get(self, path):
        with urllib.request.urlopen(self.request(path), timeout=15) as response:
            return json.load(response)

    def post(self, path, value):
        request = self.request(path)
        request.method = 'POST'
        request.data = json.dumps(value).encode()
        request.add_header('Content-Type', 'application/json')
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)


def redeploy(remote, environment):
    api = API(remote)
    request = {'redeploy': True, 'force': True}
    plan = api.post('/v1/environments/' + environment + '/plan', {'redeploy': True})
    if any(c.get('destructive') for c in plan.get('plan', {}).get('changes', [])):
        raise RuntimeError('Refusing an unexpected destructive redeploy')
    opened = api.post('/v1/environments/' + environment + '/deployments', request)
    deployment = opened['deployment']
    if any(action['action'] != 'reuse' for action in opened['actions']):
        raise RuntimeError('Expected artifact reuse')
    print(json.dumps({'deployment_id': deployment['id'], 'run_id': deployment['run_id']}), flush=True)
    api.post('/v1/deployments/' + deployment['id'] + '/complete', {})
    print(json.dumps({'submitted': True}), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ssh', required=True)
    parser.add_argument('--remote', required=True)
    parser.add_argument('--environment', default='baseline')
    parser.add_argument('--domain', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--force', action='store_true')
    parser.add_argument('--redeploy', action='store_true', help='Force-redeploy the active revision through the API, reusing its image and stored values')
    parser.add_argument('--duration', type=int, default=1200)
    args = parser.parse_args()
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=False, mode=0o700)
    api = API(args.remote)
    projects = api.get('/v1/projects')['projects']
    project = next(p for p in projects if p['name'] == 'deployment-timing')
    envs = api.get('/v1/projects/' + project['id'] + '/environments')['environments']
    environment_id = next(e['id'] for e in envs if e['name'] == args.environment)
    stop = threading.Event()
    lock = threading.Lock()
    evidence = (output / 'timeline.jsonl').open('w')
    def emit(source, data):
        with lock:
            evidence.write(json.dumps({'received_at': now(), 'source': source, 'data': data}, separators=(',', ':')) + '\n')
            evidence.flush()
    started = now()
    emit('experiment_start', {'environment': args.environment, 'domain': args.domain, 'force': args.force, 'redeploy': args.redeploy})
    (output / 'observation-before.json').write_text(json.dumps(api.get('/v1/system/observation'), indent=2))
    kube_file = (output / 'kubernetes.jsonl').open('w')
    kube_errors = (output / 'kubernetes.stderr').open('w')
    ssh = subprocess.Popen(['ssh', '-o', 'BatchMode=yes', args.ssh, 'python3 -u -'], stdin=subprocess.PIPE, stdout=kube_file, stderr=kube_errors, text=True)
    ssh.stdin.write(KUBE_COLLECTOR.replace("__TIMING_ENVIRONMENT__", environment_id))
    ssh.stdin.close()
    def sse(environment):
        while not stop.is_set():
            try:
                with urllib.request.urlopen(api.request('/v1/environments/' + environment + '/status/stream'), timeout=30) as response:
                    emit('status_stream_connected', {'environment_id': environment})
                    for raw in response:
                        if stop.is_set(): return
                        line = raw.decode().strip()
                        if line.startswith('data: '): emit('status', json.loads(line[6:]))
            except Exception as error:
                if not stop.is_set(): emit('status_stream_error', {'type': type(error).__name__})
            stop.wait(1)
    def probe():
        previous = None
        while not stop.is_set():
            begin = time.monotonic()
            try:
                with urllib.request.urlopen('https://' + args.domain + '/', timeout=2) as response:
                    value = {'status': response.status, 'body': json.load(response)}
            except Exception as error:
                value = {'error': type(error).__name__}
            value_key = json.dumps({k: v for k,v in value.items() if k != 'body'})
            if 'body' in value: value_key += str(value['body'].get('version')) + str(value['body'].get('started_at'))
            if value_key != previous:
                emit('https_probe', dict(value, duration_ms=round((time.monotonic()-begin)*1000, 3)))
                previous = value_key
            stop.wait(0.5)
    threading.Thread(target=probe, daemon=True).start()
    # Give the SSH watches time to attach before submitting the deployment.
    time.sleep(3)
    if ssh.poll() is not None: raise RuntimeError('SSH collector failed; see kubernetes.stderr')
    command = ['skali', 'deploy', '--remote', args.remote, '--environment', args.environment, '--env-file', '.env', '--platform', 'linux/arm64', '--yes', '--detach']
    if args.force: command.append('--force')
    if args.redeploy:
        command = [sys.executable, '-B', '-c', 'from measure import redeploy; import sys; redeploy(*sys.argv[1:])', args.remote, environment_id]
    cli_file = (output / 'deploy.log').open('w')
    emit('redeploy_started' if args.redeploy else 'cli_started', {'command': command})
    deployment = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    def cli_output():
        for line in deployment.stdout:
            cli_file.write(line); cli_file.flush()
            emit('cli_line', {'text': line.rstrip()})
        emit('cli_finished', {'exit_code': deployment.wait()})
    reader = threading.Thread(target=cli_output, daemon=True)
    reader.start()
    env_id = None
    trees = {}
    previous = {}
    settled_at = None
    observation_at = 0
    deadline = time.monotonic() + args.duration
    try:
        while time.monotonic() < deadline:
            if time.monotonic() >= observation_at:
                emit('observation', api.get('/v1/system/observation'))
                observation_at = time.monotonic() + 10
            if not env_id:
                projects = api.get('/v1/projects')['projects']
                project = next((p for p in projects if p['name'] == 'deployment-timing'), None)
                if project:
                    envs = api.get('/v1/projects/' + project['id'] + '/environments')['environments']
                    env = next((e for e in envs if e['name'] == args.environment), None)
                    if env:
                        env_id = env['id']
                        emit('environment_found', {'environment_id': env_id, 'project_id': project['id']})
                        threading.Thread(target=sse, args=(env_id,), daemon=True).start()
            if env_id:
                runs = api.get('/v1/environments/' + env_id + '/runs')['runs']
                for run in runs:
                    if run['created_at'] < started and run.get('finished_at'): continue
                    tree = api.get('/v1/runs/' + run['id'])
                    trees[run['id']] = tree
                    encoded = json.dumps(tree, sort_keys=True)
                    if encoded != previous.get(run['id']):
                        emit('run_tree', tree); previous[run['id']] = encoded
                terminal = any(t['run']['kind'] == 'deployment' and t['run']['status'] in ('succeeded','failed','cancelled') and t['run']['created_at'] >= started for t in trees.values())
                if terminal and deployment.poll() is not None:
                    if settled_at is None: settled_at = time.monotonic()
                    if time.monotonic() - settled_at > 12: break
            if deployment.poll() not in (None, 0): break
            stop.wait(2)
        emit('collection_finished', {'cli_exit_code': deployment.poll(), 'environment_id': env_id})
        (output / 'runs.json').write_text(json.dumps(list(trees.values()), indent=2))
        def steps(items):
            for item in items:
                yield item
                yield from steps(item.get('children') or [])
        for tree in trees.values():
            for step in steps(tree.get('steps') or []):
                logs = api.get('/v1/steps/' + step['id'] + '/logs')
                emit('step_logs', {'run_id': tree['run']['id'], 'key': step['key'], 'logs': logs})
        (output / 'observation-after.json').write_text(json.dumps(api.get('/v1/system/observation'), indent=2))
        commands = [(['k3s','kubectl','logs','-n','skali-system','deployment/skalid','-c','skalid','--timestamps','--since-time='+started], 'skalid.log')]
        if env_id:
            namespace = 'skali-' + env_id
            pods = json.loads(subprocess.check_output(['ssh','-o','BatchMode=yes',args.ssh,'k3s kubectl get pods -n '+namespace+' -o json']))
            # Store metadata/status only, never environment variables or Secrets.
            (output / 'pods-final.json').write_text(json.dumps([{'metadata':{k:p['metadata'].get(k) for k in ('name','namespace','creationTimestamp','labels')},'status':p.get('status')} for p in pods['items']], indent=2))
            for pod in pods['items']:
                for container in pod['spec'].get('containers',[]) + pod['spec'].get('initContainers',[]):
                    commands.append((['k3s','kubectl','logs','-n',namespace,pod['metadata']['name'],'-c',container['name'],'--timestamps'], pod['metadata']['name']+'-'+container['name']+'.log'))
        import shlex
        for command, filename in commands:
            with (output / filename).open('w') as log:
                subprocess.run(['ssh','-o','BatchMode=yes',args.ssh,shlex.join(command)], stdout=log, stderr=subprocess.STDOUT, timeout=30)
    finally:
        stop.set()
        # Stop the identified remote parent so its handler closes its
        # kubectl children before the SSH transport is closed.
        for line in (output / 'kubernetes.jsonl').read_text().splitlines():
            try: record = json.loads(line)
            except json.JSONDecodeError: continue
            if record.get('source') == 'collector_ready':
                pid = int(record['pid'])
                subprocess.run(['ssh','-o','BatchMode=yes',args.ssh,'kill -TERM '+str(pid)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
                break
        ssh.terminate()
        try: ssh.wait(timeout=5)
        except subprocess.TimeoutExpired: ssh.kill()
        kube_file.close(); kube_errors.close()
        emit('experiment_end', {})
        print(json.dumps({'output':str(output),'environment_id':env_id,'runs':[{'id':t['run']['id'],'status':t['run']['status']} for t in trees.values()]}))
    # --detach hands work to the server; hitting the collection deadline
    # never cancels or deletes a deployment.
    measured = [t['run'] for t in trees.values() if t['run']['kind'] == 'deployment' and t['run']['created_at'] >= started]
    return 0 if deployment.poll() == 0 and any(r['status'] == 'succeeded' for r in measured) else 1


if __name__ == '__main__':
    raise SystemExit(main())
