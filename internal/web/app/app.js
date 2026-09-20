"use strict";

const state = {
  packages: [],
  regions: [],
  analysis: null,
  plan: null,
  builds: [],
  view: { scale: 40, ox: 60, oy: 40 },
  z: 2,
  mode: "sources",
  selectedBuild: null,
  drag: null,
};

const $ = (id) => document.getElementById(id);

async function api(method, path, body, isFile) {
  const opts = { method, headers: {} };
  if (body !== undefined && !isFile) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  } else if (isFile) {
    opts.body = body;
  }
  const res = await fetch(path, opts);
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (_) { data = text; }
  if (!res.ok) {
    const msg = data && data.error ? data.error : ("HTTP " + res.status);
    throw new Error(msg);
  }
  return data;
}

function toast(msg, kind) {
  const div = document.createElement("div");
  div.className = "toast " + (kind || "");
  div.textContent = msg;
  $("toasts").appendChild(div);
  setTimeout(() => div.remove(), 6000);
}

function fmtTime(t) {
  if (!t) return "";
  return new Date(t).toLocaleString();
}

async function refreshPackages() {
  state.packages = await api("GET", "/api/packages");
  const tb = $("packages-table").querySelector("tbody");
  tb.innerHTML = "";
  const qb = $("quarantine-box");
  qb.innerHTML = "";
  for (const p of state.packages) {
    const tr = document.createElement("tr");
    tr.innerHTML =
      `<td class="mono">${p.id}</td><td>${p.name}</td><td>${p.zoom}</td>` +
      `<td>${p.scheme}</td><td>${p.projection}</td><td>${p.tiles.length}</td>` +
      `<td>${escapeHtml(p.license)}</td><td>${(p.quarantined || []).length}</td>` +
      `<td>${fmtTime(p.imported_at)}</td>`;
    tb.appendChild(tr);
    for (const q of p.quarantined || []) {
      const d = document.createElement("div");
      d.className = "q-item";
      d.textContent = `已隔离 [${p.id}] ${q.reason}: ${q.detail}`;
      qb.appendChild(d);
    }
  }
  // keep region priority inputs populated with known packages
  $("r-priority").value = state.packages.map((p) => p.id).join(",");
}

function escapeHtml(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

async function refreshRegions() {
  state.regions = await api("GET", "/api/regions");
  const div = $("region-info");
  div.innerHTML = "";
  for (const r of state.regions) {
    const d = document.createElement("div");
    d.innerHTML = `<b>${escapeHtml(r.name)}</b> · z${r.zoom} · 优先级: ` +
      `<span class="mono">${(r.priority || []).join(" → ") || "(无)"}</span>` +
      (r.locked_package ? ` · 锁定 <span class="mono">${r.locked_package}</span>` : "");
    div.appendChild(d);
  }
}

function packageColor(pkgId) {
  if (!pkgId) return "#445";
  let h = 0;
  for (const ch of pkgId) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return `hsl(${h} 55% 55%)`;
}

const canvas = $("canvas");
const ctx = canvas.getContext("2d");

function resizeCanvas() {
  const ratio = window.devicePixelRatio || 1;
  const rect = canvas.getBoundingClientRect();
  canvas.width = Math.round(rect.width * ratio);
  canvas.height = Math.round(rect.height * ratio);
  ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
  draw();
}
window.addEventListener("resize", resizeCanvas);

function cellAtPixel(px, py) {
  const { scale, ox, oy } = state.view;
  const x = Math.floor((px - ox) / scale);
  const y = Math.floor((py - oy) / scale);
  return { x, y };
}

function draw() {
  const w = canvas.clientWidth, h = canvas.clientHeight;
  ctx.clearRect(0, 0, w, h);
  const n = 1 << state.z;
  // graticule background cells across the whole zoom level
  ctx.strokeStyle = "#1d283a";
  ctx.lineWidth = 1;
  for (let x = 0; x < n; x++) {
    for (let y = 0; y < n; y++) {
      const px = state.view.ox + x * state.view.scale;
      const py = state.view.oy + y * state.view.scale;
      ctx.strokeRect(px, py, state.view.scale, state.view.scale);
    }
  }
  drawRegion();
  drawCoverage();
  drawWarningsAndSeams();
  drawLegend();
}

function drawRegion() {
  if (!state.regions.length) return;
  const r = state.regions.find((x) => x.name === "main") || state.regions[0];
  state.z = r.zoom;
  ctx.save();
  ctx.fillStyle = "rgba(90,167,255,0.07)";
  ctx.strokeStyle = "rgba(90,167,255,0.7)";
  for (const rc of r.extent.rects) {
    for (let y = rc.y0; y < rc.y1; y++) {
      for (let x = rc.x0; x < rc.x1; x++) {
        const px = state.view.ox + x * state.view.scale;
        const py = state.view.oy + y * state.view.scale;
        ctx.fillRect(px, py, state.view.scale, state.view.scale);
        ctx.strokeRect(px, py, state.view.scale, state.view.scale);
      }
    }
  }
  ctx.restore();
}

function chosenMap() {
  const m = new Map();
  if (!state.plan) return m;
  for (const [k, cell] of Object.entries(state.plan.entries)) {
    m.set(k, cell);
  }
  return m;
}

function drawCoverage() {
  const chosen = chosenMap();
  for (const p of state.packages) {
    if (p.zoom !== state.z) continue;
    for (const t of p.tiles) {
      const key = `${t.id.z}/${t.id.x}/${t.id.y}`;
      const cell = chosen.get(key);
      let fill = packageColor(p.id);
      if (state.mode === "sources") {
        const isChosen = cell && cell.candidates[0] && cell.candidates[0].package_id === p.id;
        ctx.globalAlpha = chosen.size === 0 ? 0.75 : (isChosen ? 0.85 : 0.18);
      } else {
        ctx.globalAlpha = 0.35;
      }
      const px = state.view.ox + t.id.x * state.view.scale;
      const py = state.view.oy + t.id.y * state.view.scale;
      ctx.fillStyle = fill;
      ctx.fillRect(px + 1, py + 1, state.view.scale - 2, state.view.scale - 2);
      ctx.globalAlpha = 1;
      if (state.mode === "sources" && cell && cell.candidates.length > 1) {
        ctx.strokeStyle = "#e7b53c";
        ctx.lineWidth = 2;
        ctx.strokeRect(px + 1.5, py + 1.5, state.view.scale - 3, state.view.scale - 3);
      }
    }
  }
}

function drawWarningsAndSeams() {
  const a = state.analysis;
  if (!a) return;
  if (state.mode === "warnings") {
    for (const wn of a.warnings) {
      const px = state.view.ox + wn.tile.x * state.view.scale;
      const py = state.view.oy + wn.tile.y * state.view.scale;
      ctx.fillStyle = wn.code === "date_inversion" ? "rgba(231,181,60,0.55)" : "rgba(227,97,79,0.55)";
      ctx.fillRect(px + 2, py + 2, state.view.scale - 4, state.view.scale - 4);
    }
    for (const h of a.holes) {
      const px = state.view.ox + h.x * state.view.scale;
      const py = state.view.oy + h.y * state.view.scale;
      ctx.fillStyle = "rgba(255,255,255,0.06)";
      ctx.fillRect(px + 2, py + 2, state.view.scale - 4, state.view.scale - 4);
      ctx.fillStyle = "#93a0b5";
      ctx.font = "10px monospace";
      if (state.view.scale > 18) ctx.fillText("空", px + state.view.scale / 3, py + state.view.scale / 1.7);
    }
  }
  if (state.mode === "seams") {
    for (const s of a.seams) {
      const alpha = Math.max(0.25, Math.min(1, s.diff * 3 + (s.metadata_conflict ? 0.5 : 0)));
      ctx.strokeStyle = s.metadata_conflict
        ? `rgba(176,124,255,${alpha})`
        : `rgba(227,97,79,${alpha})`;
      ctx.lineWidth = 2 + s.diff * 6;
      const ax = state.view.ox + s.a.x * state.view.scale;
      const ay = state.view.oy + s.a.y * state.view.scale;
      ctx.beginPath();
      if (s.horizontal) {
        ctx.moveTo(ax + state.view.scale, ay);
        ctx.lineTo(ax + state.view.scale, ay + state.view.scale);
      } else {
        ctx.moveTo(ax, ay + state.view.scale);
        ctx.lineTo(ax + state.view.scale, ay + state.view.scale);
      }
      ctx.stroke();
    }
  }
}

function drawLegend() {
  const a = state.analysis;
  ctx.fillStyle = "#93a0b5";
  ctx.font = "12px sans-serif";
  const msg = a
    ? `覆盖 ${a.covered.length} · 空洞 ${a.holes.length} · 重复 ${a.duplicates.length} · 警告 ${a.warnings.length} · 接缝 ${a.seams.length}`
    : "点击「分析区域」";
  ctx.fillText(msg, 10, 18);
}

canvas.addEventListener("wheel", (e) => {
  e.preventDefault();
  const rect = canvas.getBoundingClientRect();
  const mx = e.clientX - rect.left, my = e.clientY - rect.top;
  const factor = e.deltaY < 0 ? 1.18 : 1 / 1.18;
  const newScale = Math.max(10, Math.min(160, state.view.scale * factor));
  const k = newScale / state.view.scale;
  state.view.ox = mx - (mx - state.view.ox) * k;
  state.view.oy = my - (my - state.view.oy) * k;
  state.view.scale = newScale;
  draw();
}, { passive: false });

canvas.addEventListener("mousedown", (e) => {
  state.drag = { x: e.clientX, y: e.clientY, ox: state.view.ox, oy: state.view.oy, moved: false };
});
canvas.addEventListener("mousemove", (e) => {
  if (!state.drag) return;
  const dx = e.clientX - state.drag.x, dy = e.clientY - state.drag.y;
  if (Math.abs(dx) + Math.abs(dy) > 3) state.drag.moved = true;
  state.view.ox = state.drag.ox + dx;
  state.view.oy = state.drag.oy + dy;
  draw();
});
canvas.addEventListener("mouseup", (e) => {
  const wasDrag = state.drag && state.drag.moved;
  state.drag = null;
  if (!wasDrag) selectAt(e);
});
canvas.addEventListener("mouseleave", () => { state.drag = null; });

async function selectAt(e) {
  if (!state.regions.length) { toast("请先定义区域", "error"); return; }
  const region = state.regions.find((x) => x.name === "main") || state.regions[0];
  const rect = canvas.getBoundingClientRect();
  const c = cellAtPixel(e.clientX - rect.left, e.clientY - rect.top);
  const key = `${region.zoom}/${c.x}/${c.y}`;
  const prov = await api("GET", `/api/provenance/${encodeURIComponent(region.name)}?z=${region.zoom}&x=${c.x}&y=${c.y}`);
  const box = $("selection");
  if (!prov.candidates || !prov.candidates.length) {
    box.innerHTML = `<b>瓦片 ${key}</b>：无候选输入（空洞或区域外）`;
    return;
  }
  box.innerHTML = `<b>输出瓦片 <span class="mono">${key}</span> 的全部候选输入</b>` +
    "<table><thead><tr><th>排名</th><th>来源包</th><th>哈希</th><th>许可</th><th>采集日期</th><th>方案</th></tr></thead><tbody>" +
    prov.candidates.map((cand) =>
      `<tr><td>${cand.rank}${cand.chosen ? " ✓" : ""}</td>` +
      `<td class="mono">${cand.package_id}</td>` +
      `<td class="mono">${cand.tile.sha.slice(0, 14)}…</td>` +
      `<td>${escapeHtml(cand.tile.license)}</td>` +
      `<td>${fmtTime(cand.tile.captured)}</td><td>${cand.tile.scheme}</td></tr>`).join("") +
    "</tbody></table>";
}

$("layer-mode").addEventListener("change", (e) => { state.mode = e.target.value; draw(); });

$("g-submit").addEventListener("click", async () => {
  const captured = $("g-captured").value ? new Date($("g-captured").value).toISOString() : "";
  const seed = Date.now() % 10000;
  const name = $("g-name").value.trim() || ("survey-" + seed.toString(36));
  const body = {
    name, zoom: +$("g-zoom").value, scheme: $("g-scheme").value,
    projection: $("g-proj").value, license: $("g-license").value, source: $("g-source").value,
    west: +$("g-west").value, south: +$("g-south").value, east: +$("g-east").value, north: +$("g-north").value,
    captured, x0: +$("g-x0").value, y0: +$("g-y0").value, w: +$("g-w").value, h: +$("g-h").value,
    size: 16, out_of_range: +$("g-oor").value, seed,
  };
  try {
    const res = await api("POST", "/api/packages/generate", body);
    toast(`已导入 ${res.result.imported_tiles} 个瓦片，隔离 ${res.result.quarantined.length} 项`, "ok");
    await Promise.all([refreshPackages()]);
    draw();
  } catch (err) { toast("导入失败: " + err.message, "error"); }
});

$("upload-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const f = $("upload-file").files[0];
  if (!f) return toast("请选择 tar 文件", "error");
  try {
    const res = await api("POST", "/api/packages/import", f, true);
    toast(`导入 ${res.result.package_id}：${res.result.imported_tiles} 瓦片`, "ok");
    await refreshPackages(); draw();
  } catch (err) { toast("上传失败: " + err.message, "error"); }
});

$("r-save").addEventListener("click", async () => {
  const body = {
    name: $("r-name").value.trim() || "main", zoom: +$("r-zoom").value,
    west: +$("r-west").value, south: +$("r-south").value,
    east: +$("r-east").value, north: +$("r-north").value,
    priority: $("r-priority").value.split(",").map((s) => s.trim()).filter(Boolean),
    locked_package: $("r-locked").value.trim(),
  };
  try {
    await api("POST", "/api/regions", body);
    await refreshRegions();
    state.analysis = null; state.plan = null;
    draw();
    toast("规则已保存（仅影响之后的计算，已发布构建不变）", "ok");
  } catch (err) { toast("保存区域失败: " + err.message, "error"); }
});

$("analyze-btn").addEventListener("click", async () => {
  const name = $("r-name").value.trim() || "main";
  try {
    state.analysis = await api("GET", `/api/analysis/${encodeURIComponent(name)}`);
    renderAnalysis(); draw();
  } catch (err) { toast("分析失败: " + err.message, "error"); }
});

$("plan-btn").addEventListener("click", async () => {
  const name = $("r-name").value.trim() || "main";
  try {
    state.plan = await api("POST", `/api/plans/${encodeURIComponent(name)}`);
    toast("候选拼接已生成", "ok"); draw();
  } catch (err) { toast("生成失败: " + err.message, "error"); }
});

function renderAnalysis() {
  const a = state.analysis;
  const div = $("analysis-summary");
  div.innerHTML =
    `<div>覆盖 <b>${a.covered.length}</b> · 空洞 <b class="bad-code">${a.holes.length}</b> · ` +
    `重复覆盖 <b class="warn-code">${a.duplicates.length}</b> · 接缝 <b>${a.seams.length}</b></div>` +
    a.warnings.map((w) => `<div><span class="${w.code.includes("license") || w.code.includes("pixels") ? "bad-code" : "warn-code"}">● ${w.code}</span> ` +
      `<span class="mono">${w.tile.z}/${w.tile.x}/${w.tile.y}</span> ${escapeHtml(w.detail)}</div>`).join("") +
    a.seams.map((s) => `<div>接缝 <span class="mono">${s.a.z}/${s.a.x}/${s.a.y}</span>↔<span class="mono">${s.b.z}/${s.b.x}/${s.b.y}</span> ` +
      `像素差异 ${(s.diff * 100).toFixed(0)}%${s.metadata_conflict ? " · <b>元数据冲突（图像相似≠授权相同）</b>" : ""}</div>`).join("");
}

function statusColumn(status) {
  if (status === "pending") return "pending";
  if (status === "accepted") return "accepted";
  if (status === "rejected") return "rejected";
  return "recovering"; // failed, recovering, building
}

async function refreshBuilds() {
  state.builds = await api("GET", "/api/builds");
  const cols = document.querySelectorAll(".build-col ul");
  cols.forEach((ul) => { ul.innerHTML = ""; });
  for (const b of state.builds) {
    const li = document.createElement("li");
    const written = b.tiles.filter((t) => t.written).length;
    li.innerHTML = `<div><span class="mono">${b.id}</span> <span class="badge ${b.status}">${b.status}</span></div>` +
      `<div>${b.region} · z${b.zoom} · 块 ${written}/${b.tiles.length}</div>`;
    const acts = document.createElement("div");
    acts.className = "build-actions";
    const actBtn = (label, fn) => {
      const btn = document.createElement("button");
      btn.textContent = label;
      btn.addEventListener("click", fn);
      acts.appendChild(btn);
    };
    if (b.status === "pending") {
      actBtn("开始写入", () => safe(() => api("POST", `/api/builds/${b.id}/start`)));
      actBtn("接受并发布", () => acceptBuild(b.id));
      actBtn("拒绝", () => safe(() => api("POST", `/api/builds/${b.id}/reject`)));
    }
    if (b.status === "failed" || b.status === "recovering" || b.status === "building") {
      actBtn("恢复并继续", () => safe(() => api("POST", `/api/builds/${b.id}/resume`)));
    }
    if (b.status === "accepted") {
      actBtn("查看清单", () => viewManifest(b.id));
      actBtn("导出 tar", () => exportBuild(b.id));
    }
    li.appendChild(acts);
    if (b.failure) {
      const f = document.createElement("div");
      f.className = "bad-code";
      f.textContent = "失败原因: " + b.failure;
      li.appendChild(f);
    }
    li.addEventListener("click", (e) => {
      if (e.target.tagName !== "BUTTON") showBuildDetail(b.id);
    });
    document.querySelector(`.build-col ul[data-status="${statusColumn(b.status)}"]`).appendChild(li);
  }
}

async function safe(p) {
  try { await p; } catch (err) { toast(err.message, "error"); }
  await refreshBuilds();
  await refreshEvents();
}

$("create-build").addEventListener("click", async () => {
  const region = $("r-name").value.trim() || "main";
  try {
    await api("POST", "/api/plans/" + encodeURIComponent(region));
    await api("POST", "/api/builds", { region });
    toast("构建已创建（pending，尚无输出可见）", "ok");
    await refreshBuilds(); await refreshEvents();
  } catch (err) { toast("创建构建失败: " + err.message, "error"); }
});

async function acceptBuild(id) {
  try {
    const man = await api("POST", `/api/builds/${id}/accept`);
    toast(`已发布 ${man.id}，清单哈希 ${man.tiles.length > 0 ? "" : ""}见详情`, "ok");
    await refreshBuilds(); await refreshEvents();
    showBuildDetail(id);
  } catch (err) { toast("接受失败: " + err.message, "error"); }
}

async function viewManifest(id) {
  const man = await api("GET", `/api/builds/${id}/manifest`);
  const d = $("build-detail");
  d.innerHTML = `<b>清单 <span class="mono">${man.id}</span></b>` +
    `<div>瓦片数 ${man.tiles.length} · 范围 W${man.bounds.west.toFixed(2)} S${man.bounds.south.toFixed(2)} ` +
    `E${man.bounds.east.toFixed(2)} N${man.bounds.north.toFixed(2)}</div>` +
    "<div>来源摘要:</div><ul>" +
    man.sources.map((s) => `<li class="mono">${s.package_id} · ${escapeHtml(s.source)} · ${escapeHtml(s.license)} · ${s.tiles} 瓦片</li>`).join("") +
    "</ul>";
}

function showBuildDetail(id) {
  api("GET", `/api/builds/${id}`).then((b) => {
    const d = $("build-detail");
    d.innerHTML = `<b>${b.id}</b> <span class="badge ${b.status}">${b.status}</span> · 规则哈希 <span class="mono">${b.rules_hash.slice(0, 12)}…</span>` +
      (b.manifest_sha ? ` · 清单 <span class="mono">${b.manifest_sha.slice(0, 12)}…</span>` : "") +
      `<div>${b.tiles.length} 个输出瓦片，已写 ${b.tiles.filter((t) => t.written).length}</div>`;
  });
}

function exportBuild(id) {
  window.location.href = `/api/builds/${id}/export`;
}

$("export-btn").addEventListener("click", () => {
  const accepted = state.builds.find((b) => b.status === "accepted");
  if (!accepted) return toast("没有已接受的构建可导出", "error");
  exportBuild(accepted.id);
});

$("reimport-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const f = $("reimport-file").files[0];
  if (!f) return toast("请选择导出的 tar", "error");
  try {
    const res = await api("POST", "/api/builds/reimport", f, true);
    const ok = res.same_tiles && res.same_bounds && res.same_sources;
    $("reimport-result").innerHTML =
      `<div class="${ok ? "ok-code" : "bad-code"}"><b>${ok ? "往返一致 ✓" : "存在差异 ✗"}</b></div>` +
      `<div>新构建 <span class="mono">${res.build_id}</span>，来源 <span class="mono">${res.origin_id}</span></div>` +
      `<div>瓦片编号一致: ${res.same_tiles} · 范围边界一致: ${res.same_bounds} · 来源摘要一致: ${res.same_sources}</div>`;
    toast("重新导入完成", "ok");
    await refreshBuilds();
  } catch (err) { toast("重新导入失败: " + err.message, "error"); }
});

async function refreshEvents() {
  const evs = await api("GET", "/api/events");
  const tb = $("events-table").querySelector("tbody");
  tb.innerHTML = "";
  for (const e of evs.slice().reverse()) {
    const tr = document.createElement("tr");
    tr.innerHTML = `<td>${e.seq}</td><td>${e.type}</td><td>${fmtTime(e.at)}</td>` +
      `<td class="mono">${escapeHtml(e.idem_key || "")}</td><td>${escapeHtml(e.summary)}</td>`;
    tb.appendChild(tr);
  }
}

$("refresh-events").addEventListener("click", refreshEvents);

async function init() {
  resizeCanvas();
  try {
    await refreshPackages();
    await refreshRegions();
    await refreshBuilds();
    await refreshEvents();
    draw();
  } catch (err) {
    toast("初始化失败: " + err.message, "error");
  }
}
init();
