"use strict";

(function () {
  const POLL_MS = 3000;
  const MIN = 60000;
  const $ = (id) => document.getElementById(id);
  const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  const state = {
    snapshot: null, lastOk: 0, offline: false,
    repo: "all", filter: "all", q: "", sel: null, tab: "workspaces",
    confirm: null, busy: false, panelError: null,
    gc: {}, disk: {},
  };

  // ---------- formatting ----------
  const LABEL = { assigned: "Assigned", available: "Available", preparing: "Preparing", returning: "Returning", failed: "Failed", expired: "Expired", unmanaged: "Worktree" };
  const ORDER = { expired: 0, failed: 1, assigned: 2, returning: 3, preparing: 4, available: 5, unmanaged: 6 };
  const ms = (iso) => (iso ? Date.parse(iso) : NaN);
  const isExpired = (w) => w.lifecycle === "assigned" && ms(w.expiresAt) < Date.now();
  const statusOf = (w) => (w.lifecycle ? (isExpired(w) ? "expired" : w.lifecycle) : "unmanaged");
  const needsAttention = (w) => ["failed", "expired"].includes(statusOf(w));
  const detached = (w) => !w.branch || w.branch === "(detached)" || w.branch === "detached";
  const baseName = (path) => String(path).split(/[\\/]/).filter(Boolean).pop() || path;
  const nameOf = (w) => (detached(w) ? "Pool slot · " + baseName(w.path).replace(/^.*-ruk-/, "") : w.branch);
  const keyOf = (repo, w) => repo.root + "\n" + w.path;
  const dur = (delta) => {
    const s = Math.floor(Math.abs(delta) / 1000), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
    if (h >= 48) return Math.floor(h / 24) + "d";
    if (h) return h + "h " + m + "m";
    if (m >= 10) return m + "m";
    if (!m) return (s % 60) + "s";
    return m + "m " + String(s % 60).padStart(2, "0") + "s";
  };
  const leaseLength = (minutes) => (minutes >= 60 ? dur(minutes * MIN).replace(/ 0m$/, "") : Math.max(1, Math.round(minutes)) + "m");
  const renewMinutes = (w) => (w.leaseMinutes > 0 ? Math.max(1, Math.round(w.leaseMinutes)) : 0);
  const modeLabel = (mode) => ({ "managed-install": "Managed install", shared: "Shared store" }[mode] || capitalize(String(mode || "prepared").replace(/-/g, " ")));
  const coarse = (delta) => {
    const m = Math.floor(Math.abs(delta) / MIN);
    if (m < 1) return "under a minute";
    if (m < 60) return m + "m";
    return dur(m * MIN);
  };
  const ago = (iso) => {
    const t = ms(iso);
    if (Number.isNaN(t)) return "—";
    const m = Math.floor((Date.now() - t) / MIN);
    return m < 1 ? "now" : coarse(Date.now() - t) + " ago";
  };
  const clock = (iso) => new Date(ms(iso)).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const bytes = (n) => {
    if (!Number.isFinite(n)) return "—";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(n >= 10 ? 0 : 1) : n) + " " + units[i];
  };
  const shortPath = (path) => {
    const parts = String(path).split(/[\\/]/).filter(Boolean);
    return parts.length > 3 ? "…/" + parts.slice(-3).join("/") : path;
  };
  const statusHTML = (s) => `<span class="status"><span class="dot ${s}"></span>${LABEL[s]}</span>`;
  const capitalize = (text) => String(text || "").replace(/^./, (c) => c.toUpperCase());

  // ---------- data ----------
  const repos = () => (state.snapshot ? state.snapshot.repositories : []);
  const currentRepo = () => repos().find((r) => r.root === state.repo) || null;
  const scopedRepos = () => (state.repo === "all" ? repos() : repos().filter((r) => r.root === state.repo));
  const rows = () => scopedRepos().flatMap((repo) => repo.workspaces.map((w) => ({ repo, w, key: keyOf(repo, w) })));
  const selected = () => rows().find((row) => row.key === state.sel) || null;

  async function fetchSnapshot() {
    try {
      const response = await fetch("/api/snapshot", { cache: "no-store", credentials: "same-origin" });
      if (response.status === 401) return sessionLost();
      if (!response.ok) throw new Error("HTTP " + response.status);
      state.snapshot = await response.json();
      state.lastOk = Date.now();
      state.offline = false;
      if (state.repo !== "all" && !currentRepo()) state.repo = "all";
      render();
    } catch (err) {
      state.offline = true;
      renderUpdated();
    }
  }

  async function act(action) {
    const response = await fetch("/api/actions", {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(action),
    });
    if (response.status === 401) { sessionLost(); throw { code: "UNAUTHORIZED", message: "Session expired" }; }
    let body = null;
    try { body = await response.json(); } catch (err) { body = null; }
    if (!response.ok) throw (body && body.error) || { code: "OPERATION_FAILED", message: "Request failed (HTTP " + response.status + ")" };
    return body || {};
  }

  function sessionLost() {
    const banner = $("banner");
    banner.textContent = "Session ended. Open the address printed by ruk ui again.";
    banner.hidden = false;
    clearInterval(timers.poll);
  }

  function toast(message) {
    const el = $("toast");
    el.textContent = message;
    el.hidden = false;
    clearTimeout(toast.timer);
    toast.timer = setTimeout(() => { el.hidden = true; }, 2400);
  }

  const setHTML = (el, html) => { if (el.innerHTML !== html) el.innerHTML = html; };

  // ---------- rendering ----------
  function renderUpdated() {
    const el = $("updated");
    el.classList.toggle("offline", state.offline);
    if (state.offline) { el.textContent = "Offline · retrying"; return; }
    if (!state.lastOk) { el.textContent = "Loading"; return; }
    const s = Math.round((Date.now() - state.lastOk) / 1000);
    el.textContent = s < 2 ? "Updated now" : "Updated " + s + "s ago";
  }

  function renderTop() {
    const snap = state.snapshot;
    $("host").textContent = snap ? snap.host + " · " + location.host : "";
    const repo = currentRepo();
    $("crumb").textContent = repo ? repo.root : "All repositories";
  }

  function renderNav() {
    const item = (id, label, list, title) => {
      const bad = list.filter(needsAttention).length;
      return `<li><button type="button" data-repo="${esc(id)}" aria-current="${state.repo === id}" title="${esc(title)}">
        <span>${esc(label)}</span><span class="c">${bad ? `<span class="flag">${bad}</span> · ` : ""}${list.length}</span></button></li>`;
    };
    const all = repos().flatMap((r) => r.workspaces);
    setHTML($("repoList"), item("all", "All", all, "All repositories") +
      repos().map((r) => item(r.root, r.name, r.workspaces, r.error ? r.root + " — " + r.error : r.root)).join(""));
  }

  function renderStats() {
    const list = rows().map((row) => row.w);
    const n = (s) => list.filter((w) => statusOf(w) === s).length;
    const attention = n("failed") + n("expired");
    const btn = (k, text, v, cls) => `<button class="stat ${cls || ""}" type="button" data-filter="${k}" aria-pressed="${state.filter === k}"><b>${v}</b>${text}</button>`;
    let html = btn("all", "workspaces", list.length) +
      btn("assigned", "assigned", n("assigned")) +
      btn("available", "available", n("available")) +
      (n("preparing") ? btn("preparing", "preparing", n("preparing")) : "") +
      (n("returning") ? btn("returning", "returning", n("returning")) : "") +
      (n("unmanaged") ? btn("unmanaged", "worktrees", n("unmanaged")) : "") +
      btn("attention", "need attention", attention, attention ? "bad" : "");
    const repo = currentRepo();
    if (repo) {
      const disk = state.disk[repo.root];
      html += `<span class="sep"></span>`;
      if (disk && disk.result) {
        const d = disk.result.disk || {};
        html += `<span class="stat static"><b>${bytes(d.projectionBytes)}</b>packages</span><span class="stat static"><b>${bytes(d.estimatedBytesAvoided)}</b>saved by sharing</span>`;
      } else if (disk && disk.error) {
        html += `<span class="stat static alert">${esc(disk.error.message)}</span>`;
      }
      html += `<button class="ghost" type="button" id="measureDisk" ${disk && disk.loading ? "disabled" : ""}>${disk && disk.loading ? "Measuring…" : disk && disk.result ? "Measure again" : "Measure disk"}</button>`;
    }
    setHTML($("stats"), html);
  }

  function leaseHTML(w) {
    if (w.lifecycle !== "assigned") return `<span class="t3">—</span>`;
    const left = ms(w.expiresAt) - Date.now();
    if (Number.isNaN(left)) return `<span class="t3">—</span>`;
    const cls = left < 0 ? "bad" : left < 15 * MIN ? "warn" : "";
    return `<span class="lease num ${cls}">${left < 0 ? "−" + dur(left) : dur(left)}</span>${w.autoRenewing ? `<span class="auto">auto</span>` : ""}`;
  }

  function renderRows() {
    const all = state.repo === "all";
    setHTML($("wsHead"), `<th>Branch</th>${all ? '<th class="opt">Repository</th>' : ""}<th>Status</th><th>Owner</th><th>Lease</th><th class="opt">Last active</th><th class="r opt">Processes</th>`);
    const q = state.q.trim().toLowerCase();
    const list = rows()
      .filter(({ w }) => state.filter === "all" || (state.filter === "attention" ? needsAttention(w) : statusOf(w) === state.filter))
      .filter(({ repo, w }) => !q || [nameOf(w), w.owner, w.path, repo.name, w.assignmentId].join(" ").toLowerCase().includes(q))
      .sort((a, b) => ORDER[statusOf(a.w)] - ORDER[statusOf(b.w)] ||
        (ms(a.w.expiresAt) || Infinity) - (ms(b.w.expiresAt) || Infinity) || a.w.path.localeCompare(b.w.path));
    let html;
    if (!state.snapshot) html = `<tr><td colspan="7" class="empty">Loading workspaces…</td></tr>`;
    else if (!list.length) html = `<tr><td colspan="7" class="empty">${repos().length ? "No workspaces match." : "No Ruk workspaces on this machine yet. Run <span class=\"id\">ruk acquire &lt;branch&gt;</span> or <span class=\"id\">ruk warm --count 2</span> in a repository."}</td></tr>`;
    else html = list.map(({ repo, w, key }) => `<tr data-key="${esc(key)}" tabindex="0" aria-selected="${state.sel === key}">
      <td><span class="${detached(w) ? "t2" : "branch"}">${esc(nameOf(w))}</span></td>
      ${all ? `<td class="t2 opt">${esc(repo.name)}</td>` : ""}
      <td>${statusHTML(statusOf(w))}</td>
      <td class="id t2">${w.owner ? esc(w.owner) : `<span class="t3">—</span>`}</td>
      <td>${leaseHTML(w)}</td>
      <td class="num t2 opt">${ago(w.lastActivityAt || w.assignedAt)}</td>
      <td class="num t2 r opt">${w.processes.length || `<span class="t3">—</span>`}</td></tr>`).join("");
    setHTML($("rows"), html);
  }

  function cliFor(repo, w) {
    const s = statusOf(w);
    if (s === "expired") return "ruk release " + w.assignmentId + " --force";
    if (s === "assigned") return "ruk renew " + w.assignmentId + (renewMinutes(w) ? " --ttl " + renewMinutes(w) : "");
    if (s === "unmanaged") return "ruk remove " + w.path;
    if (s === "available" || s === "failed") return "ruk gc --apply";
    return null;
  }

  function renderPanel() {
    const panel = $("panel");
    const row = state.tab === "workspaces" ? selected() : null;
    $("content").classList.toggle("split", !!row);
    panel.hidden = !row;
    if (!row) { panel.innerHTML = ""; return; }
    const { repo, w } = row;
    const s = statusOf(w);
    const left = ms(w.expiresAt) - Date.now();
    const kv = [
      ["Repository", esc(repo.name)],
      ["Path", `<span class="id">${esc(w.path)}</span>`],
      detached(w) ? ["HEAD", `<span class="id">${esc((w.head || "").slice(0, 12))}</span>`] : null,
      w.owner ? ["Owner", `<span class="id">${esc(w.owner)}</span>`] : null,
      w.assignmentId ? ["Assignment", `<span class="id">${esc(w.assignmentId)}</span>`] : null,
      w.lifecycle === "assigned" && !Number.isNaN(left) ? ["Expires", `<span class="num ${left < 0 ? "alert" : ""}">${clock(w.expiresAt)} (${left < 0 ? coarse(left) + " ago" : "in " + coarse(left)})</span>`] : null,
      w.lifecycle === "assigned" ? ["Renewal", w.autoRenewing ? "Automatic while a Ruk command runs" : "Manual"] : null,
      w.leaseMinutes ? ["Lease", leaseLength(w.leaseMinutes)] : null,
      w.ports && Object.keys(w.ports).length ? ["Ports", `<span class="id">${Object.entries(w.ports).map(([k, v]) => esc(k) + " " + esc(v)).join(", ")}</span>`] : null,
      ["Packages", w.prepared ? esc(modeLabel(w.mode)) : "Not prepared"],
      w.source ? ["Created by", `<span class="id">ruk ${esc(w.source)}</span>`] : null,
      ["Last active", ago(w.lastActivityAt || w.assignedAt)],
    ].filter(Boolean);

    let html = `<div class="panel-head"><div><h3>${esc(nameOf(w))}</h3>${statusHTML(s)}</div><button class="ghost" type="button" data-act="close" aria-label="Close details">Close</button></div>`;
    if (s === "failed" && w.failure) html += `<section><div class="alert">Failed</div><pre class="log">${esc(w.failure)}</pre></section>`;
    if (s === "expired") html += `<section><div class="alert">Lease expired. Still held by ${esc(w.owner || "its owner")}.</div></section>`;
    if (s === "preparing") html += `<section class="note">Preparing dependencies.</section>`;
    if (s === "returning") html += `<section class="note">Releasing.</section>`;
    html += `<section><dl class="kv">${kv.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join("")}</dl></section>`;
    if (w.processes.length) {
      html += `<section><p class="label">Processes</p><ul class="procs">${w.processes.map((p) => `<li><span class="num t3">${esc(p.pid)}</span><code title="${esc((p.command || []).join(" "))}">${esc((p.command || []).join(" ") || "—")}</code></li>`).join("")}</ul></section>`;
    }

    let actions = "";
    if (state.confirm) {
      actions = `<div class="confirm"><p>${state.confirm.text}</p><div class="actions">
        <button class="btn danger-solid" type="button" data-act="confirm" ${state.busy ? "disabled" : ""}>${esc(state.confirm.label)}</button>
        <button class="btn" type="button" data-act="cancel" ${state.busy ? "disabled" : ""}>Cancel</button></div></div>`;
    } else if (s === "assigned" || s === "expired") {
      actions = `<div class="actions"><button class="btn primary" type="button" data-act="renew" ${state.busy ? "disabled" : ""}>${renewMinutes(w) ? "Renew " + leaseLength(renewMinutes(w)) : "Renew"}</button>
        <button class="btn ${s === "expired" ? "danger" : ""}" type="button" data-act="${s === "expired" ? "force" : "release"}" ${state.busy ? "disabled" : ""}>${s === "expired" ? "Force release" : "Release"}</button></div>`;
    } else if (s === "unmanaged") {
      actions = `<div class="actions"><button class="btn danger" type="button" data-act="remove" ${state.busy ? "disabled" : ""}>Remove</button></div>`;
    } else if (s === "available" || s === "failed") {
      actions = `<p class="note">Pool workspaces are removed by <button class="link" type="button" data-act="cleanup">Cleanup</button>.</p>`;
    }
    if (state.panelError) actions += `<div class="error-box">${esc(state.panelError.message)}<span class="id">${esc(state.panelError.code)}</span></div>`;
    if (actions) html += `<section class="confirm">${actions}</section>`;
    const cli = cliFor(repo, w);
    if (cli) html += `<section><p class="label">CLI · run in ${esc(repo.name)}</p><div class="cli"><code id="cliText">${esc(cli)}</code><button class="ghost" type="button" data-act="copy">Copy</button></div></section>`;
    setHTML(panel, html);
  }

  function renderCleanup() {
    const view = $("view-cleanup");
    if (state.tab !== "cleanup") return;
    const list = scopedRepos();
    if (!state.snapshot) { setHTML(view, `<div class="empty">Loading…</div>`); return; }
    if (!list.length) { setHTML(view, `<div class="empty">No repositories.</div>`); return; }
    setHTML(view, list.map((repo) => {
      const gc = state.gc[repo.root] || {};
      const byPath = Object.fromEntries(repo.workspaces.map((w) => [w.path, w]));
      const label = (path) => { const w = byPath[path]; return w ? esc(nameOf(w)) : `<span class="id">${esc(shortPath(path))}</span>`; };
      let body = "";
      if (gc.loading) body = `<div class="gc-foot t3">Checking…</div>`;
      else if (gc.error) body = `<div class="gc-foot"><div class="error-box">${esc(gc.error.message)}<span class="id">${esc(gc.error.code)}</span></div></div>`;
      else if (gc.preview) {
        const removed = gc.preview.removed || [], expired = gc.preview.expired || [];
        const count = removed.length + (gc.force ? expired.length : 0);
        let table = "";
        if (removed.length || expired.length) {
          table = `<div class="gc-body"><table><thead><tr><th>Workspace</th><th>Status</th><th>Reason</th></tr></thead><tbody>` +
            removed.map((r) => `<tr><td>${label(r.path)}</td><td>${statusHTML(r.lifecycle in LABEL ? r.lifecycle : "unmanaged")}</td><td class="t2">${esc(capitalize(r.reason))}</td></tr>`).join("") +
            (expired.length ? `<tr class="group"><td colspan="3">Expired leases · ${gc.force ? "included" : "kept unless included"}</td></tr>` +
              expired.map((r) => `<tr class="${gc.force ? "" : "t3"}"><td>${label(r.path)}</td><td>${statusHTML("expired")}</td><td class="t2">Expired ${esc(ago(r.expiresAt))} · ${esc((byPath[r.path] && byPath[r.path].owner) || r.assignmentId)}</td></tr>`).join("") : "") +
            `</tbody></table></div>`;
        }
        const command = "ruk gc --apply" + (gc.force && expired.length ? " --force-expired" : "");
        let foot;
        if (gc.confirm) {
          foot = `<span>Delete ${count} workspace${count === 1 ? "" : "s"}? Uncommitted changes in them are lost.</span><span class="grow"></span>
            <button class="btn" type="button" data-gc="cancel" data-root="${esc(repo.root)}" ${state.busy ? "disabled" : ""}>Cancel</button>
            <button class="btn danger-solid" type="button" data-gc="apply" data-root="${esc(repo.root)}" ${state.busy ? "disabled" : ""}>Delete ${count}</button>`;
        } else if (!removed.length && !expired.length) {
          foot = `<span class="t3">Nothing to collect.</span>`;
        } else {
          foot = (expired.length ? `<label class="toggle"><input type="checkbox" data-gc="force" data-root="${esc(repo.root)}" ${gc.force ? "checked" : ""}> Include expired leases</label>` : "") +
            `<code class="id t3">${command}</code><span class="grow"></span>
            <button class="btn primary" type="button" data-gc="confirm" data-root="${esc(repo.root)}" ${count ? "" : "disabled"}>Collect ${count}</button>`;
        }
        body = table + `<div class="gc-foot">${foot}</div>`;
      } else {
        body = `<div class="gc-foot"><button class="btn" type="button" data-gc="preview" data-root="${esc(repo.root)}">Check</button><span class="t3">Shows what <span class="id">ruk gc</span> would remove. Nothing changes until you collect.</span></div>`;
      }
      return `<section class="gc-repo"><div class="gc-head"><h3>${esc(repo.name)}</h3><span class="id t3">${esc(repo.root)}</span><span class="grow"></span>
        ${gc.preview && !gc.confirm ? `<button class="ghost" type="button" data-gc="preview" data-root="${esc(repo.root)}">Refresh</button>` : ""}</div>${body}</section>`;
    }).join(""));
  }

  function render() {
    renderTop(); renderNav(); renderStats(); renderRows(); renderPanel(); renderCleanup(); renderUpdated();
  }

  // ---------- actions ----------
  async function run(action, success) {
    state.busy = true; state.panelError = null; render();
    try {
      const result = await act(action);
      state.confirm = null;
      if (success) success(result);
      await fetchSnapshot();
      return result;
    } catch (err) {
      state.panelError = { code: err.code || "OPERATION_FAILED", message: err.message || String(err) };
      return null;
    } finally {
      state.busy = false; render();
    }
  }

  async function preview(root) {
    state.gc[root] = { ...(state.gc[root] || {}), loading: true, error: null, confirm: false };
    renderCleanup();
    try {
      const result = await act({ kind: "gc-preview", repository: root });
      state.gc[root] = { preview: result.result || {}, force: (state.gc[root] || {}).force || false };
    } catch (err) {
      state.gc[root] = { error: { code: err.code || "OPERATION_FAILED", message: err.message || String(err) } };
    }
    renderCleanup();
  }

  async function collect(root) {
    const gc = state.gc[root];
    state.busy = true; renderCleanup();
    try {
      const result = await act({ kind: "gc-apply", repository: root, forceExpired: !!gc.force });
      const removed = ((result.result || {}).removed || []).length;
      toast("Collected " + removed + " workspace" + (removed === 1 ? "" : "s"));
      state.gc[root] = { force: false };
      await fetchSnapshot();
      await preview(root);
    } catch (err) {
      state.gc[root] = { ...gc, confirm: false, error: { code: err.code || "OPERATION_FAILED", message: err.message || String(err) }, preview: null };
    } finally {
      state.busy = false; renderCleanup();
    }
  }

  async function measureDisk(root) {
    state.disk[root] = { loading: true };
    renderStats();
    try {
      const result = await act({ kind: "disk", repository: root });
      state.disk[root] = { result: result.result || {} };
    } catch (err) {
      state.disk[root] = { error: { code: err.code, message: err.message || String(err) } };
    }
    renderStats();
  }

  // ---------- events ----------
  $("repoList").addEventListener("click", (event) => {
    const button = event.target.closest("[data-repo]");
    if (!button) return;
    state.repo = button.dataset.repo; state.filter = "all"; state.sel = null; state.confirm = null; state.panelError = null;
    render();
    const repo = currentRepo();
    if (state.tab === "cleanup" && repo && !state.gc[repo.root]) preview(repo.root);
  });

  $("stats").addEventListener("click", (event) => {
    if (event.target.id === "measureDisk") { measureDisk(state.repo); return; }
    const button = event.target.closest("[data-filter]");
    if (!button) return;
    state.filter = state.filter === button.dataset.filter ? "all" : button.dataset.filter;
    renderStats(); renderRows();
  });

  $("search").addEventListener("input", (event) => { state.q = event.target.value; renderRows(); });

  const openRow = (tr) => { state.sel = tr.dataset.key; state.confirm = null; state.panelError = null; renderRows(); renderPanel(); };
  $("rows").addEventListener("click", (event) => { const tr = event.target.closest("tr[data-key]"); if (tr) openRow(tr); });
  $("rows").addEventListener("keydown", (event) => {
    const tr = event.target.closest("tr[data-key]");
    if (!tr) return;
    if (event.key === "Enter") openRow(tr);
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const next = event.key === "ArrowDown" ? tr.nextElementSibling : tr.previousElementSibling;
      if (next) next.focus();
    }
  });
  const closePanel = () => { state.sel = null; state.confirm = null; state.panelError = null; renderRows(); renderPanel(); };
  document.addEventListener("keydown", (event) => { if (event.key === "Escape" && state.sel && !state.busy) closePanel(); });

  $("panel").addEventListener("click", async (event) => {
    const button = event.target.closest("[data-act]");
    const row = selected();
    if (!button || !row) return;
    const { repo, w } = row;
    const ask = (kind, label, text) => { state.confirm = { kind, label, text }; state.panelError = null; renderPanel(); };
    switch (button.dataset.act) {
      case "close": closePanel(); break;
      case "cleanup": showTab("cleanup"); break;
      case "renew":
        await run({ kind: "renew", repository: repo.root, assignmentId: w.assignmentId, ttlMinutes: renewMinutes(w) }, (result) => {
          const expires = result.result && result.result.expiresAt;
          toast(expires ? "Renewed until " + clock(expires) : "Renewed");
        });
        break;
      case "release": ask("release", "Release", `Release <b>${esc(nameOf(w))}</b>? ${esc(w.owner || "Its owner")} loses access and the workspace returns to the pool.`); break;
      case "force": ask("force", "Force release", `Force-release <b>${esc(nameOf(w))}</b>? If ${esc(w.owner || "its owner")} is still working, its next Ruk command fails.`); break;
      case "remove": ask("remove", "Remove", `Remove <span class="id">${esc(w.path)}</span>? Ruk refuses if it has uncommitted changes.`); break;
      case "cancel": state.confirm = null; state.panelError = null; renderPanel(); break;
      case "confirm": {
        const kind = state.confirm.kind;
        if (kind === "remove") {
          await run({ kind: "remove", repository: repo.root, path: w.path }, () => { toast("Removed"); state.sel = null; });
        } else {
          await run({ kind: "release", repository: repo.root, assignmentId: w.assignmentId, force: kind === "force" }, () => toast("Released"));
        }
        break;
      }
      case "copy": {
        const el = $("cliText");
        try { await navigator.clipboard.writeText(el.textContent); toast("Copied"); }
        catch (err) {
          const range = document.createRange(); range.selectNodeContents(el);
          const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
          toast("Selected");
        }
        break;
      }
    }
  });

  $("view-cleanup").addEventListener("click", (event) => {
    const button = event.target.closest("button[data-gc]");
    if (!button) return;
    const root = button.dataset.root, gc = state.gc[root] || {};
    if (button.dataset.gc === "preview") preview(root);
    if (button.dataset.gc === "confirm") { state.gc[root] = { ...gc, confirm: true }; renderCleanup(); }
    if (button.dataset.gc === "cancel") { state.gc[root] = { ...gc, confirm: false }; renderCleanup(); }
    if (button.dataset.gc === "apply") collect(root);
  });
  $("view-cleanup").addEventListener("change", (event) => {
    if (event.target.dataset.gc !== "force") return;
    const root = event.target.dataset.root;
    state.gc[root] = { ...(state.gc[root] || {}), force: event.target.checked };
    renderCleanup();
  });

  const TABS = ["workspaces", "cleanup"];
  function showTab(tab) {
    state.tab = tab;
    TABS.forEach((name) => {
      $("tab-" + name).setAttribute("aria-selected", String(name === tab));
      $("view-" + name).hidden = name !== tab;
    });
    $("stats").hidden = tab !== "workspaces";
    $("search").hidden = tab !== "workspaces";
    renderPanel(); renderCleanup();
    const repo = currentRepo();
    if (tab === "cleanup" && repo && !state.gc[repo.root]) preview(repo.root);
  }
  TABS.forEach((name) => $("tab-" + name).addEventListener("click", () => showTab(name)));

  const THEMES = ["System", "Light", "Dark"];
  let theme = "System";
  try { theme = localStorage.getItem("ruk-ui-theme") || "System"; } catch (err) { theme = "System"; }
  if (!THEMES.includes(theme)) theme = "System";
  function applyTheme() {
    if (theme === "System") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", theme.toLowerCase());
    $("themeBtn").textContent = theme;
  }
  $("themeBtn").addEventListener("click", () => {
    theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
    try { localStorage.setItem("ruk-ui-theme", theme); } catch (err) { /* per-browser convenience only */ }
    applyTheme();
  });
  applyTheme();

  const timers = {};
  document.addEventListener("visibilitychange", () => { if (!document.hidden) fetchSnapshot(); });
  timers.poll = setInterval(() => { if (!document.hidden) fetchSnapshot(); }, POLL_MS);
  timers.tick = setInterval(() => { renderRows(); renderUpdated(); }, 1000);
  render();
  fetchSnapshot();
})();
