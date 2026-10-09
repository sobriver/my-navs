'use strict';
(() => {
  const $ = s => document.querySelector(s);
  const CDN = 'https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons';
  const MAX_ICON = 512 * 1024;
  const SVG = {
    grip: '<circle cx="9" cy="5" r="1"/><circle cx="9" cy="12" r="1"/><circle cx="9" cy="19" r="1"/><circle cx="15" cy="5" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="15" cy="19" r="1"/>',
    edit: '<path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/>',
    trash: '<path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/>',
    up: '<path d="m18 15-6-6-6 6"/>',
    down: '<path d="m6 9 6 6 6-6"/>',
  };

  let data = { version: 0, groups: [], links: [] };
  let current = null; // 当前选中的分组
  let drag = null;    // 正在拖拽的条目 { kind, id, el, sorted, dropped }

  // ---------- 工具 ----------

  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const c of children.flat()) if (c != null && c !== false) el.append(c);
    return el;
  }

  function icon(name) {
    const t = document.createElement('template');
    t.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${SVG[name]}</svg>`;
    return t.content.firstChild;
  }

  function uid() {
    const a = new Uint8Array(6);
    crypto.getRandomValues(a);
    return Array.from(a, b => b.toString(16).padStart(2, '0')).join('');
  }

  function initial(name) {
    const c = [...name.trim()][0];
    return c ? c.toUpperCase() : '?';
  }
  function iconOf(l) {
    return l.icon
      ? h('img', { src: '/icons/' + l.icon, alt: '' })
      : h('span', { class: 'letter' }, initial(l.name));
  }

  let toastTimer;
  function toast(msg, isError) {
    const el = $('#toast');
    el.textContent = msg;
    el.className = 'toast' + (isError ? ' error' : '');
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, isError ? 4000 : 1800);
  }

  async function api(method, url, body, raw) {
    const opt = { method, headers: {} };
    if (body !== undefined) {
      if (raw) opt.body = body;
      else { opt.body = JSON.stringify(body); opt.headers['Content-Type'] = 'application/json'; }
    }
    const res = await fetch(url, opt);
    if (res.status === 401) {
      location.href = '/login?next=/admin';
      throw new Error('登录已过期');
    }
    let json = null;
    try { json = await res.json(); } catch {}
    if (!res.ok) {
      const err = new Error((json && json.error) || `请求失败（${res.status}）`);
      err.status = res.status;
      throw err;
    }
    return json;
  }

  // ---------- 数据 ----------

  async function load() {
    data = await api('GET', '/api/data');
    render();
  }

  // commit 在副本上修改并整体提交；mutate 返回 false 表示没有变化，不提交
  async function commit(mutate, okMsg = '已保存') {
    const draft = structuredClone(data);
    if (mutate(draft) === false) return false;
    try {
      data = await api('PUT', '/api/data', draft);
      toast(okMsg);
      render();
      return true;
    } catch (e) {
      toast(e.message, true);
      if (e.status === 409) await load(); else render();
      return false;
    }
  }

  const linksOf = gid => data.links.filter(l => l.groupId === gid);

  function reorderLinks(d, gid, ids) {
    const byId = new Map(d.links.map(l => [l.id, l]));
    d.links = [...d.links.filter(l => l.groupId !== gid), ...ids.map(id => byId.get(id))];
  }

  function reorderGroups(d, ids) {
    const byId = new Map(d.groups.map(g => [g.id, g]));
    d.groups = ids.map(id => byId.get(id));
  }

  function move(kind, id, dir) {
    const ids = kind === 'group'
      ? data.groups.map(g => g.id)
      : linksOf(current).map(l => l.id);
    const i = ids.indexOf(id), j = i + dir;
    if (i < 0 || j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    commit(d => kind === 'group' ? reorderGroups(d, ids) : reorderLinks(d, current, ids));
  }

  // ---------- 渲染 ----------

  function render() {
    if (!data.groups.some(g => g.id === current)) current = data.groups.length ? data.groups[0].id : null;
    renderGroups();
    renderLinks();
  }

  function moveBtns(kind, id) {
    return h('span', { class: 'move-btns' },
      h('button', { class: 'icon-btn', type: 'button', title: '上移', onclick: e => { e.stopPropagation(); move(kind, id, -1); } }, icon('up')),
      h('button', { class: 'icon-btn', type: 'button', title: '下移', onclick: e => { e.stopPropagation(); move(kind, id, 1); } }, icon('down')),
    );
  }

  function renderGroups() {
    const list = $('#group-list');
    list.replaceChildren(...data.groups.map(g => h('li', {
      class: 'row' + (g.id === current ? ' active' : ''),
      draggable: 'true',
      dataset: { id: g.id, kind: 'group' },
      onclick: () => { current = g.id; render(); },
    },
      h('span', { class: 'handle' }, icon('grip')),
      h('span', { class: 'row-main row-title' }, g.name),
      h('span', { class: 'count' }, String(linksOf(g.id).length)),
      h('span', { class: 'row-actions' },
        moveBtns('group', g.id),
        h('button', { class: 'icon-btn', type: 'button', title: '重命名', onclick: e => { e.stopPropagation(); openGroup(g); } }, icon('edit')),
        h('button', { class: 'icon-btn danger', type: 'button', title: '删除', onclick: e => { e.stopPropagation(); deleteGroup(g); } }, icon('trash')),
      ),
    )));
    if (!data.groups.length) list.append(h('li', { class: 'placeholder' }, '还没有分组'));
  }

  function renderLinks() {
    const g = data.groups.find(x => x.id === current);
    const list = $('#link-list');
    $('#links-title').textContent = g ? g.name : '链接';
    $('#btn-add-link').disabled = !g;
    if (!g) {
      list.replaceChildren(h('li', { class: 'placeholder' }, '先在左边新建一个分组'));
      return;
    }
    const links = linksOf(g.id);
    list.replaceChildren(...links.map(l => h('li', {
      class: 'row',
      draggable: 'true',
      dataset: { id: l.id, kind: 'link' },
      onclick: () => openLink(l),
    },
      h('span', { class: 'handle' }, icon('grip')),
      h('span', { class: 'thumb' }, iconOf(l)),
      h('span', { class: 'row-main' },
        h('div', { class: 'row-title' }, l.name),
        h('div', { class: 'row-sub' }, l.desc ? `${l.desc} · ${l.url}` : l.url),
      ),
      h('span', { class: 'row-actions' },
        moveBtns('link', l.id),
        h('button', { class: 'icon-btn', type: 'button', title: '编辑', onclick: e => { e.stopPropagation(); openLink(l); } }, icon('edit')),
        h('button', { class: 'icon-btn danger', type: 'button', title: '删除', onclick: e => { e.stopPropagation(); deleteLink(l); } }, icon('trash')),
      ),
    )));
    if (!links.length) list.append(h('li', { class: 'placeholder' }, '这个分组还没有链接，点右上角添加'));
  }

  // ---------- 拖拽排序 ----------

  function clearDropTargets() {
    document.querySelectorAll('.drop-target').forEach(el => el.classList.remove('drop-target'));
  }

  function setupSortable(list, kind) {
    list.addEventListener('dragstart', e => {
      const row = e.target.closest && e.target.closest('.row');
      if (!row || row.dataset.kind !== kind) return;
      drag = { kind, id: row.dataset.id, el: row, sorted: false, dropped: false };
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', row.dataset.id);
      requestAnimationFrame(() => row.classList.add('dragging'));
    });

    list.addEventListener('dragover', e => {
      if (!drag || drag.kind !== kind) return;
      e.preventDefault();
      const over = e.target.closest('.row');
      if (!over || over === drag.el || over.dataset.kind !== kind) return;
      const r = over.getBoundingClientRect();
      const after = e.clientY > r.top + r.height / 2;
      list.insertBefore(drag.el, after ? over.nextSibling : over);
    });

    list.addEventListener('drop', e => {
      if (!drag || drag.kind !== kind) return;
      e.preventDefault();
      drag.sorted = true;
    });

    list.addEventListener('dragend', () => {
      if (!drag || drag.kind !== kind) return;
      const d0 = drag;
      drag = null;
      d0.el.classList.remove('dragging');
      clearDropTargets();
      if (d0.dropped) return; // 已经拖到别的分组，由 drop 处理
      if (!d0.sorted) { render(); return; } // 拖到列表外或按了 Esc，恢复原样
      const ids = [...list.querySelectorAll('.row')].map(r => r.dataset.id);
      const before = kind === 'group' ? data.groups.map(g => g.id) : linksOf(current).map(l => l.id);
      if (ids.join() === before.join()) return;
      commit(d => kind === 'group' ? reorderGroups(d, ids) : reorderLinks(d, current, ids));
    });
  }

  // 把链接拖到分组上 = 移到该分组
  function setupGroupDrop(list) {
    list.addEventListener('dragover', e => {
      if (!drag || drag.kind !== 'link') return;
      const row = e.target.closest('.row');
      clearDropTargets();
      if (!row || row.dataset.id === current) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      row.classList.add('drop-target');
    });
    list.addEventListener('dragleave', e => {
      if (!list.contains(e.relatedTarget)) clearDropTargets();
    });
    list.addEventListener('drop', e => {
      if (!drag || drag.kind !== 'link') return;
      const row = e.target.closest('.row');
      if (!row || row.dataset.id === current) return;
      e.preventDefault();
      drag.dropped = true;
      const id = drag.id, gid = row.dataset.id;
      const g = data.groups.find(x => x.id === gid);
      commit(d => {
        const i = d.links.findIndex(l => l.id === id);
        const [l] = d.links.splice(i, 1);
        l.groupId = gid;
        d.links.push(l);
      }, `已移到「${g.name}」`);
    });
  }

  // ---------- 分组弹窗 ----------

  const dlgGroup = $('#dlg-group');
  let editingGroup = null;

  function openGroup(g) {
    editingGroup = g || null;
    $('#group-title').textContent = g ? '重命名分组' : '新建分组';
    $('#g-name').value = g ? g.name : '';
    dlgGroup.showModal();
  }

  $('#form-group').addEventListener('submit', async e => {
    e.preventDefault();
    const name = $('#g-name').value.trim();
    if (!name) return;
    const g = editingGroup;
    const newId = g ? null : uid();
    const ok = await commit(d => {
      if (g) d.groups.find(x => x.id === g.id).name = name;
      else d.groups.push({ id: newId, name });
    });
    if (!ok) return;
    if (newId) { current = newId; render(); }
    dlgGroup.close();
  });

  function deleteGroup(g) {
    const n = linksOf(g.id).length;
    const msg = n ? `删除分组「${g.name}」以及其中的 ${n} 个链接？` : `删除分组「${g.name}」？`;
    if (!confirm(msg)) return;
    commit(d => {
      d.groups = d.groups.filter(x => x.id !== g.id);
      d.links = d.links.filter(l => l.groupId !== g.id);
    }, '已删除');
  }

  // ---------- 链接弹窗 ----------

  const dlgLink = $('#dlg-link');
  let editingLink = null;
  let linkIcon = '';

  function openLink(l) {
    editingLink = l || null;
    $('#link-title').textContent = l ? '编辑链接' : '添加链接';
    $('#l-name').value = l ? l.name : '';
    $('#l-url').value = l ? l.url : '';
    $('#l-desc').value = l ? l.desc || '' : '';
    $('#l-sametab').checked = !!(l && l.sameTab);
    const sel = $('#l-group');
    sel.replaceChildren(...data.groups.map(g => h('option', { value: g.id }, g.name)));
    sel.value = l ? l.groupId : current;
    $('#l-icon-input').value = '';
    setIcon(l ? l.icon || '' : '');
    $('#btn-link-delete').hidden = !l;
    dlgLink.showModal();
  }

  function setIcon(name) {
    linkIcon = name;
    const label = $('#l-name').value || '?';
    $('#l-icon-preview').replaceChildren(name
      ? h('img', { src: '/icons/' + name, alt: '' })
      : h('span', { class: 'letter' }, initial(label)));
    $('#btn-icon-clear').hidden = !name;
  }

  $('#l-name').addEventListener('input', () => { if (!linkIcon) setIcon(''); });

  $('#form-link').addEventListener('submit', async e => {
    e.preventDefault();
    const v = {
      name: $('#l-name').value.trim(),
      url: $('#l-url').value.trim(),
      groupId: $('#l-group').value,
      desc: $('#l-desc').value.trim(),
      icon: linkIcon,
      sameTab: $('#l-sametab').checked,
    };
    const l = editingLink;
    const ok = await commit(d => {
      if (!l) { d.links.push({ id: uid(), ...v }); return; }
      const i = d.links.findIndex(x => x.id === l.id);
      const moved = d.links[i].groupId !== v.groupId;
      Object.assign(d.links[i], v);
      if (moved) d.links.push(...d.links.splice(i, 1)); // 换了分组就排到新分组末尾
    });
    if (ok) dlgLink.close();
  });

  function deleteLink(l) {
    if (!confirm(`删除链接「${l.name}」？`)) return;
    commit(d => { d.links = d.links.filter(x => x.id !== l.id); }, '已删除').then(ok => {
      if (ok && dlgLink.open) dlgLink.close();
    });
  }

  $('#btn-link-delete').addEventListener('click', () => { if (editingLink) deleteLink(editingLink); });

  // ---------- 图标 ----------

  async function busy(fn) {
    const btns = [$('#btn-icon-fetch'), $('#btn-icon-upload')];
    const label = btns[0].textContent;
    btns.forEach(b => { b.disabled = true; });
    btns[0].textContent = '处理中…';
    try { await fn(); } catch (e) { toast(e.message, true); }
    finally {
      btns.forEach(b => { b.disabled = false; });
      btns[0].textContent = label;
    }
  }

  async function uploadBlob(blob) {
    if (blob.size > MAX_ICON) throw new Error('图片不能超过 512KB');
    const r = await api('POST', '/api/icons/upload', blob, true);
    setIcon(r.icon);
  }

  async function fetchIcon() {
    let input = $('#l-icon-input').value.trim();
    if (!input) input = $('#l-name').value.trim();
    if (!input) throw new Error('请输入图标名称或图片地址');

    let urls;
    if (/^https?:\/\//i.test(input)) {
      urls = [input];
    } else {
      const n = input.toLowerCase().replace(/\.(svg|png|webp)$/, '').replace(/\s+/g, '-');
      urls = [`${CDN}/svg/${n}.svg`, `${CDN}/png/${n}.png`];
    }

    // 先让服务器下载；只有服务器连不上外网（502）时，才改由浏览器下载后上传
    let lastErr;
    const unreachable = [];
    for (const u of urls) {
      try {
        const r = await api('POST', '/api/icons/fetch', { url: u });
        setIcon(r.icon);
        return;
      } catch (e) {
        lastErr = e;
        if (e.status === 502) unreachable.push(u);
      }
    }
    for (const u of unreachable) {
      try {
        const res = await fetch(u, { mode: 'cors', credentials: 'omit', signal: AbortSignal.timeout(8000) });
        if (!res.ok) continue;
        await uploadBlob(await res.blob());
        return;
      } catch {}
    }
    if (urls.length > 1 && !unreachable.length) throw new Error(`图标库里没有「${input}」，换个名字试试`);
    throw lastErr || new Error('获取失败');
  }

  $('#btn-icon-fetch').addEventListener('click', () => busy(fetchIcon));
  $('#l-icon-input').addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); busy(fetchIcon); }
  });
  $('#btn-icon-upload').addEventListener('click', () => $('#l-icon-file').click());
  $('#l-icon-file').addEventListener('change', e => {
    const f = e.target.files[0];
    e.target.value = '';
    if (f) busy(() => uploadBlob(f));
  });
  $('#btn-icon-clear').addEventListener('click', () => setIcon(''));

  dlgLink.addEventListener('paste', e => {
    const item = [...((e.clipboardData && e.clipboardData.items) || [])]
      .find(i => i.kind === 'file' && i.type.startsWith('image/'));
    if (!item) return;
    e.preventDefault();
    busy(() => uploadBlob(item.getAsFile()));
  });

  // ---------- 弹窗通用 ----------

  document.addEventListener('click', e => {
    const b = e.target.closest('[data-close]');
    if (b) b.closest('dialog').close();
  });
  // 点击遮罩关闭；要求按下和松开都在遮罩上，避免拖选文字时误关
  for (const dlg of [dlgGroup, dlgLink]) {
    let downOnBackdrop = false;
    dlg.addEventListener('mousedown', e => { downOnBackdrop = e.target === dlg; });
    dlg.addEventListener('click', e => { if (downOnBackdrop && e.target === dlg) dlg.close(); });
  }

  // ---------- 导入导出 ----------

  $('#btn-export').addEventListener('click', () => { location.href = '/api/export'; });
  $('#file-import').addEventListener('change', async e => {
    const f = e.target.files[0];
    e.target.value = '';
    if (!f) return;
    if (!confirm('导入会覆盖当前全部的分组和链接，确定继续？')) return;
    try {
      data = await api('POST', '/api/import', f, true);
      render();
      toast('导入成功');
    } catch (err) {
      toast(err.message, true);
    }
  });

  // ---------- 启动 ----------

  $('#btn-add-group').addEventListener('click', () => openGroup(null));
  $('#btn-add-link').addEventListener('click', () => openLink(null));
  setupSortable($('#group-list'), 'group');
  setupSortable($('#link-list'), 'link');
  setupGroupDrop($('#group-list'));
  load().catch(e => toast(e.message, true));
})();
