'use strict';
// acp web: renders with DOM APIs and textContent only. Every change goes preview -> apply, and the
// server refuses an apply whose preview is stale.
(() => {
  // ------------------------------------------------------------ helpers

  function h(tag, props, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const kid of kids.flat(Infinity)) {
      if (kid == null || kid === false) continue;
      el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    }
    return el;
  }

  function svg(tag, attrs, ...kids) {
    const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
    for (const [k, v] of Object.entries(attrs || {})) el.setAttribute(k, v);
    for (const kid of kids) el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    return el;
  }

  const store = {
    get(k) { try { return sessionStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { sessionStorage.setItem(k, v); } catch { /* private window */ } },
    local(k, v) {
      try {
        if (v === undefined) return localStorage.getItem(k);
        localStorage.setItem(k, v);
      } catch { /* storage blocked */ }
      return null;
    },
  };

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const toArray = (v) => (Array.isArray(v) ? v : []);

  // ------------------------------------------------------------ state

  const SECTIONS = [
    { id: 'overview', label: 'Tổng quan' },
    { id: 'projects', label: 'Dự án & Skill', module: 'projects' },
    { id: 'agentkit', label: 'AgentKit & Runtime', module: 'agentkit' },
    { id: 'hooks', label: 'Hook, MCP & Cài đặt', module: 'hooks' },
  ];
  const MODULE_LABEL = { projects: 'Dự án & Skill', agentkit: 'AgentKit & Runtime', hooks: 'Hook, MCP & Cài đặt' };
  const SEVERITY = {
    error: { label: 'Lỗi', rank: 0 },
    warn: { label: 'Cần sửa', rank: 1 },
    decide: { label: 'Bạn quyết định', rank: 2 },
    info: { label: 'Để ý', rank: 3 },
  };
  const SAFETY = {
    safe: { label: 'An toàn', cls: 'ok', note: 'Chỉ thêm hoặc sửa liên kết acp tạo ra; chạy lại bao nhiêu lần cũng được.' },
    backup: { label: 'Có bản sao lưu', cls: 'accent', note: 'acp chép lại tệp cũ vào runtime/backups trước khi đổi.' },
    confirm: { label: 'Cần bạn xác nhận', cls: 'decide', note: 'Thay đổi này đổi lựa chọn của bạn hoặc tải phần mềm mới. Đọc kỹ rồi mới áp dụng.' },
  };
  // What each change leaves alone, from how acp is built: it never removes or replaces files git tracks.
  const KEEPS = {
    link: ['Tệp Git đang theo dõi', 'Skill gốc trong control plane', 'Mã nguồn của dự án'],
    sync: ['Tệp Git đang theo dõi', 'Skill gốc trong control plane', 'Mã nguồn của dự án'],
    adopt: ['Tệp Git đang theo dõi', 'Bản cũ được cất vào runtime/backups, không bị xoá'],
    'skills.apply': ['Thư mục skill trên máy', 'Các dự án khác'],
    'kit.apply': ['Skill cá nhân của dự án', 'Các dự án khác'],
    'global.apply': ['Thư mục skill thật (chỉ đổi liên kết acp tạo)', 'Các dự án'],
    'hooks.install': ['Các hook khác trong tệp cài đặt'],
    'hooks.uninstall': ['Các hook khác trong tệp cài đặt'],
    'setup.install-ak': ['Skill và dự án của bạn'],
    upgrade: ['Skill cá nhân', 'Tệp Git đang theo dõi'],
    add: ['Mã nguồn trong repo; acp chỉ thêm khai báo và liên kết'],
  };
  // How a scan fix maps to the dashboard's preview and apply kinds.
  const FIX = {
    link: (a) => ({ preview: 'link.preview', apply: 'link', args: { path: a[0] } }),
    sync: (a) => ({ preview: 'sync.preview', apply: 'sync', args: { id: a[0] } }),
    adopt: (a) => ({ preview: 'adopt.preview', apply: 'adopt', args: { id: a[0] } }),
    add: (a) => ({ preview: 'add.preview', apply: 'add', args: { path: a[0] } }),
    global: () => ({ preview: 'global.preview', apply: 'global.apply', args: {} }),
    'hooks.install': () => ({ preview: 'hooks.install.preview', apply: 'hooks.install', args: {} }),
    'setup.install-ak': () => ({ preview: 'setup.install-ak.preview', apply: 'setup.install-ak', args: {} }),
    upgrade: (a) => {
      const args = {};
      for (let i = 0; i + 1 < a.length; i += 2) {
        if (a[i] === '--channel') args.channel = a[i + 1];
        if (a[i] === '--kit') args.kit = a[i + 1];
      }
      return { preview: 'upgrade.preview', apply: 'upgrade', args };
    },
    'kit.add': (a) => ({ preview: 'kit.preview', apply: 'kit.apply', args: { id: a[0] } }),
    'skills.set': (a) => {
      const args = { id: a[0] };
      for (let i = 1; i + 1 < a.length; i += 2) {
        if (a[i] === '--add') args.add = a[i + 1].split(',');
        if (a[i] === '--remove') args.remove = a[i + 1].split(',');
      }
      return { preview: 'skills.preview', apply: 'skills.apply', args };
    },
  };

  let token = store.get('acp.token');
  const state = {
    section: store.get('acp.section') || 'overview',
    tech: store.local('acp.tech') === '1',
    status: null, setup: null, hooks: null, global: null, scan: null, scanAt: null,
    glossary: {}, scanning: false, loadError: null,
  };

  // ------------------------------------------------------------ API

  async function api(path, opts = {}) {
    const headers = {};
    if (token) headers.Authorization = 'Bearer ' + token;
    if (opts.body) headers['Content-Type'] = 'application/json';
    let res;
    try {
      res = await fetch(path, { method: opts.method || 'GET', headers, body: opts.body ? JSON.stringify(opts.body) : undefined });
    } catch {
      return { status: 0, data: { error: 'Không kết nối được acp web. Nó còn chạy trong terminal không?' } };
    }
    let data = null;
    try { data = await res.json(); } catch { /* empty body */ }
    if (res.status === 401 && path !== '/api/session') {
      token = null;
      store.set('acp.token', '');
      renderSignIn();
    }
    return { status: res.status, data };
  }

  async function runOp(kind, args, digest) {
    const r = await api('/api/run', { method: 'POST', body: { kind, args: args || {}, digest } });
    if (r.status !== 202) return { error: (r.data && r.data.error) || 'HTTP ' + r.status, status: r.status };
    for (;;) {
      await sleep(300);
      const o = await api('/api/ops/' + r.data.op);
      if (o.status !== 200) return { error: (o.data && o.data.error) || 'HTTP ' + o.status, status: o.status };
      if (o.data.state === 'done') return o.data;
    }
  }

  const dataOf = (env) => (env && env.data) || {};

  async function load(what) {
    state.loadError = null;
    const jobs = {
      status: () => api('/api/status'),
      setup: () => api('/api/setup'),
      hooks: () => api('/api/hooks'),
      global: () => api('/api/global'),
    };
    const names = what || Object.keys(jobs);
    const results = await Promise.all(names.map((n) => jobs[n]()));
    names.forEach((n, i) => {
      const r = results[i];
      if (r.status === 200) state[n] = r.data;
      else state.loadError = (r.data && r.data.error) || 'HTTP ' + r.status;
    });
  }

  async function loadScan() {
    const r = await api('/api/scan');
    if (r.status === 200) {
      state.scan = r.data;
      state.scanAt = new Date();
    }
  }

  async function loadGlossary() {
    const r = await api('/api/explain');
    if (r.status === 200) for (const t of toArray(r.data.glossary)) state.glossary[t.term] = t.meaning;
  }

  // ------------------------------------------------------------ small pieces

  function term(word, label) {
    const btn = h('button', { class: 'term', type: 'button', text: label || word, 'aria-haspopup': 'dialog' });
    btn.addEventListener('click', (ev) => {
      ev.stopPropagation();
      closePop();
      const meaning = state.glossary[word];
      if (!meaning) return;
      const pop = h('div', { class: 'pop', role: 'tooltip', id: 'pop' }, h('b', { text: word }), meaning);
      document.body.append(pop);
      const r = btn.getBoundingClientRect();
      const left = Math.min(window.scrollX + r.left, window.scrollX + document.documentElement.clientWidth - pop.offsetWidth - 16);
      pop.style.left = Math.max(8, left) + 'px';
      pop.style.top = window.scrollY + r.bottom + 6 + 'px';
      btn.setAttribute('aria-describedby', 'pop');
    });
    return btn;
  }

  function closePop() {
    const pop = document.getElementById('pop');
    if (pop) pop.remove();
    document.querySelectorAll('[aria-describedby="pop"]').forEach((el) => el.removeAttribute('aria-describedby'));
  }

  function tag(cls, text) { return h('span', { class: 'tag ' + cls, text }); }
  function tech(...kids) { return state.tech ? kids : null; }
  function btn(label, onclick, cls) { return h('button', { class: 'btn ' + (cls || ''), type: 'button', onclick }, label); }

  function toast(text) {
    const old = document.querySelector('.toast');
    if (old) old.remove();
    const t = h('div', { class: 'toast', role: 'status', text });
    document.body.append(t);
    setTimeout(() => t.remove(), 3200);
  }

  function findings(module) {
    const all = toArray(dataOf(state.scan).findings);
    const list = module ? all.filter((f) => f.module === module) : all;
    return list.slice().sort((a, b) => SEVERITY[a.severity].rank - SEVERITY[b.severity].rank);
  }

  function counts(list) {
    const c = { error: 0, warn: 0, decide: 0, info: 0 };
    for (const f of list) c[f.severity]++;
    return c;
  }

  // ------------------------------------------------------------ sheet

  let lastFocus = null;

  function openSheet(title, body, footer) {
    closeSheet();
    lastFocus = document.activeElement;
    const close = btn('Đóng', closeSheet, 'quiet');
    const sheet = h('aside', { class: 'sheet', role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
      h('header', {}, h('h2', { text: title }), close),
      h('div', { class: 'body' }, body),
      h('footer', {}, footer || []));
    const overlay = h('div', { class: 'overlay', onclick: closeSheet });
    document.body.append(overlay, sheet);
    close.focus();
    return sheet;
  }

  function closeSheet() {
    document.querySelectorAll('.sheet, .overlay').forEach((el) => el.remove());
    if (lastFocus && lastFocus.isConnected) lastFocus.focus();
    lastFocus = null;
  }

  document.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape') { closePop(); closeSheet(); }
  });
  document.addEventListener('click', closePop);

  // Lines from a preview envelope: what changes, what is wrong, what is already right.
  function previewLines(env) {
    const d = dataOf(env);
    const out = { changes: [], issues: [], ok: [] };
    for (const log of toArray(d.logs)) {
      out.changes.push(...toArray(log.changes));
      out.issues.push(...toArray(log.issues));
      out.ok.push(...toArray(log.ok));
    }
    // acp hooks reports one state per settings file instead of logs.
    for (const t of toArray(d.targets)) {
      const s = t.state || '';
      if (s.startsWith('would install')) out.changes.push('+ Thêm hook SessionStart của acp vào ' + t.name);
      else if (s.startsWith('would uninstall')) out.changes.push('- Gỡ hook SessionStart của acp khỏi ' + t.name);
      else if (s.startsWith('unreadable') || s.includes('unexpected shape')) out.issues.push(t.name + ': không đọc được tệp cài đặt, để nguyên');
      else out.ok.push(s);
    }
    return out;
  }

  function lineList(lines, cls, sign) {
    return h('ul', { class: 'changes ' + cls }, lines.map((l) => {
      const m = /^([+~\-!])\s(.*)$/.exec(l);
      return h('li', {}, h('span', { class: 'sign', text: m ? m[1] : sign }), h('span', { text: m ? m[2] : l }));
    }));
  }

  // openFix previews a change and applies it only from that preview.
  function openFix(title, fix, safety) {
    const s = SAFETY[safety] || SAFETY.safe;
    const body = h('div', { class: 'stack' });
    const apply = btn('Áp dụng', null, 'primary');
    apply.disabled = true;
    const again = btn('Xem trước lại', () => preview(), '');
    again.classList.add('hidden');
    openSheet(title, body, [again, apply]);
    let digest = null;
    let confirmed = safety !== 'confirm';

    async function preview() {
      digest = null;
      confirmed = safety !== 'confirm';
      apply.disabled = true;
      again.classList.add('hidden');
      body.replaceChildren(h('p', { class: 'muted', text: 'Đang xem trước, chưa thay đổi gì…' }));
      const op = await runOp(fix.preview, fix.args);
      if (op.error) {
        body.replaceChildren(h('div', { class: 'notice error', text: op.error }));
        again.classList.remove('hidden');
        return;
      }
      // The server gives no digest for a preview that failed, so Apply stays disabled.
      digest = op.envelope && op.envelope.error ? null : op.digest;
      const lines = previewLines(op.envelope);
      const d = dataOf(op.envelope);
      const parts = [
        h('div', { class: 'row' }, tag(s.cls, s.label), h('span', { class: 'muted', text: s.note })),
      ];
      if (op.envelope && op.envelope.error) {
        parts.push(h('div', { class: 'notice error', text: 'Không xem trước được: ' + op.envelope.error + '. Chưa thể áp dụng.' }));
      }
      parts.push(h('div', { class: 'part' }, h('h3', { text: 'Sẽ thay đổi' }),
        lines.changes.length ? lineList(lines.changes, '', '+') : h('p', { class: 'muted', text: d.text && !lines.ok.length ? 'Xem chi tiết bên dưới.' : 'Không có gì cần đổi.' })));
      if (lines.issues.length) parts.push(h('div', { class: 'part' }, h('h3', { text: 'Cần bạn xem' }), lineList(lines.issues, 'issues', '!')));
      parts.push(h('div', { class: 'part' }, h('h3', { text: 'Không đụng tới' }),
        lineList([...(KEEPS[fix.apply] || []), ...(lines.ok.length ? [lines.ok.length + ' mục đã đúng, giữ nguyên'] : [])], 'keep', '✓')));
      if (safety === 'confirm') {
        const box = h('input', { type: 'checkbox', id: 'confirm-box' });
        box.checked = confirmed;
        box.addEventListener('change', () => { confirmed = box.checked; apply.disabled = !(digest && confirmed); });
        parts.push(h('label', { class: 'confirm', for: 'confirm-box' }, box, h('span', { text: 'Tôi đã đọc phần xem trước và muốn áp dụng thay đổi này.' })));
      }
      parts.push(h('details', {}, h('summary', { text: 'Chi tiết kỹ thuật' }),
        h('pre', { text: ['$ acp ' + (op.envelope.kind || '').replace(/^acp\./, '').replace(/\./g, ' '), d.text || ''].join('\n') })));
      body.replaceChildren(...parts);
      apply.disabled = !(digest && confirmed);
      again.classList.remove('hidden');
    }

    apply.addEventListener('click', async () => {
      apply.disabled = true;
      again.classList.add('hidden');
      const pending = h('p', { class: 'muted', text: 'Đang áp dụng…' });
      body.prepend(pending);
      const op = await runOp(fix.apply, fix.args, digest);
      pending.remove();
      if (op.error) {
        body.prepend(h('div', { class: 'notice error', text: op.status === 409 ? op.error + ' Bấm "Xem trước lại".' : op.error }));
        again.classList.remove('hidden');
        return;
      }
      const env = op.envelope || {};
      const ok = env.exit_code === 0;
      const lines = previewLines(env);
      apply.classList.add('hidden');
      body.replaceChildren(...[
        h('div', { class: 'notice ' + (ok ? 'ok' : 'error'), text: ok ? 'Đã áp dụng.' : 'acp báo lỗi: ' + (env.error || 'xem chi tiết') }),
        lines.changes.length ? h('div', { class: 'part' }, h('h3', { text: 'Đã thay đổi' }), lineList(lines.changes, '', '+')) : null,
        lines.issues.length ? h('div', { class: 'part' }, h('h3', { text: 'Còn lại' }), lineList(lines.issues, 'issues', '!')) : null,
        h('details', {}, h('summary', { text: 'Chi tiết kỹ thuật' }), h('pre', { text: dataOf(env).text || '' })),
      ].filter(Boolean));
      toast(ok ? 'Đã áp dụng. Đang quét lại…' : 'Có lỗi khi áp dụng.');
      await load();
      if (state.scan) await scanNow();
      else render();
    });

    preview();
  }

  function openExplain(f) {
    const parts = [
      ['Chuyện gì', f.what], ['Vì sao quan trọng', f.why], ['Nếu bỏ qua', f.ignore], ['Nên làm gì', f.todo],
    ].map(([title, text]) => h('div', { class: 'part' }, h('h3', { text: title }), h('p', { text: text || '' })));
    const terms = h('div', { class: 'part' });
    const raw = tech(h('details', { open: true }, h('summary', { text: 'Thông tin kỹ thuật' }),
      h('pre', { text: [f.code + '  ' + f.subject, f.raw || '', f.fix ? '$ ' + f.fix.command : ''].filter(Boolean).join('\n') })));
    const copy = btn('Chép để hỏi AI', async () => {
      const text = ['Tôi dùng acp để đồng bộ skill cho trợ lý AI. acp scan báo:', f.title, '',
        'Chuyện gì: ' + f.what, 'Vì sao: ' + f.why, 'Nên làm gì: ' + f.todo, '',
        'Mã: ' + f.code, f.raw ? 'Chi tiết:\n' + f.raw : '', f.fix ? 'Lệnh sửa: ' + f.fix.command : '',
        '', 'Giải thích giúp tôi và cho biết có nên làm theo không.'].filter((l) => l !== null).join('\n');
      try { await navigator.clipboard.writeText(text); toast('Đã chép. Dán vào cuộc trò chuyện với AI.'); } catch { toast('Trình duyệt không cho chép.'); }
    });
    const footer = [copy];
    if (f.fix && FIX[f.fix.kind]) footer.push(btn('Xem cách sửa', () => openFix(f.title, FIX[f.fix.kind](f.fix.args || []), f.fix.safety), 'primary'));
    openSheet(f.title, [h('div', { class: 'row' }, tag(f.severity, SEVERITY[f.severity].label), h('span', { class: 'muted', text: MODULE_LABEL[f.module] })), ...parts, terms, raw], footer);
    api('/api/explain?code=' + encodeURIComponent(f.code)).then((r) => {
      const list = toArray(r.data && r.data.glossary);
      if (!list.length) return;
      terms.append(h('h3', { text: 'Thuật ngữ' }), h('dl', { class: 'facts' }, list.flatMap((t) => [h('dt', { text: t.term }), h('dd', { text: t.meaning })])));
    });
  }

  function findingRow(f) {
    const actions = [btn('Vì sao?', () => openExplain(f), 'quiet')];
    const fixable = f.fix && FIX[f.fix.kind];
    if (fixable) actions.push(btn('Sửa…', () => openFix(f.title, FIX[f.fix.kind](f.fix.args || []), f.fix.safety)));
    // The catalog's todo is written for the terminal; here the Sửa… button is the preview.
    return h('div', { class: 'finding ' + f.severity },
      h('div', { class: 'row' }, tag(f.severity, SEVERITY[f.severity].label), h('span', { class: 'title', text: f.title })),
      h('p', { class: 'muted', text: fixable ? 'Bấm "Sửa…" để xem trước. Chưa có gì thay đổi cho tới khi bạn áp dụng.' : f.todo }),
      tech(h('p', { class: 'cmd muted', text: f.code + ' · ' + f.subject })),
      h('div', { class: 'row' }, actions));
  }

  function findingList(list, emptyText) {
    if (!state.scan) return h('div', { class: 'empty', text: 'Chưa quét. Bấm "Quét ngay" ở Tổng quan để kiểm tra.' });
    if (!list.length) return h('div', { class: 'empty', text: emptyText });
    return h('div', { class: 'findings' }, list.map(findingRow));
  }

  // ------------------------------------------------------------ sections

  function healthSentence() {
    if (!state.scan) return 'Bấm quét để xem máy này còn gì cần sửa.';
    const c = counts(findings());
    const parts = [];
    if (c.error) parts.push(c.error + ' lỗi');
    if (c.warn) parts.push(c.warn + ' việc cần sửa');
    if (c.decide) parts.push(c.decide + ' việc chờ bạn quyết định');
    if (!parts.length) return c.info ? 'Mọi thứ ổn. Có ' + c.info + ' điều nên để ý.' : 'Mọi thứ đang ổn.';
    return 'Có ' + parts.join(', ') + '.';
  }

  function ring(ready, total) {
    const r = 48;
    const len = 2 * Math.PI * r;
    const frac = total ? ready / total : 0;
    return svg('svg', { class: 'ring', viewBox: '0 0 120 120', role: 'img', 'aria-label': ready + ' trên ' + total + ' dự án sẵn sàng' },
      svg('circle', { class: 'track', cx: 60, cy: 60, r, fill: 'none', 'stroke-width': 12 }),
      svg('circle', { class: 'value', cx: 60, cy: 60, r, fill: 'none', 'stroke-width': 12, 'stroke-linecap': 'round',
        'stroke-dasharray': (frac * len).toFixed(1) + ' ' + len.toFixed(1), transform: 'rotate(-90 60 60)' }),
      svg('text', { x: 60, y: 58, 'text-anchor': 'middle', 'font-size': 24, 'font-weight': 600 }, ready + '/' + total),
      svg('text', { x: 60, y: 80, 'text-anchor': 'middle', 'font-size': 11, opacity: 0.7 }, 'sẵn sàng'));
  }

  async function scanNow() {
    state.scanning = true;
    render();
    const op = await runOp('scan', {});
    state.scanning = false;
    if (op.error) toast(op.error);
    else { state.scan = op.envelope; state.scanAt = new Date(); }
    render();
  }

  function overview() {
    const projects = toArray(dataOf(state.status).projects);
    const ready = projects.filter((p) => p.healthy).length;
    const steps = h('ul', { class: 'steps' }, Object.entries(MODULE_LABEL).map(([m, label]) => {
      const n = state.scan && !state.scanning ? findings(m).length : null;
      return h('li', {}, h('span', { class: 'dot ' + (state.scanning ? 'run' : state.scan ? 'done' : '') }),
        h('span', { text: label }), n != null ? h('span', { class: 'muted', text: n ? '· ' + n + ' mục' : '· ổn' }) : null);
    }));
    const when = state.scanAt ? 'Lần quét gần nhất: ' + state.scanAt.toLocaleTimeString('vi-VN', { hour: '2-digit', minute: '2-digit' }) : 'Chưa quét lần nào trong phiên này.';
    const scanBtn = btn(state.scanning ? 'Đang quét…' : 'Quét ngay', scanNow, 'primary big');
    scanBtn.disabled = state.scanning;
    const hero = h('section', { class: 'card hero' }, ring(ready, projects.length),
      h('div', { class: 'stack' },
        h('p', { class: 'sentence', text: healthSentence() }),
        h('p', { class: 'muted' }, when, ' Quét chỉ đọc, không thay đổi gì trên máy.'),
        h('div', { class: 'row' }, scanBtn),
        steps));
    const modules = h('div', { class: 'grid' }, SECTIONS.filter((s) => s.module).map((s) => {
      const list = state.scan ? findings(s.module) : [];
      const c = counts(list);
      return h('button', { class: 'card module', type: 'button', onclick: () => go(s.id) },
        h('h3', { text: s.label }),
        h('span', { class: 'num', text: state.scan ? String(list.length) : '–' }),
        h('div', { class: 'row' },
          c.error ? tag('error', c.error + ' lỗi') : null,
          c.warn ? tag('warn', c.warn + ' cần sửa') : null,
          c.decide ? tag('decide', c.decide + ' quyết định') : null,
          state.scan && !list.length ? tag('ok', 'Ổn') : null));
    }));
    return [hero, modules,
      h('div', { class: 'section-head' }, h('h2', { text: 'Cần chú ý' })),
      findingList(findings(), 'Không có gì cần sửa.')];
  }

  function moduleFindings(module) {
    const list = state.scan ? findings(module) : [];
    if (state.scan && !list.length) return null;
    return [h('h2', { text: 'Cần chú ý ở đây' }), findingList(list, '')];
  }

  function projectsSection() {
    const d = dataOf(state.status);
    const projects = toArray(d.projects);
    const head = h('div', { class: 'section-head' },
      h('p', { class: 'lead' }, 'Mỗi dự án có bản chính và các ', term('worktree'), '. acp gắn ', term('skill'), ' của ',
        term('AgentKit'), ' và skill cá nhân vào cả hai bằng ', term('liên kết'), '.'),
      h('div', { class: 'row' },
        btn('Thêm dự án', openAdd),
        btn('Đồng bộ tất cả', () => openFix('Đồng bộ mọi dự án', { preview: 'sync.preview', apply: 'sync', args: { all: true } }, 'safe'))));
    const cards = projects.map((p) => {
      const ak = p.agentkit || {};
      const facts = [
        ['AgentKit', ak.kit ? [ak.kit, ak.channel, ak.pin].filter(Boolean).join(' · ') : 'Không dùng'],
        ['Skill AgentKit', ak.skills != null ? String(ak.skills) : '–'],
        ['Skill cá nhân', p.personal ? p.personal.linked + '/' + p.personal.total + ' đã gắn' : '–'],
        ['Worktree', p.worktrees ? p.worktrees.linked + '/' + p.worktrees.total + ' đã gắn' : '–'],
      ];
      if (state.tech) facts.push(['Thư mục', p.repo]);
      return h('article', { class: 'card' },
        h('div', { class: 'row' }, h('h3', { text: p.id }), p.healthy ? tag('ok', 'Ổn') : tag('warn', 'Cần đồng bộ')),
        h('dl', { class: 'facts' }, facts.flatMap(([k, v]) => [h('dt', { text: k }), h('dd', { text: v })])),
        toArray(p.notes).length ? h('ul', { class: 'changes issues' }, p.notes.map((n) => h('li', {}, h('span', { class: 'sign', text: '!' }), h('span', { text: n })))) : null,
        h('div', { class: 'row' },
          btn('Đồng bộ', () => openFix('Đồng bộ ' + p.id, { preview: 'sync.preview', apply: 'sync', args: { id: p.id } }, 'safe'), p.healthy ? '' : 'primary'),
          btn('Skill', () => openSkills(p.id)),
          ak.kit ? null : btn('Thêm AgentKit', () => openFix('Thêm AgentKit cho ' + p.id, { preview: 'kit.preview', apply: 'kit.apply', args: { id: p.id } }, 'confirm')),
          btn('Gom bản sao cũ', () => openFix('Gom bản sao cũ trong worktree của ' + p.id, { preview: 'adopt.preview', apply: 'adopt', args: { id: p.id } }, 'backup'), 'quiet')));
    });
    return [head, moduleFindings('projects'), h('h2', { text: 'Dự án' }),
      projects.length ? h('div', { class: 'grid' }, cards) : h('div', { class: 'empty', text: 'Chưa có dự án nào. Bấm "Thêm dự án".' })];
  }

  function openAdd() {
    const input = h('input', { type: 'text', placeholder: '/Users/ban/projects/ten-du-an', 'aria-label': 'Đường dẫn repo' });
    const go2 = btn('Xem trước', () => {
      const path = input.value.trim();
      if (!path.startsWith('/')) { toast('Nhập đường dẫn đầy đủ, bắt đầu bằng /'); return; }
      openFix('Thêm ' + path, { preview: 'add.preview', apply: 'add', args: { path } }, 'safe');
    }, 'primary');
    openSheet('Thêm dự án', [h('p', { text: 'Đường dẫn tới repo Git trên máy này. acp thêm nó vào danh sách và gắn skill.' }), input], [go2]);
    input.focus();
  }

  async function openSkills(id) {
    const body = h('div', { class: 'stack' }, h('p', { class: 'muted', text: 'Đang tải…' }));
    const next = btn('Xem trước', null, 'primary');
    next.disabled = true;
    openSheet('Skill của ' + id, body, [next]);
    const r = await api('/api/skills/' + encodeURIComponent(id));
    if (r.status !== 200) { body.replaceChildren(h('div', { class: 'notice error', text: (r.data && r.data.error) || 'Không tải được' })); return; }
    const d = dataOf(r.data);
    const ak = d.agentkit || {};
    const personal = d.personal || {};
    const akSelected = new Set(ak.mode === 'all' ? toArray(ak.catalog) : toArray(ak.selected));
    const personalSelected = new Set(Object.keys(personal.selected || {}));
    const want = new Map();
    function group(title, catalog, selected) {
      return h('div', { class: 'part' }, h('h3', { text: title + ' (' + selected.size + '/' + catalog.length + ')' }),
        catalog.length ? h('div', { class: 'checks' }, catalog.map((name) => {
          const box = h('input', { type: 'checkbox' });
          box.checked = selected.has(name);
          const label = h('label', {}, box, h('span', { text: name }));
          box.addEventListener('change', () => {
            if (box.checked === selected.has(name)) want.delete(name); else want.set(name, box.checked);
            label.classList.toggle('changed', want.has(name));
            next.disabled = want.size === 0;
          });
          return label;
        })) : h('p', { class: 'muted', text: 'Không có.' }));
    }
    body.replaceChildren(...[
      h('p', { class: 'muted', text: 'Đánh dấu để thêm, bỏ đánh dấu để gỡ. Chưa có gì thay đổi cho tới khi bạn áp dụng.' }),
      ak.kit ? group('Skill AgentKit · ' + ak.kit, toArray(ak.catalog), akSelected) : null,
      group('Skill cá nhân', toArray(personal.catalog), personalSelected)].filter(Boolean));
    next.addEventListener('click', () => {
      const add = [...want].filter(([, v]) => v).map(([k]) => k);
      const remove = [...want].filter(([, v]) => !v).map(([k]) => k);
      openFix('Đổi skill của ' + id, { preview: 'skills.preview', apply: 'skills.apply', args: { id, add, remove } }, 'confirm');
    });
  }

  const CHANNEL_TERM = { beta: 'kênh Beta', stable: 'kênh Stable' };

  function agentkitSection() {
    const d = dataOf(state.setup);
    const ak = d.ak || {};
    const login = d.login || {};
    const pins = d.pins || {};
    const channel = d.channel || 'beta';
    const akCard = h('article', { class: 'card' },
      h('div', { class: 'row' }, h('h3', {}, 'Công cụ ', term('AgentKit', 'ak')), ak.version ? tag('ok', 'Đã cài') : tag('error', 'Chưa cài')),
      h('dl', { class: 'facts' },
        h('dt', { text: 'Phiên bản' }), h('dd', { text: ak.version || '–' }),
        h('dt', { text: 'Đăng nhập' }), h('dd', { text: login.authenticated ? 'Đã đăng nhập' + (login.method ? ' (' + login.method + ')' : '') : 'Chưa đăng nhập' }),
        h('dt', { text: 'Tình trạng' }), h('dd', { text: d.healthy ? 'Ổn' : 'Cần xem lại' }),
        tech(h('dt', { text: 'Đường dẫn' }), h('dd', { class: 'cmd', text: ak.path || '–' }))),
      login.authenticated ? null : h('p', { class: 'muted', text: 'Đăng nhập cần terminal: chạy acp setup login.' }),
      h('div', { class: 'row' }, btn(ak.version ? 'Cài lại ak' : 'Cài ak', () => openFix('Cài công cụ ak', { preview: 'setup.install-ak.preview', apply: 'setup.install-ak', args: {} }, 'confirm'))));
    const pinRows = Object.entries(pins).flatMap(([ch, kits]) => Object.entries(kits || {}).map(([kit, v]) => h('div', {}, h('span', {}, h('b', { text: kit }), ' · ', ch), h('span', { class: 'muted', text: v }))));
    const channelCard = h('article', { class: 'card' },
      h('div', { class: 'row' }, h('h3', {}, 'Kênh phát hành: ', term(CHANNEL_TERM[channel] || channel))),
      h('p', { class: 'muted', text: state.glossary[CHANNEL_TERM[channel]] || '' }),
      pinRows.length ? h('div', { class: 'list' }, pinRows) : h('p', { class: 'muted', text: 'Chưa ghim phiên bản nào.' }),
      h('div', { class: 'row' }, btn('Tìm bản mới', () => openFix('Cập nhật AgentKit', { preview: 'upgrade.preview', apply: 'upgrade', args: d.channel ? { channel: d.channel } : {} }, 'confirm'))));
    const homes = toArray(state.hooks && state.hooks.homes);
    const homeCard = h('article', { class: 'card' },
      h('h3', { text: 'Trợ lý AI trên máy' }),
      h('div', { class: 'list' }, homes.map((home) => h('div', {},
        h('div', { class: 'row' }, h('b', { text: home.label }), home.exists ? tag('ok', home.skills + ' skill') : tag('info', 'Chưa cài')),
        tech(h('span', { class: 'cmd muted', text: home.folder }))))));
    return [h('p', { class: 'lead' }, term('AgentKit'), ' cài bộ skill và agent dựng sẵn vào từng dự án qua công cụ ak. Ở đây bạn xem phiên bản, kênh và các trợ lý AI trên máy.'),
      moduleFindings('agentkit'), h('div', { class: 'grid' }, akCard, channelCard, homeCard)];
  }

  function hookState(s) {
    if (s === 'installed') return tag('ok', 'Đã cài');
    if (s === 'not installed') return tag('warn', 'Chưa cài');
    if (s.startsWith('old Python hook')) return tag('warn', 'Bản Python cũ');
    if (s.startsWith('installed with another path')) return tag('warn', 'Trỏ tới acp khác');
    if (s.startsWith('not found')) return tag('info', 'Chưa có tệp cài đặt');
    if (s.startsWith('unreadable')) return tag('error', 'Không đọc được');
    if (s.includes('unexpected shape')) return tag('error', 'Cấu trúc lạ, để nguyên');
    return tag('info', s);
  }

  function hooksSection() {
    const inv = state.hooks || {};
    const acpTargets = toArray(dataOf(inv.acp).targets);
    const all = state.scan ? findings('hooks') : [];
    const marks = (file, event, command) => all.filter((f) => f.params && f.params.file === file &&
      (f.code === 'hook_duplicate' ? f.params.event === event && f.params.command === command
        : f.code === 'hook_target_missing' ? f.params.event === event && f.raw === command : false));
    const acpCard = h('article', { class: 'card' },
      h('h3', {}, term('hook'), ' tự gắn worktree của acp'),
      h('p', { class: 'muted', text: 'Khi bạn mở một phiên mới trong worktree, hook này gắn skill cho nó.' }),
      h('div', { class: 'list' }, acpTargets.map((t) => h('div', {}, h('div', { class: 'row' }, h('b', { text: t.name }), hookState(t.state || '')), tech(h('span', { class: 'cmd muted', text: t.path }))))),
      h('div', { class: 'row' },
        btn('Cài hook', () => openFix('Cài hook của acp', { preview: 'hooks.install.preview', apply: 'hooks.install', args: {} }, 'backup'), 'primary'),
        btn('Gỡ hook', () => openFix('Gỡ hook của acp', { preview: 'hooks.uninstall.preview', apply: 'hooks.uninstall', args: {} }, 'backup'), 'quiet')));
    const settings = toArray(inv.settings).filter((s) => s.exists);
    const settingsCards = settings.map((s) => h('article', { class: 'card' },
      h('div', { class: 'row' }, h('h3', { text: s.name }), tag('info', toArray(s.hooks).length + ' hook')),
      tech(h('p', { class: 'cmd muted', text: s.path })),
      toArray(s.hooks).length ? h('div', { class: 'list' }, s.hooks.map((hk) => {
        const m = marks(s.path, hk.event, hk.command);
        return h('div', {},
          h('div', { class: 'row' }, h('b', { text: hk.event }), hk.matcher ? h('span', { class: 'muted', text: hk.matcher }) : null,
            hk.kind === 'acp' ? tag('accent', 'acp') : hk.kind === 'legacy' ? tag('warn', 'acp bản Python cũ') : null,
            m.some((f) => f.code === 'hook_duplicate') ? tag('info', 'Chạy hai lần') : null,
            m.some((f) => f.code === 'hook_target_missing') ? tag('warn', 'Thiếu tệp') : null),
          h('span', { class: 'cmd', text: hk.command }));
      })) : h('p', { class: 'muted', text: 'Không có hook.' })));
    const missing = new Set(all.filter((f) => f.code === 'mcp_command_missing').map((f) => f.params.source + '\u0000' + f.params.server));
    const servers = toArray(inv.mcp);
    const mcpCard = h('article', { class: 'card' },
      h('h3', {}, 'Máy chủ ', term('MCP')),
      servers.length ? h('div', { class: 'list' }, servers.map((s) => h('div', {},
        h('div', { class: 'row' }, h('b', { text: s.name }), s.remote ? tag('info', 'Từ xa') : null,
          missing.has(s.source + '\u0000' + s.name) ? tag('warn', 'Không chạy được') : null),
        h('span', { class: 'muted', text: s.source }),
        s.remote ? null : h('span', { class: 'cmd', text: s.command })))) : h('p', { class: 'muted', text: 'Chưa cấu hình máy chủ MCP nào.' }),
      h('p', { class: 'muted', text: 'Chỉ hiện tên và lệnh; tham số, biến môi trường và địa chỉ có thể chứa khoá bí mật nên không hiện.' }));
    const g = previewLines(state.global);
    const globalCard = h('article', { class: 'card' },
      h('div', { class: 'row' }, h('h3', { text: 'Skill dùng chung cho tài khoản' }), g.changes.length || g.issues.length ? tag('warn', 'Chưa khớp') : tag('ok', 'Khớp')),
      h('p', { class: 'muted', text: 'Skill trong global-skills.json được gắn vào thư mục home của từng trợ lý.' }),
      g.changes.length ? lineList(g.changes.slice(0, 8), '', '+') : null,
      g.issues.length ? lineList(g.issues.slice(0, 8), 'issues', '!') : null,
      h('div', { class: 'row' }, btn('Áp dụng', () => openFix('Gắn skill dùng chung', { preview: 'global.preview', apply: 'global.apply', args: {} }, 'safe'))));
    return [h('p', { class: 'lead' }, term('hook'), ', máy chủ ', term('MCP'), ' và cài đặt dùng chung của các trợ lý AI trên máy.'),
      moduleFindings('hooks'), h('div', { class: 'grid' }, acpCard, globalCard, mcpCard), h('h2', { text: 'Tệp cài đặt' }),
      settingsCards.length ? h('div', { class: 'grid' }, settingsCards) : h('div', { class: 'empty', text: 'Không tìm thấy tệp cài đặt nào.' })];
  }

  // ------------------------------------------------------------ frame

  function go(id) {
    state.section = id;
    store.set('acp.section', id);
    render();
    window.scrollTo(0, 0);
  }

  function setTheme(t) {
    if (t === 'auto') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.setAttribute('data-theme', t);
    store.local('acp.theme', t);
  }

  function render() {
    const app = document.getElementById('app');
    const techBox = h('input', { type: 'checkbox' });
    techBox.checked = state.tech;
    techBox.addEventListener('change', () => { state.tech = techBox.checked; store.local('acp.tech', state.tech ? '1' : '0'); render(); });
    const theme = store.local('acp.theme') || 'auto';
    const themeBtn = btn({ auto: 'Giao diện: tự động', light: 'Giao diện: sáng', dark: 'Giao diện: tối' }[theme], () => {
      setTheme({ auto: 'light', light: 'dark', dark: 'auto' }[theme]);
      render();
    }, 'quiet');
    const tabs = h('nav', { class: 'tabs', 'aria-label': 'Mục' }, SECTIONS.map((s) => {
      const n = s.module && state.scan ? findings(s.module).filter((f) => f.severity !== 'info').length : 0;
      return h('button', { type: 'button', 'aria-current': state.section === s.id ? 'page' : null, onclick: () => go(s.id) },
        s.label, n ? h('span', { class: 'count', text: String(n) }) : null);
    }));
    const view = { overview, projects: projectsSection, agentkit: agentkitSection, hooks: hooksSection }[state.section] || overview;
    app.replaceChildren(h('div', { class: 'shell' },
      h('header', { class: 'top' },
        h('div', { class: 'brand' }, h('b', { text: 'acp' }), h('span', { class: 'muted', text: 'skill cho trợ lý AI trên máy này' })),
        h('div', { class: 'controls' }, h('label', { class: 'switch' }, techBox, 'Hiện thông tin kỹ thuật'), themeBtn)),
      tabs,
      state.loadError ? h('div', { class: 'notice error', text: state.loadError }) : null,
      h('main', { class: 'stack' }, view())));
  }

  function renderSignIn() {
    closeSheet();
    document.getElementById('app').replaceChildren(h('div', { class: 'shell' }, h('div', { class: 'card signin' },
      h('h1', { text: 'Mở lại acp web' }),
      h('p', { text: 'Phiên này đã hết hoặc liên kết đăng nhập đã được dùng. Chạy lại lệnh dưới đây trong terminal rồi mở liên kết nó in ra.' }),
      h('pre', { text: 'acp web' }))));
  }

  async function boot() {
    const theme = store.local('acp.theme');
    if (theme && theme !== 'auto') setTheme(theme);
    const m = /code=([0-9a-f]+)/.exec(location.hash);
    if (m) {
      history.replaceState(null, '', location.pathname);
      const r = await api('/api/session', { method: 'POST', body: { code: m[1] } });
      if (r.status === 200) { token = r.data.token; store.set('acp.token', token); }
    }
    if (!token) { renderSignIn(); return; }
    document.getElementById('app').replaceChildren(h('div', { class: 'shell' }, h('p', { class: 'muted', text: 'Đang tải…' })));
    await Promise.all([load(), loadScan(), loadGlossary()]);
    if (token) render();
  }

  // A new `acp web` run opened in the same tab only changes the hash.
  window.addEventListener('hashchange', () => { if (/code=/.test(location.hash)) boot(); });
  boot();
})();
