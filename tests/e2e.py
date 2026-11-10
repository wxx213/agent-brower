#!/usr/bin/env python3
"""Real browser acceptance tests. Uses only synthetic local pages and private profiles."""
import argparse
import base64
import concurrent.futures
import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import urllib.request

HTML = b'''<!doctype html><meta charset=utf-8><title>Agent Web Lab</title>
<label>Name <input id=name></label><button id=save onclick="document.querySelector('#result').textContent=document.querySelector('#name').value">Save</button>
<p id=result>empty</p><button id=popup onclick="window.open('/child')">Open popup</button>
<form action=/login method=post><label>Password <input id=password name=password type=password></label><button id=login>Log in</button></form>
<iframe id=frame title="embedded" src=/frame></iframe>
<script>window.counter=0;</script>'''


class Fixture(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        if self.path == '/who':
            body = json.dumps({'cookie': self.headers.get('Cookie', '')}).encode()
            content = 'application/json'
        elif self.path == '/frame':
            body = b'<label>Note<input id=note></label><button onclick="document.body.dataset.saved=document.querySelector(\'#note\').value">Save note</button>'
            content = 'text/html'
        elif self.path == '/child':
            body = b'<title>Child</title><h1>Popup child</h1>'
            content = 'text/html'
        else:
            body, content = HTML, 'text/html'
        self.send_response(200); self.send_header('Content-Type', content)
        self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', '0')))
        self.send_response(303); self.send_header('Set-Cookie', 'session=synthetic-login; HttpOnly; SameSite=Lax; Path=/')
        self.send_header('Location', '/'); self.end_headers()


def run(binary, browser=None):
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f'http://127.0.0.1:{server.server_port}'
    checks = []
    with tempfile.TemporaryDirectory(prefix='agent-web-e2e-') as tmp:
        env = {**os.environ, 'AGENT_WEB_HOME': str(Path(tmp)/'data')}
        env.pop('AGENT_WEB_URL', None); env.pop('AGENT_WEB_TOKEN', None)
        if browser: env['AGENT_WEB_BROWSER'] = browser
        else: env.pop('AGENT_WEB_BROWSER', None)
        def call(op, *, error=None, **kw):
            p = subprocess.run([binary, 'call'], input=json.dumps({'op':op, **kw}), text=True, capture_output=True, env=env, timeout=110)
            try: data = json.loads(p.stdout)
            except Exception: raise AssertionError(f'{op}: {p.returncode} {p.stdout} {p.stderr}')
            if error:
                assert not data['ok'] and data['error']['code']==error, data
                return data
            assert data['ok'], data
            assert p.returncode == 0, p.stderr
            return data.get('result')
        def stop():
            result=subprocess.run([binary,'stop'],env=env,capture_output=True,timeout=40)
            if result.returncode==0:
                assert not (Path(env['AGENT_WEB_HOME'])/'daemon.json').exists(), 'stop returned before state was saved and daemon closed'
        try:
            # A concurrent first invocation must start one daemon only.
            with concurrent.futures.ThreadPoolExecutor(4) as pool:
                results=list(pool.map(lambda _:call('ping'),range(4)))
            assert len({r['pid'] for r in results})==1
            checks.append('concurrent autostart: one daemon')
            t=call('start',account='alice',url=url);task=t['task'];pid=t['browser_pid']
            call('start',account='alice',error='ACCOUNT_BUSY')
            snap=call('observe',task=task)
            ref=next(n['ref'] for n in snap['nodes'] if n['role']=='textbox' and 'Name' in n['name'])
            call('act',task=task,actions=[{'type':'fill','ref':ref,'text':'跨轮表单'}, {'type':'click','selector':'#save'}])
            assert call('eval',task=task,script='() => document.querySelector("#result").textContent')=='跨轮表单'
            checks.append('semantic refs, batch fill/click, persistent task')
            call('act',task=task,frame='#frame',actions=[{'type':'fill','selector':'#note','text':'iframe-ok'},{'type':'click','selector':'button'}])
            assert call('eval',task=task,frame='#frame',script='() => document.body.dataset.saved')=='iframe-ok'
            assert call('observe',task=task,frame='#frame')['nodes']
            # Change iframe origin and verify cross-origin control as well.
            call('eval',task=task,script=f'() => document.querySelector("#frame").src = "{url.replace("127.0.0.1","localhost")}/frame"')
            call('act',task=task,frame='#frame',actions=[{'type':'fill','selector':'#note','text':'cross-origin'}])
            assert call('eval',task=task,frame='#frame',script='() => document.querySelector("#note").value')=='cross-origin'
            checks.append('same-origin and cross-origin iframe')
            snap=call('observe',task=task)
            ref=next(n['ref'] for n in snap['nodes'] if n['role']=='button')
            call('act',task=task,actions=[{'type':'navigate','url':url+'/new'}])
            call('act',task=task,actions=[{'type':'click','ref':ref}],error='STALE_REF')
            partial=call('act',task=task,actions=[{'type':'fill','selector':'#name','text':'committed'},{'type':'bogus'}],error='INVALID_ACTION')
            assert partial['result']['completed']==1
            checks.append('stale refs and partial failure accounting')
            call('act',task=task,actions=[{'type':'click','selector':'#popup'}])
            tabs=call('tabs',task=task);assert len(tabs)==2,tabs
            child=next(k for k in tabs if k!='p1');call('close_page',task=task,page=child)
            assert len(call('tabs',task=task))==1
            assert base64.b64decode(call('screenshot',task=task)['base64']).startswith(b'\x89PNG')
            checks.append('popup ownership and PNG screenshot')
            positions=call('eval',task=task,script='() => Object.fromEntries(["password","login"].map(id=>{const r=document.getElementById(id).getBoundingClientRect();return [id,{x:r.x+r.width/2,y:r.y+r.height/2}]}))')
            h=call('handoff',task=task,reason='Synthetic login')
            call('act',task=task,actions=[{'type':'click','selector':'#save'}],error='NEEDS_HUMAN')
            state=json.loads((Path(env['AGENT_WEB_HOME'])/'daemon.json').read_text())
            base=state['url'];token=h['url'].split('#')[1]
            def human(path,body=None,tok=token):
                req=urllib.request.Request(base+f'/human/{task}/'+path,data=json.dumps(body).encode() if body else None,headers={'Authorization':'Bearer '+tok,**({'Content-Type':'application/json'} if body else {})})
                with urllib.request.urlopen(req,timeout=20) as r:return r.read()
            assert human('frame?page=p1').startswith(b'\xff\xd8')
            try: human('tabs',tok='wrong')
            except urllib.error.HTTPError as e: assert e.code==401
            else: raise AssertionError('invalid handoff token accepted')
            # Complete a real form POST through the same input channel as the user UI.
            for typ in ['down','up']: human('input',{'type':typ,'page':'p1','button':'left',**positions['password']})
            human('input',{'type':'text','text':'synthetic','page':'p1'})
            for typ in ['down','up']: human('input',{'type':typ,'page':'p1','button':'left',**positions['login']})
            tabs_data=json.loads(human('tabs'));assert tabs_data['ok']
            human('input',{'type':'done','page':'p1'})
            assert call('status',task=task)['state']=='running'
            try: human('frame?page=p1')
            except urllib.error.HTTPError as e: assert e.code==401
            else: raise AssertionError('handoff token not revoked')
            call('eval',task=task,script='() => localStorage.setItem("remember","alice")')
            call('eval',task=task,script='''async () => { const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('lab',1);r.onupgradeneeded=()=>r.result.createObjectStore('state');r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error)}); await new Promise((resolve,reject)=>{const tx=db.transaction('state','readwrite');tx.objectStore('state').put('durable','token');tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error)}); db.close();return true }''')
            assert 'synthetic-login' in call('eval',task=task,script='async () => (await (await fetch("/who")).json()).cookie')
            checks.append('handoff pauses automation, image/input channel, revocation')
            # Exercise the actual handoff HTML/JS in another browser, including
            # scaled pointer coordinates, the text box, image polling and Done.
            positions=call('eval',task=task,script='() => Object.fromEntries(["name","save"].map(id=>{const r=document.getElementById(id).getBoundingClientRect();return [id,{x:r.x+r.width/2,y:r.y+r.height/2}]}))')
            h=call('handoff',task=task,reason='Test the real handoff page')
            viewer=call('start',account='handoff-viewer',url=h['url'])['task']
            for _ in range(30):
                ready=call('eval',task=viewer,script='() => document.querySelector("#screen").naturalWidth > 0')
                if ready: break
                time.sleep(.2)
            assert ready, 'handoff image did not load'
            rect=call('eval',task=viewer,script='() => {const r=document.querySelector("#screen").getBoundingClientRect();return {x:r.x,y:r.y,w:r.width,h:r.height}}')
            def screen_click(position):
                call('act',task=viewer,actions=[{'type':'click_xy','x':rect['x']+position['x']*rect['w']/1280,'y':rect['y']+position['y']*rect['h']/800}])
            screen_click(positions['name'])
            call('act',task=viewer,actions=[{'type':'fill','selector':'#text','text':'ui-handoff'},{'type':'click','selector':'#send'}])
            screen_click(positions['save'])
            call('act',task=viewer,actions=[{'type':'click','selector':'#done'}])
            for _ in range(30):
                if call('status',task=task)['state']=='running': break
                time.sleep(.2)
            ui_result=call('eval',task=task,script='() => ({result:document.querySelector("#result").textContent,name:document.querySelector("#name").value})')
            assert ui_result['result']=='ui-handoff', ui_result
            call('finish',task=viewer)
            checks.append('rendered handoff UI: live image, scaled mouse, text input, return control')
            bob=call('start',account='bob',url=url)
            assert call('eval',task=bob['task'],script='() => localStorage.getItem("remember")') is None
            assert call('eval',task=bob['task'],script='async () => (await (await fetch("/who")).json()).cookie')==''
            call('finish',task=bob['task']);call('finish',task=task)
            t=call('start',account='alice',url=url);task=t['task'];assert t['browser_pid']==pid
            assert call('eval',task=task,script='() => localStorage.getItem("remember")')=='alice'
            call('finish',task=task);stop()
            t=call('start',account='alice',url=url);task=t['task'];assert t['browser_pid']!=pid
            assert 'synthetic-login' in call('eval',task=task,script='async () => (await (await fetch("/who")).json()).cookie')
            assert call('eval',task=task,script='() => localStorage.getItem("remember")')=='alice'
            assert call('eval',task=task,script='''async () => { const db=await new Promise(r=>{const q=indexedDB.open('lab',1);q.onsuccess=()=>r(q.result)}); const v=await new Promise(r=>{const q=db.transaction('state').objectStore('state').get('token');q.onsuccess=()=>r(q.result)});db.close();return v }''')=='durable'
            call('finish',task=task)
            checks.append('account isolation, same-process reuse, restart cookies/localStorage/IndexedDB')
            # Installing creates a self-contained skill folder and never overwrites it.
            dest=Path(tmp)/'skills'/'agent-web'
            p=subprocess.run([binary,'install','--dir',str(dest)],capture_output=True,text=True,env=env)
            assert p.returncode==0,p.stdout
            assert (dest/'SKILL.md').exists()
            assert subprocess.run([str(dest/'bin/agent-web'),'version'],capture_output=True,env=env).returncode==0
            assert subprocess.run([binary,'install','--dir',str(dest)],capture_output=True,env=env).returncode!=0
            stop()
            binary=str(dest/'bin/agent-web')
            env['AGENT_WEB_HOME']=str(Path(tmp)/'installed-data')
            installed=call('start',account='installed',url=url)
            assert call('observe',task=installed['task'])['nodes']
            call('finish',task=installed['task'])
            checks.append('skill installation, no overwrite, installed binary launches browser independently')
        finally:
            stop();server.shutdown()
    return checks


if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',required=True);p.add_argument('--browser')
    a=p.parse_args()
    checks=run(str(Path(a.binary).resolve()),a.browser)
    print(json.dumps({'ok':True,'checks':checks},ensure_ascii=False,indent=2))
