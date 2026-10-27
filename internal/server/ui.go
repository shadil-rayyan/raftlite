package server

import (
	"net/http"
	"os"
	"strings"
)

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>RaftLite Console</title>
<style>
:root{--bg:#0b1220;--panel:#111a2e;--card:#17233d;--line:#2a3a5f;--text:#e5edf7;--muted:#93a4bf;--blue:#3b82f6;--green:#22c55e;--red:#ef4444;--amber:#f59e0b}
*{box-sizing:border-box}
body{margin:0;font-family:Inter,system-ui,sans-serif;background:var(--bg);color:var(--text)}
header{padding:18px 24px;border-bottom:1px solid var(--line);display:flex;justify-content:space-between;align-items:center;gap:16px;flex-wrap:wrap}
h1{margin:0;font-size:22px}
h2{margin:0 0 12px;font-size:15px;color:var(--muted);text-transform:uppercase;letter-spacing:.08em}
main{padding:20px;max-width:1400px;margin:0 auto}
.grid{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin-bottom:16px}
@media(max-width:900px){.grid{grid-template-columns:repeat(2,1fr)}}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px}
.stat{font-size:24px;font-weight:700}.label{color:var(--muted);font-size:12px}
.badge{display:inline-block;padding:3px 10px;border-radius:999px;font-size:12px;background:#334155}
.ok{background:#14532d;color:#bbf7d0}.warn{background:#78350f;color:#fde68a}.err{background:#7f1d1d;color:#fecaca}
.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}
button{background:var(--blue);color:#fff;border:0;border-radius:8px;padding:9px 14px;cursor:pointer;font-weight:600}
button.secondary{background:var(--panel);border:1px solid var(--line)}
input,select,textarea{background:var(--panel);color:var(--text);border:1px solid var(--line);border-radius:8px;padding:9px}
table{width:100%;border-collapse:collapse;font-size:14px}
th,td{padding:10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}
th{color:var(--muted);font-size:12px;text-transform:uppercase}
.tabs{display:flex;gap:8px;margin-bottom:16px;flex-wrap:wrap}
.tab{padding:8px 14px;border-radius:8px;cursor:pointer;background:var(--panel);border:1px solid var(--line)}
.tab.active{background:var(--blue);border-color:var(--blue)}
.panel{display:none}.panel.active{display:block}
pre{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:12px;overflow:auto;max-height:360px;font-size:12px}
.muted{color:var(--muted)}
.toast{position:fixed;right:16px;bottom:16px;background:var(--card);border:1px solid var(--line);padding:12px 16px;border-radius:10px;display:none}
</style>
</head>
<body>
<header>
<div>
<h1>RaftLite Console</h1>
<div class="muted">Distributed security-policy control plane</div>
</div>
<div class="row">
<span>Health <span id="health" class="badge">?</span></span>
<span>Node <b id="node">-</b></span>
<span>Role <b id="role">-</b></span>
<span>Leader <b id="leader">-</b></span>
<span>Term <b id="term">-</b></span>
<span>Quorum <span id="quorumBadge" class="badge">?</span></span>
<input id="token" placeholder="Bearer token" style="width:180px">
<button class="secondary" onclick="refresh()">Refresh</button>
</div>
</header>
<main>
<div class="tabs">
<div class="tab active" data-tab="overview">Overview</div>
<div class="tab" data-tab="policies">Policies</div>
<div class="tab" data-tab="ipcheck">IP Check</div>
<div class="tab" data-tab="audit">Audit</div>
<div class="tab" data-tab="metrics">Metrics</div>
<div class="tab" data-tab="diag">Diagnostics</div>
<div class="tab" id="devTab" style="display:none" data-tab="devtests">Dev Tests</div>
</div>

<section id="overview" class="panel active">
<div class="grid">
<div class="card"><div class="label">Revision</div><div class="stat" id="rev">-</div></div>
<div class="card"><div class="label">Policies</div><div class="stat" id="policyCount">-</div></div>
<div class="card"><div class="label">Last Log</div><div class="stat" id="lastLog">-</div></div>
<div class="card"><div class="label">Commit</div><div class="stat" id="commit">-</div></div>
</div>
<div class="card">
<h2>Cluster Topology</h2>
<table><thead><tr><th>Node</th><th>Role</th><th>Leader</th><th>Term</th><th>Last Log</th><th>Commit</th><th>Reachable</th></tr></thead>
<tbody id="topology"></tbody></table>
</div>
<div class="card">
<h2>Create IP Block</h2>
<div class="row">
<input id="ip" placeholder="IP or CIDR">
<input id="reason" placeholder="reason">
<input id="dur" placeholder="duration (1h)">
<button onclick="createBlock()">Create</button>
</div>
<div id="createResult" class="muted" style="margin-top:8px"></div>
</div>
</section>

<section id="policies" class="panel">
<div class="card">
<div class="row" style="margin-bottom:10px">
<input id="policyFilter" placeholder="filter target/status" oninput="renderPolicies()">
</div>
<table><thead><tr><th>ID</th><th>Target</th><th>Status</th><th>Version</th><th>Expires</th><th></th></tr></thead>
<tbody id="policiesBody"></tbody></table>
</div>
</section>

<section id="ipcheck" class="panel">
<div class="card">
<h2>IP / CIDR Check</h2>
<div class="row">
<input id="checkip" placeholder="IP or CIDR">
<button onclick="checkIP()">Check</button>
<span id="checkResult"></span>
</div>
<div class="muted" style="margin-top:8px">Tests exact IP and CIDR membership against committed policy.</div>
</div>
</section>

<section id="audit" class="panel">
<div class="card">
<h2>Recent Audit Entries</h2>
<table><thead><tr><th>Time</th><th>Actor</th><th>Operation</th><th>Target</th><th>Outcome</th><th>Index</th></tr></thead>
<tbody id="auditRows"></tbody></table>
</div>
</section>

<section id="metrics" class="panel">
<div class="card">
<h2>Prometheus Metrics</h2>
<pre id="metricsText">loading...</pre>
</div>
</section>

<section id="diag" class="panel">
<div class="card">
<h2>Diagnostics</h2>
<div class="row" style="margin-bottom:10px">
<span>Cluster size <b id="clusterSize">-</b></span>
<span>Quorum <b id="quorum">-</b></span>
<span>Majority commit <b id="majCommit">-</b></span>
<span>Majority log <b id="majLog">-</b></span>
</div>
<pre id="diagJson">loading...</pre>
</div>
</section>

<section id="devtests" class="panel">
<div class="card">
<h2>Dev Self-Test (removable)</h2>
<button onclick="runTests()">Run Self-Test</button>
<div id="testResults" style="margin-top:12px"></div>
</div>
</section>

<div class="toast" id="toast"></div>
</main>

<script>
const DEV_UI = __DEV_UI__;
let allPolicies = [];
function $(id){return document.getElementById(id)}
function esc(v){return String(v==null?'':v).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')}
function toast(msg){const t=$('toast');t.textContent=msg;t.style.display='block';setTimeout(()=>t.style.display='none',2500)}
async function request(path, opts){
  opts = opts || {};
  const t = $('token').value.trim();
  const headers = Object.assign({'Content-Type':'application/json'}, opts.headers || {});
  if(t) headers['Authorization'] = 'Bearer ' + t;
  const r = await fetch(path, Object.assign({}, opts, {headers}));
  const text = await r.text();
  let data = text;
  try { data = JSON.parse(text); } catch(e){}
  return {ok:r.ok,status:r.status,data};
}
function tab(name){
  document.querySelectorAll('.tab').forEach(x=>x.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(x=>x.classList.remove('active'));
  const t = document.querySelector('.tab[data-tab="'+name+'"]'); if(t) t.classList.add('active');
  const p = document.getElementById(name); if(p) p.classList.add('active');
}
document.querySelectorAll('.tab').forEach(t=>t.addEventListener('click',()=>tab(t.dataset.tab)));

async function refresh(){
  try{
    const h = await request('/healthz');
    const he = $('health'); he.textContent = (h.ok && h.data && h.data.status) ? h.data.status : 'error';
    he.className = 'badge ' + (he.textContent==='ok'?'ok':'err');
    const n = await request('/v1/node');
    if(n.ok){
      $('node').textContent = n.data.node_id || '-';
      $('role').textContent = n.data.role || '-';
      $('leader').textContent = n.data.leader_id || '-';
      $('term').textContent = n.data.term != null ? n.data.term : '-';
      $('lastLog').textContent = n.data.last_log_index != null ? n.data.last_log_index : '-';
      $('commit').textContent = n.data.commit_index != null ? n.data.commit_index : '-';
    }
    const d = await request('/v1/diagnostics');
    if(d.ok){
      $('clusterSize').textContent = d.data.cluster_size;
      $('quorum').textContent = d.data.quorum;
      $('majCommit').textContent = d.data.majority_commit_count + '/' + d.data.cluster_size;
      $('majLog').textContent = d.data.majority_log_count + '/' + d.data.cluster_size;
      const qb = $('quorumBadge');
      qb.textContent = d.data.quorum_ok ? 'OK' : 'DEGRADED';
      qb.className = 'badge ' + (d.data.quorum_ok ? 'ok' : 'err');
      const tb = $('topology'); tb.innerHTML = '';
      const rows = [d.data.node].concat(d.data.peers || []);
      rows.forEach(x=>{
        const tr = document.createElement('tr');
        tr.innerHTML = '<td>'+esc(x.node_id)+'</td><td>'+esc(x.role)+'</td><td>'+esc(x.leader_id)+'</td><td>'+esc(x.term)+'</td><td>'+esc(x.last_log_index)+'</td><td>'+esc(x.commit_index)+'</td><td>'+(x.reachable?'<span class="badge ok">yes</span>':'<span class="badge err">no</span>')+'</td>';
        tb.appendChild(tr);
      });
      $('diagJson').textContent = JSON.stringify(d.data, null, 2);
    }
    const p = await request('/v1/policies');
    if(p.ok){
      allPolicies = p.data.policies || [];
      $('rev').textContent = p.data.revision != null ? p.data.revision : '-';
      $('policyCount').textContent = allPolicies.length;
      renderPolicies();
    }
    const a = await request('/v1/audit?limit=50');
    if(a.ok) renderAudit(a.data.entries || []);
    const m = await fetch('/metrics'); const mt = await m.text();
    $('metricsText').textContent = mt;
  }catch(e){
    $('health').textContent = 'error'; $('health').className = 'badge err';
  }
}
function renderPolicies(){
  const pf = $('policyFilter'); const q = pf ? (pf.value || '').toLowerCase() : '';
  const tb = $('policiesBody'); if(!tb) return; tb.innerHTML = '';
  allPolicies.filter(x=>JSON.stringify(x).toLowerCase().includes(q)).forEach(x=>{
    const tr = document.createElement('tr');
    tr.innerHTML = '<td>'+esc(x.id)+'</td><td>'+esc(x.target)+'</td><td>'+esc(x.status)+'</td><td>'+esc(x.version)+'</td><td>'+esc(x.expires_at||'-')+'</td><td><button class="secondary" onclick="revoke(\''+esc(x.id)+'\')">Revoke</button></td>';
    tb.appendChild(tr);
  });
}
async function createBlock(){
  const ip = $('ip').value.trim();
  const reason = $('reason').value.trim();
  const dur = $('dur').value.trim();
  const body = {ip}; if(reason) body.reason = reason; if(dur) body.duration = dur;
  const r = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify(body)});
  $('createResult').textContent = JSON.stringify(r.data);
  toast(r.ok ? 'Created' : 'Failed');
  refresh();
}
async function revoke(id){
  const r = await request('/v1/policies/'+encodeURIComponent(id), {method:'DELETE'});
  toast(r.ok ? 'Revoked' : 'Failed');
  refresh();
}
async function checkIP(){
  const ip = $('checkip').value.trim();
  const r = await request('/v1/blocked/'+encodeURIComponent(ip));
  $('checkResult').textContent = JSON.stringify(r.data);
}
function renderAudit(entries){
  const tb = $('auditRows'); tb.innerHTML = '';
  entries.slice().reverse().forEach(e=>{
    const tr = document.createElement('tr');
    tr.innerHTML = '<td>'+esc(e.timestamp)+'</td><td>'+esc(e.actor)+'</td><td>'+esc(e.operation)+'</td><td>'+esc(e.target)+'</td><td>'+esc(e.outcome)+'</td><td>'+esc(e.raft_index)+'</td>';
    tb.appendChild(tr);
  });
}
async function runTests(){
  const out = [];
  function add(name, pass, detail){
    out.push({name, pass, detail});
  }
  const h = await request('/healthz');
  add('healthz', h.ok && h.data && h.data.status==='ok', JSON.stringify(h.data));
  const n = await request('/v1/node');
  add('node identity', n.ok && !!n.data.node_id, JSON.stringify(n.data));
  const d = await request('/v1/diagnostics');
  add('diagnostics', d.ok, JSON.stringify(d.data && d.data.quorum_ok));
  if(d.ok){
    add('quorum ok', !!d.data.quorum_ok, d.data.majority_commit_count+'/'+d.data.cluster_size);
    add('majority has log', !!d.data.majority_has_log, d.data.majority_log_count+'/'+d.data.cluster_size);
  }
  for(const ip of ['10.0.0.5','2001:db8::1','not-an-ip','10.0.0.0/8','256.1.1.1']){
    const r = await request('/v1/blocked/'+encodeURIComponent(ip));
    add('ip '+ip, r.status !== 500, r.status+':'+JSON.stringify(r.data));
  }
  const a = await request('/v1/audit?limit=5');
  add('audit endpoint', a.ok, a.status+':'+a.data.count);
  const html = out.map(x=>'<div style="padding:6px 0;border-bottom:1px solid var(--line)"><span class="badge '+(x.pass?'ok':'err')+'">'+(x.pass?'PASS':'FAIL')+'</span> '+esc(x.name)+' <span class="muted">'+esc(x.detail)+'</span></div>').join('');
  $('testResults').innerHTML = html;
}
if(DEV_UI){ $('devTab').style.display = 'inline-block'; }
setInterval(refresh, 2000);
refresh();
</script>
</body>
</html>`

func (a *API) handleUI(w http.ResponseWriter, r *http.Request) {
	dev := "false"
	if os.Getenv("RAFTLITE_DEV_UI") == "1" {
		dev = "true"
	}
	html := strings.ReplaceAll(uiHTML, "__DEV_UI__", dev)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}
