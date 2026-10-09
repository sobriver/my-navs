'use strict';
(() => {
  const q = document.getElementById('q');
  const tabs = [...document.querySelectorAll('.tab')];
  const cards = [...document.querySelectorAll('.card')];
  const grid = document.getElementById('grid');
  const empty = document.getElementById('empty');
  const KEY = 'navs:group';

  let current = null;
  try { current = localStorage.getItem(KEY); } catch {}
  if (!tabs.some(t => t.dataset.group === current)) current = tabs[0] ? tabs[0].dataset.group : null;

  function render() {
    const words = q.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const searching = words.length > 0;
    document.body.classList.toggle('searching', searching);

    let first = null;
    for (const c of cards) {
      const show = searching
        ? words.every(w => c.dataset.search.includes(w))
        : c.dataset.group === current;
      c.hidden = !show;
      c.classList.remove('first');
      if (show && !first) first = c;
    }
    // 搜索时高亮第一个结果，按回车打开的就是它
    if (searching && first) first.classList.add('first');

    for (const t of tabs) {
      const on = !searching && t.dataset.group === current;
      t.classList.toggle('active', on);
      t.setAttribute('aria-selected', on);
    }
    if (empty) {
      grid.hidden = !first; // 没有结果时连网格的边线也一起隐藏
      empty.hidden = !!first;
      empty.textContent = searching ? '没有匹配的链接' : '这个分组还没有链接';
    }
  }

  for (const t of tabs) {
    t.addEventListener('click', () => {
      current = t.dataset.group;
      try { localStorage.setItem(KEY, current); } catch {}
      q.value = '';
      render();
    });
  }

  q.addEventListener('input', render);
  q.addEventListener('keydown', e => {
    if (e.key === 'Enter') {
      const c = cards.find(c => !c.hidden);
      if (c) { e.preventDefault(); c.click(); }
    } else if (e.key === 'Escape') {
      if (q.value) { q.value = ''; render(); } else q.blur();
    }
  });

  document.addEventListener('keydown', e => {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
    if (document.activeElement === q) return;
    e.preventDefault();
    q.focus();
  });

  render();
})();
