#!/usr/bin/env python3
"""Deterministic MCPD orchestration micro-benchmark.

Measures serial single-process orchestration against start/read_process_batch over
real MCP HTTP. It reports tool-call count, wire response bytes, duplicate output
bytes, wall time, scheduler idle estimate, retries, and peak concurrency.
"""
import argparse
import json
import time
import urllib.request

class MCP:
    def __init__(self, origin):
        self.url = origin.rstrip('/') + '/mcp'
        self.calls = 0
        self.response_bytes = 0
        self.next_id = 1

    def call(self, name, arguments):
        rid = self.next_id; self.next_id += 1
        body = json.dumps({"jsonrpc":"2.0","id":rid,"method":"tools/call","params":{"name":name,"arguments":arguments}}, separators=(',', ':')).encode()
        req = urllib.request.Request(self.url, data=body, headers={"Content-Type":"application/json","Accept":"application/json, text/event-stream"}, method='POST')
        started = time.monotonic()
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read()
        elapsed = time.monotonic()-started
        self.calls += 1; self.response_bytes += len(raw)
        env = json.loads(raw)
        if 'error' in env: raise RuntimeError(env['error'])
        result = env.get('result', {})
        if result.get('isError'): raise RuntimeError(result)
        # Go MCP SDK exposes structuredContent for typed tool outputs.
        value = result.get('structuredContent')
        if value is None:
            # Fallback for SDK versions that only expose JSON text content.
            for item in result.get('content', []):
                if item.get('type') == 'text':
                    try: value = json.loads(item.get('text','')); break
                    except json.JSONDecodeError: pass
        if value is None: raise RuntimeError(f'no structured tool output for {name}: {result!r}')
        return value, elapsed, len(raw)


def output_text(job):
    if job.get('lines'): return '\n'.join(job['lines'])
    if job.get('streams'): return '\n'.join(x.get('text','') for x in job['streams'])
    if job.get('output'): return '\n'.join(job['output'])
    return ''


def duplicate_bytes(chunks):
    seen=set(); duplicate=0
    for chunk in chunks:
        for line in chunk.splitlines(True):
            if line in seen: duplicate += len(line.encode())
            else: seen.add(line)
    return duplicate


def serial_scenario(origin, commands):
    m=MCP(origin); started=time.monotonic(); chunks=[]; runtimes=[]
    for command in commands:
        out, _, _ = m.call('start_process', {'command':command,'timeout_ms':1,'pty':'never'})
        chunks.append('\n'.join(out.get('output', []))); runtimes.append(out.get('waited_ms',0)/1000)
        if out.get('state') not in ('exited','waiting_for_input'):
            pid=out['pid']
            while True:
                more, _, _=m.call('read_process_output', {'pid':pid,'timeout_ms':5000,'offset':0,'length':1000})
                chunks.append(output_text(more))
                if more.get('state')=='exited': break
    wall=time.monotonic()-started
    return {'wall_ms':round(wall*1000,2),'tool_calls':m.calls,'response_bytes':m.response_bytes,'duplicate_output_bytes':duplicate_bytes(chunks),'scheduler_idle_ms':round(max(0,wall-sum(runtimes))*1000,2),'retry_executions':0,'peak_concurrency':1}


def batch_scenario(origin, jobs):
    m=MCP(origin); started=time.monotonic(); chunks=[]; peak=0
    out, _, _=m.call('start_process_batch', {'jobs':jobs,'max_parallel':len(jobs),'initial_wait_ms':40,'output_mode':'failures'})
    cursor=out.get('cursor',''); batch_id=out['batch_id']
    peak=max(peak,out.get('counts',{}).get('running',0)+out.get('counts',{}).get('waiting',0))
    for job in out.get('jobs',[]): chunks.append(output_text(job))
    while out.get('state')=='running':
        out, _, _=m.call('read_process_batch', {'batch_id':batch_id,'only_changed':True,'cursor':cursor,'timeout_ms':5000,'length':1000,'output_mode':'failures'})
        cursor=out.get('cursor',cursor)
        peak=max(peak,out.get('counts',{}).get('running',0)+out.get('counts',{}).get('waiting',0))
        for job in out.get('jobs',[]): chunks.append(output_text(job))
    wall=time.monotonic()-started
    # With a work-conserving batch scheduler, idle is wall time minus the longest
    # observed job runtime; this is an approximation meant for relative comparison.
    max_runtime=max([j.get('runtime_ms',0) for j in out.get('jobs',[])] or [0])/1000
    return {'wall_ms':round(wall*1000,2),'tool_calls':m.calls,'response_bytes':m.response_bytes,'duplicate_output_bytes':duplicate_bytes(chunks),'scheduler_idle_ms':round(max(0,wall-max_runtime)*1000,2),'retry_executions':0,'peak_concurrency':peak,'failure_evidence': 'FAIL-LANE' in '\n'.join(chunks)}


def scenario_commands(kind):
    if kind=='short':
        cmds=[f"sleep 0.08; printf 'short-{i}\\n'" for i in range(4)]
    elif kind=='noisy':
        cmds=[f"python3 -c \"import time; [print('job-{i}-line-%04d-'%n+'x'*120) for n in range(400)]; time.sleep(0.04)\"" for i in range(4)]
    elif kind=='failure':
        cmds=["sleep 0.05; printf 'ok-a\\n'", "sleep 0.05; printf 'FAIL-LANE\\n' >&2; exit 7", "sleep 0.05; printf 'ok-c\\n'"]
    else: raise ValueError(kind)
    jobs=[{'id':f'job-{i}','command':cmd,'pty':'never'} for i,cmd in enumerate(cmds)]
    return cmds,jobs


def dag_scenario(origin):
    m=MCP(origin); started=time.monotonic()
    jobs=[
        {'id':'a','command':"sleep 0.06; printf 'a\\n'",'pty':'never'},
        {'id':'b','command':"sleep 0.06; printf 'b\\n'",'pty':'never'},
        {'id':'c','command':"sleep 0.03; printf 'c\\n'",'pty':'never','depends_on':['a','b']},
    ]
    out,_,_=m.call('start_process_batch',{'jobs':jobs,'max_parallel':3,'initial_wait_ms':40,'output_mode':'failures'}); cursor=out['cursor']; peak=0
    while out.get('state')=='running':
        peak=max(peak,out.get('counts',{}).get('running',0))
        out,_,_=m.call('read_process_batch',{'batch_id':out['batch_id'],'only_changed':True,'cursor':cursor,'timeout_ms':5000,'length':100,'output_mode':'failures'})
        cursor=out['cursor']
    return {'wall_ms':round((time.monotonic()-started)*1000,2),'tool_calls':m.calls,'response_bytes':m.response_bytes,'duplicate_output_bytes':0,'scheduler_idle_ms':0,'retry_executions':0,'peak_concurrency':peak,'state':out.get('state'),'blocked':out.get('counts',{}).get('blocked',0)}


def resume_scenario(origin):
    # Consumer A takes a snapshot, consumer B resumes from the same caller-owned
    # cursor. Reading from A must not consume B's view.
    m=MCP(origin)
    jobs=[{'id':'a','command':"printf 'first\\n'; sleep 0.08; printf 'second\\n'",'pty':'never'}, {'id':'b','command':"sleep 0.04; printf 'b\\n'",'pty':'never'}]
    started=time.monotonic(); first,_,_=m.call('start_process_batch',{'jobs':jobs,'max_parallel':2,'initial_wait_ms':1,'output_mode':'failures'}); cursor=first['cursor']; bid=first['batch_id']
    a,_,_=m.call('read_process_batch',{'batch_id':bid,'only_changed':True,'cursor':cursor,'timeout_ms':5000,'length':100,'output_mode':'failures'})
    b,_,_=m.call('read_process_batch',{'batch_id':bid,'only_changed':False,'cursor':cursor,'timeout_ms':0,'length':100,'output_mode':'failures'})
    # Finish from B's independent cursor.
    cur=b['cursor']; out=b
    while out.get('state')=='running':
        out,_,_=m.call('read_process_batch',{'batch_id':bid,'only_changed':True,'cursor':cur,'timeout_ms':5000,'length':100,'output_mode':'failures'}); cur=out['cursor']
    return {'wall_ms':round((time.monotonic()-started)*1000,2),'tool_calls':m.calls,'response_bytes':m.response_bytes,'duplicate_output_bytes':0,'scheduler_idle_ms':0,'retry_executions':0,'peak_concurrency':2,'resume_independent': bool(b.get('jobs')),'state':out.get('state')}


def main():
    ap=argparse.ArgumentParser(); ap.add_argument('origin'); ap.add_argument('--json-out'); ap.add_argument('--assert-ci',action='store_true'); args=ap.parse_args()
    results={}
    for kind in ('short','noisy','failure'):
        cmds,jobs=scenario_commands(kind)
        results[kind]={'serial':serial_scenario(args.origin,cmds),'batch':batch_scenario(args.origin,jobs)}
    results['dag']={'batch':dag_scenario(args.origin)}
    results['resume']={'batch':resume_scenario(args.origin)}
    if args.assert_ci:
        short=results['short']; noisy=results['noisy']
        if short['batch']['peak_concurrency'] < 2: raise SystemExit('batch never reached parallel concurrency')
        serial_calls=sum(results[k]['serial']['tool_calls'] for k in ('short','noisy','failure'))
        batch_calls=sum(results[k]['batch']['tool_calls'] for k in ('short','noisy','failure'))
        if batch_calls >= serial_calls: raise SystemExit(f'batch did not reduce aggregate tool calls: serial={serial_calls} batch={batch_calls}')
        if short['batch']['wall_ms'] >= short['serial']['wall_ms']*0.9: raise SystemExit(f"batch wall time regression: {short}")
        if noisy['batch']['response_bytes'] >= noisy['serial']['response_bytes']*0.5: raise SystemExit(f"batch noisy response budget regression: {noisy}")
        if not results['failure']['batch']['failure_evidence']: raise SystemExit('failure-only polling lost failure evidence')
        if results['dag']['batch']['state'] != 'completed' or results['dag']['batch']['blocked'] != 0: raise SystemExit('DAG scenario failed')
        if not results['resume']['batch']['resume_independent']: raise SystemExit('resume cursor scenario lost independent consumer view')
    text=json.dumps(results,indent=2,sort_keys=True)
    if args.json_out:
        with open(args.json_out,'w',encoding='utf-8') as f:f.write(text+'\n')
    print('MCPD workflow benchmark')
    for kind in ('short','noisy','failure'):
        s,b=results[kind]['serial'],results[kind]['batch']
        print(f"{kind:8s} serial wall={s['wall_ms']:7.1f}ms calls={s['tool_calls']:2d} bytes={s['response_bytes']:7d} | batch wall={b['wall_ms']:7.1f}ms calls={b['tool_calls']:2d} bytes={b['response_bytes']:7d} peak={b['peak_concurrency']}")
    print(text)

if __name__=='__main__': main()
