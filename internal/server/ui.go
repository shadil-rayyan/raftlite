package server

import "net/http"

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>RaftLite</title>
<style>
body{font-family:system-ui,sans-serif;margin:24px;background:#0f172a;color:#e2e8f0}
h1{margin:0 0 16px}h2{margin:24px 0 8px;font-size:16px;color:#94a3b8}
.card{background:#1e293b;border:1px solid #334155;border-radius:8px;padding:16px;margin-bottom:16px}
.row{display:flex;gap:12px;flex-wrap:wrap;align-items:center}
button{background:#3b82f6;color:#fff;border:0;border-radius:6px;padding:8px 12px;cursor:pointer}
input{background:#0f172a;color:#e2e8f0;border:1px solid #475569;border-radius:6px;padding:8px}
table{width:100%;border-collapse:collapse;margin-top:8px}
th,td{padding:8px;border-bottom:1px solid #334155;text-align:left;font-size:14px}
.badge{padding:2px 8px;border-radius:999px;font-size:12px;background:#334155}
.ok{background:#166534}.err{background:#991b1b}
small{color:#94a3b8}
</style>
</head>
<body>
<h1>RaftLite</h1>
<div class="card">
<div class="row">
<span>Health: <span id="health" class="badge">?</span></span>
<span>Leader: <b id="leader">-</b></span>
<span>Revision: <b id="rev">-</b></span>
<span>Policies: <b id="count">-</b></span>
<button onclick="refresh()">Refresh</button>
</div>
</div>

<div class="card">
<h2>Token (optional)</h2>
<input id="token" placeholder="Bearer token" style="width:100%">
</div>

<div class="card">
<h2>Create IP block</h2>
<div class="row">
<input id="ip" placeholder="IP (e.g. 10.0.0.5)">
<input id="reason" placeholder="reason">
<input id="dur" placeholder="duration (e.g. 1h)">
<button onclick="create()">Create</button>
</div>
<div id="createResult" style="margin-top:8px"></div>
</div>

<div class="card">
<h2>Check blocked IP</h2>
<div class="row">
<input id="checkip" placeholder="IP">
<button onclick="check()">Check</button>
<span id="checkResult"></span>
</div>
</div>

<div class="card">
<h2>Policies</h2>
<table><thead><tr><th>ID</th><th>Target</th><th>Status</th><th>Version</th></tr></thead>
<tbody id="policies"></tbody></table>
</div>

<script>
function headers(){const t=document.getElementById('token').value.trim();const h={'Content-Type':'application/json'};if(t)h['Authorization']='Bearer '+t;return h}
async function refresh(){
try{
const h=await fetch('/healthz');const hd=await h.json();
const he=document.getElementById('health');he.textContent=hd.status;he.className='badge '+(hd.status==='ok'?'ok':'err');
const l=await fetch('/v1/leader');const ld=await l.json();document.getElementById('leader').textContent=ld.leader_id||'-';
const p=await fetch('/v1/policies');const pd=await p.json();
document.getElementById('rev').textContent=pd.revision;document.getElementById('count').textContent=(pd.policies||[]).length;
const tb=document.getElementById('policies');tb.innerHTML='';
(pd.policies||[]).forEach(x=>{const tr=document.createElement('tr');
tr.innerHTML='<td>'+x.id+'</td><td>'+x.target+'</td><td>'+x.status+'</td><td>'+x.version+'</td>';tb.appendChild(tr);});
}catch(e){document.getElementById('health').textContent='error';document.getElementById('health').className='badge err';}
}
async function create(){
const ip=document.getElementById('ip').value.trim();const reason=document.getElementById('reason').value.trim();const dur=document.getElementById('dur').value.trim();
const body={ip};if(reason)body.reason=reason;if(dur)body.duration=dur;
const r=await fetch('/v1/policies/ip-blocks',{method:'POST',headers:headers(),body:JSON.stringify(body)});
const d=await r.json();document.getElementById('createResult').textContent=JSON.stringify(d);
refresh();
}
async function check(){
const ip=document.getElementById('checkip').value.trim();
const r=await fetch('/v1/blocked/'+encodeURIComponent(ip));const d=await r.json();
document.getElementById('checkResult').textContent=JSON.stringify(d);
}
setInterval(refresh,2000);refresh();
</script>
</body>
</html>`

func (a *API) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(uiHTML))
}
