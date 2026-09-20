let state = { packages: [], builds: [], policies: [] };
let selectedBuild = '';

const $ = selector => document.querySelector(selector);
const fmt = value => value === null || value === undefined ? '' : JSON.stringify(value, null, 2);

async function api(path, options = {}) {
  const headers = options.headers || {};
  if (options.body && !(options.body instanceof FormData)) headers['Content-Type'] = 'application/json';
  const response = await fetch(path, { ...options, headers });
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) throw new Error(data?.error || response.statusText);
  return data;
}

async function refresh() {
  state = await api('/api/state');
  renderCounts();
  renderPackages();
  renderPolicies();
  renderBuilds();
  renderMap();
  document.querySelector('#events').textContent = fmt(await api('/api/events'));
}

function renderCounts() {
  const labels = [
    ['package_pending', '待确认'], ['package_accepted', '已接受'], ['package_rejected', '被拒绝'],
    ['build_recovering', '恢复中'], ['build_building', '构建中'], ['build_published', '已发布'], ['build_failed', '失败']
  ];
  document.querySelector('#statusCounts').innerHTML = labels
    .map(([key, label]) => `<span class="pill ${key.split('_')[1]}">${label}: ${state.counts?.[key] || 0}</span>`).join('');
}

function statusBadge(status) { return `<span class="pill ${status}">${status}</span>`; }

function renderPackages() {
  document.querySelector('#packages').innerHTML = state.packages.map(pkg => `
    <article class="card">
      <h3>${pkg.name} ${statusBadge(pkg.status)}</h3>
      <div class="muted">${pkg.id} · v${pkg.version} · ${pkg.crs}/${pkg.scheme || 'xyz'} · ${pkg.license} · ${pkg.updated_at}</div>
      <div>有效瓦片 ${pkg.tiles.length}；隔离 ${(pkg.isolated || []).length}</div>
      ${(pkg.isolated || []).map(t => `<div class="issues">隔离 ${t.z}/${t.x}/${t.y}: ${t.reason}</div>`).join('')}
      ${pkg.status === 'pending' ? `
        <button onclick="decide('${pkg.id}','accepted')">接受</button>
        <button onclick="decide('${pkg.id}','rejected')">拒绝</button>` : ''}
    </article>`).join('');
}

window.decide = async (id, status) => {
  await api(`/api/packages/${encodeURIComponent(id)}/decision`, { method: 'POST', body: JSON.stringify({ status }) });
  refresh();
};

function renderPolicies() {
  document.querySelector('#policies').innerHTML = state.policies.map(policy => `
    <div class="card"><strong>规则</strong><div class="muted">${fmt(policy)}</div></div>`).join('');
}

function renderBuilds() {
  const select = document.querySelector('#buildSelect');
  const current = select.value || selectedBuild;
  select.innerHTML = state.builds.map(build => `<option value="${build.build_id}">${build.build_id} (${build.status})</option>`).join('');
  if (state.builds.some(build => build.build_id === current)) select.value = current;
  selectedBuild = select.value || '';
  const exportLink = document.querySelector('#exportLink');
  exportLink.href = selectedBuild ? `/api/builds/${encodeURIComponent(selectedBuild)}/export` : '#';
  document.querySelector('#builds').innerHTML = state.builds.map(build => `
    <article class="card">
      <h3>${build.build_id} ${statusBadge(build.status)}</h3>
      <div class="muted">zoom ${build.manifest.zoom} · 来源 ${(build.manifest.package_ids || []).join(', ')} · ${fmt(build.manifest.source_summary)}</div>
      ${build.reason ? `<div class="issues">${build.reason}</div>` : ''}
      ${build.status !== 'published' ? `<button onclick="recover('${build.build_id}')">恢复/重试</button>` : ''}
    </article>`).join('');
}

window.recover = async id => {
  await api(`/api/builds/${encodeURIComponent(id)}/recover`, { method: 'POST' });
  refresh();
};

function project(lon, lat) {
  return { x: (lon + 180) / 360 * 960, y: (90 - lat) / 180 * 480 };
}

function drawBox(ctx, bbox, color, fill) {
  const pieces = bbox.west > bbox.east
    ? [{ west: bbox.west, east: 180, south: bbox.south, north: bbox.north }, { west: -180, east: bbox.east, south: bbox.south, north: bbox.north }]
    : [bbox];
  pieces.forEach(piece => {
    const a = project(piece.west, piece.north);
    const b = project(piece.east, piece.south);
    ctx.fillStyle = fill;
    ctx.strokeStyle = color;
    ctx.fillRect(a.x, a.y, b.x - a.x, b.y - a.y);
    ctx.strokeRect(a.x, a.y, b.x - a.x, b.y - a.y);
  });
}

function renderMap() {
  const canvas = document.querySelector('#map');
  const ctx = canvas.getContext('2d');
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  state.packages.filter(p => p.status === 'accepted').forEach((pkg, index) => {
    const colors = ['rgba(70,170,255,.22)', 'rgba(90,220,150,.22)', 'rgba(245,190,80,.22)', 'rgba(210,120,255,.22)'];
    pkg.tiles.forEach(tile => drawBox(ctx, tile.bbox, '#9ecbff', colors[index % colors.length]));
  });
  const build = state.builds.find(b => b.build_id === selectedBuild);
  if (!build) return;
  const heat = {};
  build.manifest.tiles.forEach(tile => {
    const key = `${tile.z}/${tile.x}/${tile.y}`;
    heat[key] = (tile.issues || []).length;
  });
  build.manifest.tiles.forEach(tile => {
    const color = tile.hole ? 'rgba(80,80,80,.24)' : heat[`${tile.z}/${tile.x}/${tile.y}`] ? 'rgba(255,80,60,.30)' : 'rgba(80,210,140,.18)';
    drawBox(ctx, tile.bbox, heat[`${tile.z}/${tile.x}/${tile.y}`] ? '#ff7866' : '#52c28c', color);
  });
}

canvasHit = event => {
  const rect = event.target.getBoundingClientRect();
  return { lon: (event.clientX - rect.left) / rect.width * 360 - 180, lat: 90 - (event.clientY - rect.top) / rect.height * 180 };
};

document.querySelector('#map').addEventListener('click', async event => {
  const build = state.builds.find(b => b.build_id === selectedBuild);
  if (!build) return;
  const point = canvasHit(event);
  const tile = build.manifest.tiles.find(t => inBox(point, t.bbox));
  if (!tile) return;
  const detail = await api(`/api/builds/${encodeURIComponent(build.build_id)}/tiles/${tile.z}/${tile.x}/${tile.y}?provenance=1`);
  document.querySelector('#tileInfo').textContent = fmt(detail);
});

function inBox(point, bbox) {
  if (bbox.west > bbox.east) return (point.lon >= bbox.west || point.lon < bbox.east) && point.lat >= bbox.south && point.lat < bbox.north;
  return point.lon >= bbox.west && point.lon < bbox.east && point.lat >= bbox.south && point.lat < bbox.north;
}

document.querySelector('#buildSelect').addEventListener('change', event => { selectedBuild = event.target.value; renderMap(); });
document.querySelector('#refreshMap').addEventListener('click', refresh);
document.querySelector('#recoverBuild').addEventListener('click', () => selectedBuild && window.recover(selectedBuild));

document.querySelector('#uploadForm').addEventListener('submit', async event => {
  event.preventDefault();
  const data = new FormData(event.target);
  const idem = data.get('idem');
  data.delete('idem');
  try {
    await api('/api/packages', { method: 'POST', body: data, headers: idem ? { 'Idempotency-Key': idem } : {} });
  } finally { refresh(); }
});

function formValues(form) {
  return Object.fromEntries(new FormData(form).entries());
}

document.querySelector('#policyForm').addEventListener('submit', async event => {
  event.preventDefault();
  const values = formValues(event.target);
  await api('/api/policies', { method: 'POST', body: JSON.stringify({
    region: { west: +values.west, south: +values.south, east: +values.east, north: +values.north },
    priority_package: values.priority || undefined,
    locked_package: values.lockedPackage || undefined,
    locked_version: values.lockedVersion || undefined
  }) });
  refresh();
});

document.querySelector('#buildForm').addEventListener('submit', async event => {
  event.preventDefault();
  const values = formValues(event.target);
  const headers = values.idem ? { 'Idempotency-Key': values.idem } : {};
  const record = await api('/api/builds', { method: 'POST', headers, body: JSON.stringify({
    region: { west: +values.west, south: +values.south, east: +values.east, north: +values.north }, zoom: +values.zoom
  }) });
  selectedBuild = record.build_id;
  setTimeout(refresh, 150);
  setTimeout(refresh, 1200);
});

document.querySelector('#importForm').addEventListener('submit', async event => {
  event.preventDefault();
  await api('/api/imports', { method: 'POST', body: new FormData(event.target) });
  refresh();
});

refresh();
