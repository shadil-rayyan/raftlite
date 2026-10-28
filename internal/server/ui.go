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
<div class="tab" data-tab="gateways">Gateways (Sim)</div>
<div class="tab" id="devTab" style="display:none" data-tab="devtests">Dev Tests</div>
<div class="tab" id="labTab" style="display:none" data-tab="testlab">Test Lab</div>
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
<div class="grid" id="metricCards"></div>
<div class="card">
<h2>All Metrics</h2>
<table><thead><tr><th>Metric</th><th>Value</th><th>Type</th></tr></thead>
<tbody id="metricsTable"></tbody></table>
<details style="margin-top:12px"><summary class="muted">Raw exposition</summary>
<pre id="metricsText">loading...</pre></details>
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

<section id="gateways" class="panel">
<div class="muted" style="margin-bottom:10px">SIMULATED gateways — acceptance is simulated by the mock adapter and does not verify real network enforcement.</div>
<div class="grid">
<div class="card"><div class="label">Total Gateways</div><div class="stat" id="gwTotal">-</div></div>
<div class="card"><div class="label">Online</div><div class="stat" id="gwOnline">-</div></div>
<div class="card"><div class="label">Offline</div><div class="stat" id="gwOffline">-</div></div>
<div class="card"><div class="label">Needs Attention</div><div class="stat" id="gwAttention">-</div></div>
</div>
<div class="card">
<h2>Current Rollout</h2>
<div id="rolloutPanel" class="muted">no rollouts yet</div>
</div>
<div class="card">
<h2>Gateways</h2>
<table><thead><tr><th>ID</th><th>Name</th><th>Status</th><th>Desired</th><th>Reported</th><th>State</th><th>Verification</th><th>Last Error</th></tr></thead>
<tbody id="gatewaysBody"></tbody></table>
</div>
<div class="card" id="gwDetailCard" style="display:none">
<h2 id="gwDetailTitle">Gateway Detail</h2>
<pre id="gwDetail">-</pre>
<h2>Recent Events</h2>
<table><thead><tr><th>Time</th><th>Type</th><th>Result</th><th>Revision</th><th>Error</th></tr></thead>
<tbody id="gwDetailEvents"></tbody></table>
</div>
<div class="card" id="gwDevCard" style="display:none">
<h2>Simulator Controls (dev only)</h2>
<div class="row" style="margin-bottom:8px">
<input id="gwName" placeholder="gateway name" style="width:150px">
<input id="gwDelay" placeholder="delay ms" style="width:90px">
<button onclick="gwCreate()">Create Gateway</button>
<button class="secondary" onclick="gwReconcile()">Re-reconcile</button>
</div>
<div class="row" style="margin-bottom:8px">
<input id="gwTarget" placeholder="gateway id (gw-001)" style="width:150px">
<button class="secondary" onclick="gwScenario('offline')">Offline</button>
<button class="secondary" onclick="gwScenario('online')">Online</button>
<button class="secondary" onclick="gwScenario('reject-next')">Reject Next</button>
<button class="secondary" onclick="gwScenario('accept-next')">Accept Next</button>
<button class="secondary" onclick="gwScenario('timeout-next')">Timeout Next</button>
<button class="secondary" onclick="gwScenario('reset')">Reset</button>
<button class="secondary" onclick="gwSetDelay()">Set Delay</button>
<button class="secondary" onclick="gwDelete()">Delete</button>
</div>
<div class="muted">One-shot flags apply to the next delivery only. Timeout means no response within the delivery window — it is not a rejection.</div>
</div>
<div class="card">
<h2>Recent Fleet Events</h2>
<table><thead><tr><th>Time</th><th>Gateway</th><th>Type</th><th>Result</th><th>Revision</th><th>Error</th></tr></thead>
<tbody id="gwEventsBody"></tbody></table>
</div>
</section>

<section id="devtests" class="panel">
<div class="card">
<h2>Dev Self-Test (removable)</h2>
<button onclick="runTests()">Run Self-Test</button>
<div id="testResults" style="margin-top:12px"></div>
</div>
</section>

<section id="testlab" class="panel">
<div class="card">
<h2>Test Lab (removable)</h2>
<div class="row" style="margin-bottom:12px">
<button onclick="runLab()">Run All Scenarios</button>
<button class="secondary" onclick="cleanupLab()">Cleanup Lab Policies</button>
<button class="secondary" onclick="runLabScenario('liveness')">Liveness</button>
<button class="secondary" onclick="runLabScenario('leader')">Leader / Quorum</button>
<button class="secondary" onclick="runLabScenario('create')">Create Block</button>
<button class="secondary" onclick="runLabScenario('idem')">Idempotency</button>
<button class="secondary" onclick="runLabScenario('revoke')">Revoke</button>
<button class="secondary" onclick="runLabScenario('validation')">Validation</button>
<button class="secondary" onclick="runLabScenario('audit')">Audit</button>
<button class="secondary" onclick="runLabScenario('metrics')">Metrics</button>
<button class="secondary" onclick="runLabScenario('forwarding')">Follower Forwarding</button>
</div>
<div class="row" style="margin-bottom:12px">
<input id="labIP" placeholder="target IP (blank = random 203.0.113.x)">
<button class="secondary" onclick="$('labIP').value = randomTestIP()">Random IP</button>
</div>
<div id="labResults"></div>
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
    renderMetrics(mt);
    refreshFleet();
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
function renderMetrics(text){
  const cards = [
    ['raftlite_is_leader','Leader'],
    ['raftlite_term','Term'],
    ['raftlite_commit_index','Commit'],
    ['raftlite_applied_index','Applied'],
    ['raftlite_policies','Policies'],
    ['raftlite_wal_size_bytes','WAL Bytes'],
    ['raftlite_elections_total','Elections'],
    ['raftlite_commits_total','Commits'],
  ];
  const values = {};
  const rows = [];
  text.split('\n').forEach(line=>{
    if(!line || line.startsWith('#')) return;
    const sp = line.lastIndexOf(' ');
    if(sp < 0) return;
    const lhs = line.slice(0,sp), val = line.slice(sp+1);
    let name = lhs, labels = '';
    const brace = lhs.indexOf('{');
    if(brace >= 0){ name = lhs.slice(0,brace); labels = lhs.slice(brace); }
    values[name] = val;
    rows.push([name + labels, val]);
  });
  const cg = $('metricCards');
  cg.innerHTML = '';
  cards.forEach(function(pair){
    const v = values[pair[0]] != null ? values[pair[0]] : '-';
    const d = document.createElement('div');
    d.className = 'card';
    d.innerHTML = '<div class="label">'+pair[1]+'</div><div class="stat">'+esc(v)+'</div>';
    cg.appendChild(d);
  });
  const tb = $('metricsTable'); tb.innerHTML = '';
  rows.forEach(function(r){
    const tr = document.createElement('tr');
    tr.innerHTML = '<td>'+esc(r[0])+'</td><td>'+esc(r[1])+'</td>';
    tb.appendChild(tr);
  });
}
function randomTestIP(){
  return '203.0.113.' + (2 + Math.floor(Math.random()*250));
}
let labPolicies = [];
async function runLabScenario(name){
  const results = [];
  function add(step, pass, detail){ results.push({step, pass, detail}); }
  const ip = ($('labIP').value || '').trim() || randomTestIP();
  const reqID = 'lab-' + Date.now();

  if(name === 'liveness' || name === 'all'){
    const h = await request('/healthz');
    add('healthz', h.ok && h.data && h.data.status==='ok', JSON.stringify(h.data));
    const r = await request('/readyz');
    add('readyz', r.ok, JSON.stringify(r.data));
  }
  if(name === 'leader' || name === 'all'){
    const d = await request('/v1/diagnostics');
    add('diagnostics', d.ok, d.ok ? ('quorum_ok=' + d.data.quorum_ok) : JSON.stringify(d.data));
    if(d.ok){
      add('quorum', !!d.data.quorum_ok, d.data.majority_commit_count + '/' + d.data.cluster_size);
      add('majority log', !!d.data.majority_has_log, d.data.majority_log_count + '/' + d.data.cluster_size);
      add('leader known', !!d.data.leader_id, d.data.leader_id);
    }
    const n = await request('/v1/node');
    add('node identity', n.ok && !!n.data.node_id, n.ok ? (n.data.node_id + ' role=' + n.data.role) : JSON.stringify(n.data));
  }
  if(name === 'create' || name === 'all'){
    const before = await request('/v1/policies');
    const rev0 = before.ok ? before.data.revision : 0;
    const c = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: ip, reason: 'lab test', request_id: reqID})});
    add('create ' + ip, c.ok && c.data && c.data.status==='committed', c.status + ':' + JSON.stringify(c.data));
    if(c.ok && c.data && c.data.policy_id){ labPolicies.push(c.data.policy_id); }
    const b = await request('/v1/blocked/' + encodeURIComponent(ip));
    add('blocked after create', b.ok && b.data && b.data.blocked === true, JSON.stringify(b.data));
    const after = await request('/v1/policies');
    add('revision advanced', after.ok && after.data.revision > rev0, (rev0) + ' -> ' + (after.ok ? after.data.revision : '?'));
  }
  if(name === 'idem' || name === 'all'){
    const idemIP = randomTestIP();
    const r1 = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: idemIP, request_id: reqID + '-idem'})});
    const r2 = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: idemIP, request_id: reqID + '-idem'})});
    add('first create', r1.ok && r1.data && r1.data.status==='committed', JSON.stringify(r1.data));
    add('replay idempotent', r2.ok && r2.data && r2.data.idempotent === true, JSON.stringify(r2.data));
    if(r1.ok && r1.data && r1.data.policy_id){ labPolicies.push(r1.data.policy_id); }
  }
  if(name === 'revoke' || name === 'all'){
    const revIP = randomTestIP();
    const c = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: revIP, request_id: reqID + '-revoke'})});
    add('create for revoke', c.ok && c.data && c.data.status==='committed', JSON.stringify(c.data));
    if(c.ok && c.data && c.data.policy_id){
      const d = await request('/v1/policies/' + encodeURIComponent(c.data.policy_id), {method:'DELETE'});
      add('revoke', d.ok, d.status + ':' + JSON.stringify(d.data));
      const b = await request('/v1/blocked/' + encodeURIComponent(revIP));
      add('unblocked after revoke', b.ok && b.data && b.data.blocked === false, JSON.stringify(b.data));
    }
  }
  if(name === 'validation' || name === 'all'){
    const bad = [['not-an-ip'],['256.1.1.1'],['0.0.0.0'],['::'],['10.0.0.0/8']];
    for(const pair of bad){
      const r = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: pair[0]})});
      add('reject ' + pair[0], r.status === 400, r.status + ':' + JSON.stringify(r.data));
    }
    const badDur = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: randomTestIP(), duration: '-1h'})});
    add('reject bad duration', badDur.status === 400, badDur.status + ':' + JSON.stringify(badDur.data));
  }
  if(name === 'audit' || name === 'all'){
    const a1 = await request('/v1/audit?limit=100');
    const count0 = a1.ok ? a1.data.count : 0;
    await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: randomTestIP(), request_id: reqID + '-audit'})});
    const a2 = await request('/v1/audit?limit=100');
    add('audit grows', a2.ok && a2.data.count > count0, count0 + ' -> ' + (a2.ok ? a2.data.count : '?'));
  }
  if(name === 'metrics' || name === 'all'){
    const m = await fetch('/metrics');
    const t = await m.text();
    add('metrics reachable', m.ok && t.includes('raftlite_commit_index'), m.status + ', ' + t.split('\n').length + ' lines');
  }
  if(name === 'forwarding' || name === 'all'){
    const n = await request('/v1/node');
    if(n.ok && n.data.role === 'follower'){
      const f = await request('/v1/policies/ip-blocks', {method:'POST', body:JSON.stringify({ip: randomTestIP(), request_id: reqID + '-fwd'})});
      add('follower create forwarded', f.ok, f.status + ':' + JSON.stringify(f.data));
      if(f.ok && f.data && f.data.policy_id){ labPolicies.push(f.data.policy_id); }
    } else {
      add('follower forwarding', true, 'this node is the leader; forwarding not exercised');
    }
  }
  renderLab(results, name);
}
async function runLab(){
  const names = ['liveness','leader','create','idem','revoke','validation','audit','metrics','forwarding'];
  for(const n of names){
    await runLabScenario(n);
  }
  toast('Lab run complete');
}
async function cleanupLab(){
  let ok = 0, fail = 0;
  for(const id of labPolicies){
    const r = await request('/v1/policies/' + encodeURIComponent(id), {method:'DELETE'});
    if(r.ok) ok++; else fail++;
  }
  labPolicies = [];
  toast('Cleanup: ' + ok + ' revoked, ' + fail + ' failed');
}
function renderLab(results, name){
  const html = results.map(function(x){
    return '<div style="padding:6px 0;border-bottom:1px solid var(--line)"><span class="badge '+(x.pass?'ok':'err')+'">'+(x.pass?'PASS':'FAIL')+'</span> '+esc(x.step)+' <span class="muted">'+esc(x.detail)+'</span></div>';
  }).join('');
  const d = document.createElement('div');
  d.innerHTML = '<h2 style="margin-top:12px">'+esc(name)+'</h2>' + html;
  $('labResults').prepend(d);
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
let fleetGws = [];
function gwState(g){
  if(g.connection_status === 'offline') return 'offline';
  if(g.last_rejected_revision != null && g.last_rejected_revision === g.desired_revision) return 'rejected';
  if(g.desired_revision > 0 && g.desired_revision === g.reported_accepted_revision) return 'applied';
  if(g.desired_revision > g.reported_accepted_revision) return 'stale';
  return 'unknown';
}
function gwStateBadge(s){
  const cls = {applied:'ok', offline:'err', rejected:'err'}[s] || '';
  return '<span class="badge '+cls+'">'+esc(s)+'</span>';
}
async function refreshFleet(){
  const g = await request('/v1/fleet/gateways');
  if(!g.ok){ $('gwTotal').textContent = '-'; return; }
  fleetGws = g.data.gateways || [];
  const s = g.data.summary || {};
  $('gwTotal').textContent = s.total != null ? s.total : '-';
  $('gwOnline').textContent = s.online != null ? s.online : '-';
  $('gwOffline').textContent = s.offline != null ? s.offline : '-';
  $('gwAttention').textContent = s.needs_attention != null ? s.needs_attention : '-';
  const tb = $('gatewaysBody'); tb.innerHTML = '';
  fleetGws.forEach(x=>{
    const tr = document.createElement('tr');
    tr.style.cursor = 'pointer';
    tr.onclick = function(){ gwDetail(x.id); };
    tr.innerHTML = '<td>'+esc(x.id)+'</td><td>'+esc(x.name)+'</td><td>'+gwStateBadge(x.connection_status)+
      '</td><td>'+esc(x.desired_revision)+'</td><td>'+esc(x.reported_accepted_revision)+'</td><td>'+
      gwStateBadge(gwState(x))+'</td><td>'+esc(x.verification)+'</td><td>'+esc(x.last_error ? x.last_error.code : '')+'</td>';
    tb.appendChild(tr);
  });
  const r = await request('/v1/fleet/rollouts');
  if(r.ok) renderRollout((r.data.rollouts || [])[0]);
  const e = await request('/v1/fleet/events?limit=50');
  renderFleetEvents(e.ok ? (e.data.events || []) : []);
}
function renderRollout(ro){
  const el = $('rolloutPanel');
  if(!ro){ el.textContent = 'no rollouts yet'; return; }
  let done = 0, total = 0;
  const rows = [];
  Object.keys(ro.results || {}).sort().forEach(function(k){
    const res = ro.results[k]; total++;
    if(res.status === 'accepted' || res.status === 'rejected' || res.status === 'timeout' || res.status === 'offline' || res.status === 'superseded') done++;
    rows.push('<tr><td>'+esc(k)+'</td><td>'+gwStateBadge(res.status)+'</td><td>'+esc(res.revision)+'</td><td>'+esc(res.error ? res.error.code : '')+'</td></tr>');
  });
  const pct = total ? Math.round(done*100/total) : 0;
  el.innerHTML = '<div style="margin-bottom:6px"><b>'+esc(ro.id)+'</b> revision '+esc(ro.revision)+
    ' <span class="muted">('+esc(ro.trigger_type)+')</span> — '+done+'/'+total+' resolved</div>'+
    '<div style="background:var(--line);border-radius:4px;height:8px;margin-bottom:8px"><div style="background:var(--blue);border-radius:4px;height:8px;width:'+pct+'%"></div></div>'+
    '<table><tbody>'+rows.join('')+'</tbody></table>';
}
function renderFleetEvents(evs){
  const tb = $('gwEventsBody'); tb.innerHTML = '';
  evs.forEach(function(e){
    const tr = document.createElement('tr');
    tr.innerHTML = '<td>'+esc(new Date(e.timestamp).toLocaleTimeString())+'</td><td>'+esc(e.gateway_id)+'</td><td>'+
      esc(e.type)+'</td><td>'+esc(e.result)+'</td><td>'+esc(e.revision)+'</td><td>'+esc(e.error ? e.error.code+' '+e.error.message : '')+'</td>';
    tb.appendChild(tr);
  });
}
async function gwDetail(id){
  const r = await request('/v1/fleet/gateways/'+encodeURIComponent(id));
  if(!r.ok){ toast('gateway detail failed: '+r.status); return; }
  $('gwDetailCard').style.display = 'block';
  $('gwDetailTitle').textContent = 'Gateway Detail — ' + id + ' (SIMULATED)';
  $('gwDetail').textContent = JSON.stringify(r.data.gateway, null, 2);
  const tb = $('gwDetailEvents'); tb.innerHTML = '';
  (r.data.events || []).forEach(function(e){
    const tr = document.createElement('tr');
    tr.innerHTML = '<td>'+esc(new Date(e.timestamp).toLocaleTimeString())+'</td><td>'+esc(e.type)+'</td><td>'+
      esc(e.result)+'</td><td>'+esc(e.revision)+'</td><td>'+esc(e.error ? e.error.code : '')+'</td>';
    tb.appendChild(tr);
  });
}
async function gwCreate(){
  const body = {name: $('gwName').value.trim() || 'sim-gateway'};
  if($('gwDelay').value) body.delay_ms = parseInt($('gwDelay').value, 10);
  const r = await request('/v1/fleet/gateways', {method:'POST', body: JSON.stringify(body)});
  toast(r.ok ? 'gateway created' : 'create failed: '+r.status);
  refreshFleet();
}
async function gwReconcile(){
  const r = await request('/v1/fleet/reconcile', {method:'POST'});
  toast(r.ok ? 'reconcile scheduled' : 'reconcile: '+(r.data && r.data.message ? r.data.message : r.status));
  refreshFleet();
}
function gwTargetId(){ return $('gwTarget').value.trim(); }
async function gwScenario(action){
  const id = gwTargetId();
  if(!id){ toast('enter a gateway id'); return; }
  const r = await request('/dev/mock-gateways/'+encodeURIComponent(id)+'/scenarios/'+action, {method:'POST'});
  toast(r.ok ? action+' applied to '+id : action+' failed: '+r.status);
  refreshFleet();
}
async function gwSetDelay(){
  const id = gwTargetId();
  if(!id){ toast('enter a gateway id'); return; }
  const ms = parseInt($('gwDelay').value, 10);
  if(isNaN(ms)){ toast('enter delay ms'); return; }
  const r = await request('/dev/mock-gateways/'+encodeURIComponent(id)+'/scenarios/set-delay', {method:'POST', body: JSON.stringify({delay_ms: ms})});
  toast(r.ok ? 'delay set' : 'set-delay failed: '+r.status);
}
async function gwDelete(){
  const id = gwTargetId();
  if(!id){ toast('enter a gateway id'); return; }
  const r = await request('/dev/mock-gateways/'+encodeURIComponent(id), {method:'DELETE'});
  toast(r.ok ? 'gateway deleted' : 'delete failed: '+r.status);
  refreshFleet();
}
if(DEV_UI){ $('devTab').style.display = 'inline-block'; $('labTab').style.display = 'inline-block'; $('gwDevCard').style.display = 'block'; }
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
