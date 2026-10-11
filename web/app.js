// Meme Forge frontend — talks to the Go API under /api. No dependencies.
(() => {
  const $ = (id) => document.getElementById(id);
  const el = (tag, props = {}, ...children) => {
    const node = Object.assign(document.createElement(tag), props);
    node.append(...children);
    return node;
  };

  // ---------- API ----------
  async function api(method, path, body) {
    const opts = { method, headers: {} };
    if (body instanceof FormData) opts.body = body;
    else if (body !== undefined) {
      opts.body = JSON.stringify(body);
      opts.headers['Content-Type'] = 'application/json';
    }
    const res = await fetch(path, opts);
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      const err = new Error(data.error || `Request failed (${res.status})`);
      err.status = res.status;
      throw err;
    }
    return data;
  }

  // ---------- Auth ----------
  let user = null;
  let afterLogin = null; // action to resume once the user logs in

  function setUser(u) {
    user = u;
    document.querySelectorAll('.auth-only').forEach((n) => { n.hidden = !user; });
    document.querySelectorAll('.anon-only').forEach((n) => { n.hidden = !!user; });
    $('whoami').textContent = user ? user.username : '';
  }

  // Runs fn now if logged in, otherwise after a successful login.
  function requireLogin(fn) {
    if (user) return fn();
    afterLogin = fn;
    openAuth('login');
  }

  let authMode = 'login';
  function openAuth(mode) {
    authMode = mode;
    [...$('auth-mode').children].forEach((b) => b.classList.toggle('active', b.dataset.mode === mode));
    $('auth-submit').textContent = mode === 'login' ? 'Log in' : 'Create account';
    $('auth-form').password.autocomplete = mode === 'login' ? 'current-password' : 'new-password';
    $('auth-error').textContent = '';
    $('auth-dialog').showModal();
  }
  $('auth-mode').addEventListener('click', (e) => { if (e.target.dataset.mode) openAuth(e.target.dataset.mode); });
  $('login').addEventListener('click', () => openAuth('login'));
  $('auth-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      const { user: u } = await api('POST', `/api/auth/${authMode}`, { username: f.username.value, password: f.password.value });
      const next = afterLogin; afterLogin = null;
      setUser(u);
      f.reset();
      $('auth-dialog').close();
      if (next) next(); else route();
    } catch (err) {
      $('auth-error').textContent = err.message;
    }
  });
  $('logout').addEventListener('click', async () => {
    await api('POST', '/api/auth/logout').catch(() => {});
    setUser(null);
    if (location.hash.startsWith('#/mine')) location.hash = '#/'; else route();
  });

  document.querySelectorAll('dialog').forEach((d) => {
    d.addEventListener('click', (e) => { if (e.target.closest('[data-close]')) d.close(); });
    d.addEventListener('close', () => { if (d.id === 'auth-dialog') afterLogin = null; });
  });

  // ---------- Router ----------
  const views = ['templates', 'editor', 'feed'];
  function show(view, nav) {
    views.forEach((v) => { $(`view-${v}`).hidden = v !== view; });
    document.querySelectorAll('[data-nav]').forEach((a) => a.classList.toggle('active', a.dataset.nav === nav));
  }

  function route() {
    const [, page = '', arg] = location.hash.split('/');
    if (page === 'editor' && arg) { show('editor'); openTemplate(arg); }
    else if (page === 'feed') { show('feed', 'feed'); loadFeed('feed', true); }
    else if (page === 'mine') {
      if (!user) { location.hash = '#/'; return; }
      show('feed', 'mine'); loadFeed('mine', true);
    } else { show('templates', 'templates'); loadTemplates(true); loadTags(); }
  }
  window.addEventListener('hashchange', route);

  // ---------- Templates ----------
  const tq = { tag: '', favorites: false, offset: 0, total: 0 };

  async function loadTemplates(reset) {
    if (reset) tq.offset = 0;
    const params = new URLSearchParams({ sort: $('sort').value, limit: 60, offset: tq.offset });
    const q = $('search').value.trim();
    if (q) params.set('q', q);
    if (tq.tag) params.set('tag', tq.tag);
    if (tq.favorites) params.set('favorites', '1');
    try {
      const { templates, total } = await api('GET', `/api/templates?${params}`);
      tq.total = total;
      tq.offset += templates.length;
      const grid = $('template-grid');
      if (reset) grid.replaceChildren();
      grid.append(...templates.map(templateCard));
      $('templates-status').textContent = total
        ? `${total} template${total === 1 ? '' : 's'}`
        : tq.favorites ? 'No favourites yet — tap ☆ on a template to save it.' : 'No templates match.';
      $('templates-more').hidden = tq.offset >= total;
    } catch (err) {
      $('templates-status').textContent = `Could not load templates: ${err.message}`;
    }
  }

  function templateCard(t) {
    const fav = el('button', { className: 'icon-btn fav', title: 'Favourite', textContent: t.favorite ? '★' : '☆' });
    fav.setAttribute('aria-pressed', t.favorite);
    fav.addEventListener('click', (e) => {
      e.preventDefault();
      requireLogin(async () => {
        const on = !t.favorite;
        try {
          await api(on ? 'PUT' : 'DELETE', `/api/templates/${t.id}/favorite`);
          t.favorite = on;
          fav.textContent = on ? '★' : '☆';
          fav.setAttribute('aria-pressed', on);
        } catch (err) { alert(err.message); }
      });
    });
    const card = el('a', { className: 'card', href: `#/editor/${t.id}` },
      el('img', { src: t.image_url, alt: t.name, loading: 'lazy' }),
      el('span', { className: 'card-title', textContent: t.name }),
      fav);
    if (t.can_delete) {
      const del = el('button', { className: 'icon-btn del', title: 'Delete template', textContent: '×' });
      del.addEventListener('click', async (e) => {
        e.preventDefault();
        if (!confirm(`Delete template "${t.name}"?`)) return;
        try { await api('DELETE', `/api/templates/${t.id}`); card.remove(); } catch (err) { alert(err.message); }
      });
      card.append(del);
    }
    return card;
  }

  async function loadTags() {
    try {
      const { tags } = await api('GET', '/api/tags');
      const chip = (label, active, onClick) => {
        const b = el('button', { className: 'chip' + (active ? ' active' : ''), textContent: label });
        b.addEventListener('click', onClick);
        return b;
      };
      const chips = [chip('All', !tq.tag && !tq.favorites, () => { tq.tag = ''; tq.favorites = false; refresh(); })];
      if (user) chips.push(chip('★ Favourites', tq.favorites, () => { tq.favorites = !tq.favorites; tq.tag = ''; refresh(); }));
      for (const { tag } of tags) chips.push(chip(`#${tag}`, tq.tag === tag, () => { tq.tag = tq.tag === tag ? '' : tag; tq.favorites = false; refresh(); }));
      $('tags').replaceChildren(...chips);
    } catch { /* tags are optional */ }
  }
  function refresh() { loadTemplates(true); loadTags(); }

  let searchTimer;
  $('search').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => loadTemplates(true), 250); });
  $('sort').addEventListener('change', () => loadTemplates(true));
  $('templates-more').addEventListener('click', () => loadTemplates(false));

  $('open-upload').addEventListener('click', () => requireLogin(() => {
    $('upload-error').textContent = '';
    $('upload-dialog').showModal();
  }));
  $('upload-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const t = await api('POST', '/api/templates', new FormData(e.target));
      e.target.reset();
      $('upload-dialog').close();
      location.hash = `#/editor/${t.id}`;
    } catch (err) {
      $('upload-error').textContent = err.message;
    }
  });

  $('local-image').addEventListener('change', (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      ed.localPending = true; // keeps the router from bouncing back before the image loads
      location.hash = '#/editor/local';
      startEditor(reader.result, { id: null, name: file.name.replace(/\.[^.]+$/, ''), box_count: 2 });
    };
    reader.readAsDataURL(file);
  });

  // ---------- Editor ----------
  const canvas = $('canvas');
  const ctx = canvas.getContext('2d');
  const ed = {
    image: null,
    template: null, // { id, name, box_count } — id is null for a local image
    layers: [],     // { text, x, y (0..1 of canvas), size (fraction of width) }
    active: 0,
    style: { font: $('font').value, color: '#ffffff', stroke: '#000000', uppercase: true },
  };

  async function openTemplate(id) {
    if (id === 'local') {
      if (!ed.image && !ed.localPending) location.hash = '#/';
      return;
    }
    if (ed.template && String(ed.template.id) === id) return; // already open
    try {
      const t = await api('GET', `/api/templates/${encodeURIComponent(id)}`);
      startEditor(t.image_url, t);
    } catch (err) {
      $('editor-status').textContent = err.message;
    }
  }

  function defaultLayers(count) {
    if (count <= 2) {
      return [
        { text: 'TOP TEXT', x: 0.5, y: 0.08, size: 0.09 },
        { text: 'BOTTOM TEXT', x: 0.5, y: 0.92, size: 0.09 },
      ];
    }
    return Array.from({ length: count }, (_, i) => ({
      text: `TEXT ${i + 1}`, x: 0.5, y: (i + 0.5) / count, size: 0.06,
    }));
  }

  function startEditor(src, template) {
    const img = new Image();
    img.crossOrigin = 'anonymous';
    img.onload = () => {
      ed.image = img;
      ed.template = template;
      ed.layers = defaultLayers(template.box_count);
      ed.active = 0;
      // Cap working resolution so huge images stay snappy and uploads stay small.
      const scale = Math.min(1, 1200 / Math.max(img.naturalWidth, img.naturalHeight));
      canvas.width = Math.round(img.naturalWidth * scale);
      canvas.height = Math.round(img.naturalHeight * scale);
      $('template-name').textContent = template.name;
      $('editor-status').textContent = '';
      renderLayers();
      draw();
      window.scrollTo(0, 0);
    };
    img.onerror = () => { $('editor-status').textContent = 'Could not load that image.'; };
    img.src = src;
  }

  function renderLayers() {
    $('layers').replaceChildren(...ed.layers.map((layer, i) => {
      const ta = el('textarea', { value: layer.text, placeholder: `Text ${i + 1}` });
      ta.addEventListener('input', () => { layer.text = ta.value; draw(); });
      ta.addEventListener('focus', () => setActive(i));

      const range = el('input', { type: 'range', min: 0.02, max: 0.2, step: 0.005, value: layer.size });
      range.addEventListener('input', () => { layer.size = +range.value; draw(); });

      const del = el('button', { className: 'btn remove', title: 'Remove text box', textContent: '×' });
      del.addEventListener('click', () => {
        ed.layers.splice(i, 1);
        ed.active = Math.max(0, Math.min(ed.active, ed.layers.length - 1));
        renderLayers(); draw();
      });

      return el('div', { className: 'layer' + (i === ed.active ? ' active' : '') },
        ta, el('label', { className: 'col' }, range, 'size'), del);
    }));
  }

  function setActive(i) {
    ed.active = i;
    [...$('layers').children].forEach((row, j) => row.classList.toggle('active', j === i));
  }

  // Wrap text to fit maxWidth, honouring explicit line breaks.
  function wrapLines(text, maxWidth) {
    const lines = [];
    for (const para of text.split('\n')) {
      let line = '';
      for (const word of para.split(/\s+/).filter(Boolean)) {
        const test = line ? `${line} ${word}` : word;
        if (line && ctx.measureText(test).width > maxWidth) { lines.push(line); line = word; }
        else line = test;
      }
      lines.push(line);
    }
    return lines;
  }

  // Lay out a layer; returns lines plus its bounding box (for hit testing).
  function layout(layer) {
    const { width: W, height: H } = canvas;
    const fontSize = layer.size * W;
    ctx.font = `bold ${fontSize}px ${ed.style.font}`;
    const text = ed.style.uppercase ? layer.text.toUpperCase() : layer.text;
    const lines = wrapLines(text, W * 0.94);
    const lineHeight = fontSize * 1.1;
    const height = lines.length * lineHeight;
    const width = Math.max(0, ...lines.map((l) => ctx.measureText(l).width));
    const cx = layer.x * W;
    let top = layer.y * H - height / 2;
    top = Math.max(4, Math.min(top, H - height - 4)); // keep inside the image
    return { lines, fontSize, lineHeight, cx, top, box: { x: cx - width / 2, y: top, w: width, h: height } };
  }

  function draw() {
    if (!ed.image) return;
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(ed.image, 0, 0, canvas.width, canvas.height);
    ctx.textAlign = 'center';
    ctx.textBaseline = 'top';
    ctx.lineJoin = 'round';
    for (const layer of ed.layers) {
      const { lines, fontSize, lineHeight, cx, top } = layout(layer);
      ctx.fillStyle = ed.style.color;
      ctx.strokeStyle = ed.style.stroke;
      ctx.lineWidth = Math.max(2, fontSize / 8);
      lines.forEach((line, i) => {
        const y = top + i * lineHeight;
        ctx.strokeText(line, cx, y);
        ctx.fillText(line, cx, y);
      });
    }
  }

  // Dragging text
  let drag = null;
  function toCanvas(e) {
    const r = canvas.getBoundingClientRect();
    return { x: (e.clientX - r.left) * (canvas.width / r.width), y: (e.clientY - r.top) * (canvas.height / r.height) };
  }
  canvas.addEventListener('pointerdown', (e) => {
    const p = toCanvas(e);
    for (let i = ed.layers.length - 1; i >= 0; i--) { // topmost layer wins
      const { box } = layout(ed.layers[i]);
      const pad = 10;
      if (p.x >= box.x - pad && p.x <= box.x + box.w + pad && p.y >= box.y - pad && p.y <= box.y + box.h + pad) {
        drag = { i, dx: p.x - ed.layers[i].x * canvas.width, dy: p.y - ed.layers[i].y * canvas.height };
        setActive(i);
        canvas.setPointerCapture(e.pointerId);
        canvas.classList.add('dragging');
        return;
      }
    }
  });
  canvas.addEventListener('pointermove', (e) => {
    if (!drag) return;
    const p = toCanvas(e);
    const layer = ed.layers[drag.i];
    layer.x = Math.min(1, Math.max(0, (p.x - drag.dx) / canvas.width));
    layer.y = Math.min(1, Math.max(0, (p.y - drag.dy) / canvas.height));
    draw();
  });
  const endDrag = () => { drag = null; canvas.classList.remove('dragging'); };
  canvas.addEventListener('pointerup', endDrag);
  canvas.addEventListener('pointercancel', endDrag);

  $('add-text').addEventListener('click', () => {
    ed.layers.push({ text: 'NEW TEXT', x: 0.5, y: 0.5, size: 0.07 });
    ed.active = ed.layers.length - 1;
    renderLayers(); draw();
    $('layers').lastElementChild.querySelector('textarea').select();
  });
  $('font').addEventListener('change', (e) => { ed.style.font = e.target.value; draw(); });
  $('color').addEventListener('input', (e) => { ed.style.color = e.target.value; draw(); });
  $('stroke').addEventListener('input', (e) => { ed.style.stroke = e.target.value; draw(); });
  $('uppercase').addEventListener('change', (e) => { ed.style.uppercase = e.target.checked; draw(); });

  const fileName = () => `${(ed.template?.name || '').replace(/[^\w-]+/g, '-').toLowerCase() || 'meme'}.png`;
  const canvasBlob = () => new Promise((resolve) => canvas.toBlob(resolve, 'image/png'));

  $('download').addEventListener('click', () => {
    el('a', { download: fileName(), href: canvas.toDataURL('image/png') }).click();
  });

  $('copy').addEventListener('click', async () => {
    if (!navigator.clipboard || !window.ClipboardItem) {
      $('editor-status').textContent = 'Copying images is not supported in this browser — use Download.';
      return;
    }
    try {
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': canvasBlob() })]);
      $('editor-status').textContent = 'Copied to clipboard!';
    } catch {
      $('editor-status').textContent = 'Copy failed — use Download instead.';
    }
  });

  // Save & share
  $('open-save').addEventListener('click', () => requireLogin(() => {
    $('save-form').hidden = false;
    $('save-result').hidden = true;
    $('save-error').textContent = '';
    $('save-dialog').showModal();
  }));
  $('save-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const f = e.target;
    const form = new FormData();
    form.append('image', await canvasBlob(), fileName());
    form.append('title', f.title.value);
    form.append('public', f.public.checked ? 'true' : 'false');
    if (ed.template?.id) form.append('template_id', ed.template.id);
    try {
      const meme = await api('POST', '/api/memes', form);
      const link = new URL(meme.share_url, location.origin).href;
      $('share-link').value = link;
      $('open-share').href = link;
      f.reset();
      f.hidden = true;
      $('save-result').hidden = false;
    } catch (err) {
      $('save-error').textContent = err.message;
    }
  });
  $('copy-link').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText($('share-link').value); $('copy-link').textContent = 'Copied!'; }
    catch { $('share-link').select(); }
    setTimeout(() => { $('copy-link').textContent = 'Copy'; }, 1500);
  });

  // ---------- Feed & my memes ----------
  const fq = { mode: 'feed', sort: 'new', offset: 0, done: false };

  async function loadFeed(mode, reset) {
    if (reset) Object.assign(fq, { mode, offset: 0, done: false });
    $('feed-title').textContent = mode === 'mine' ? 'My memes' : 'Feed';
    $('feed-sort').hidden = mode === 'mine';
    const limit = 30;
    const path = mode === 'mine'
      ? `/api/me/memes?limit=${limit}&offset=${fq.offset}`
      : `/api/memes?sort=${fq.sort}&limit=${limit}&offset=${fq.offset}`;
    try {
      const { memes } = await api('GET', path);
      fq.offset += memes.length;
      fq.done = memes.length < limit;
      const grid = $('meme-grid');
      if (reset) grid.replaceChildren();
      grid.append(...memes.map(memeCard));
      $('feed-status').textContent = grid.children.length ? ''
        : mode === 'mine' ? 'You haven\'t saved any memes yet. Pick a template and hit "Save & share".'
          : 'No memes yet — be the first!';
      $('feed-more').hidden = fq.done;
    } catch (err) {
      $('feed-status').textContent = `Could not load memes: ${err.message}`;
    }
  }

  function memeCard(m) {
    const like = el('button', { className: 'like' + (m.liked ? ' liked' : '') });
    const renderLike = () => {
      like.textContent = `${m.liked ? '♥' : '♡'} ${m.likes}`;
      like.classList.toggle('liked', m.liked);
      like.setAttribute('aria-pressed', m.liked);
    };
    renderLike();
    like.addEventListener('click', () => requireLogin(async () => {
      try {
        const r = await api(m.liked ? 'DELETE' : 'PUT', `/api/memes/${m.id}/like`);
        m.liked = r.liked; m.likes = r.likes;
        renderLike();
      } catch (err) { alert(err.message); }
    }));

    const meta = el('div', { className: 'meme-meta' },
      el('span', { className: 'author', textContent: m.author }), like);
    const card = el('div', { className: 'meme-card' },
      el('a', { href: m.share_url, target: '_blank', rel: 'noopener' },
        el('img', { src: m.image_url, alt: m.title || 'meme', loading: 'lazy', width: m.width, height: m.height })),
      m.title ? el('p', { className: 'meme-title', textContent: m.title }) : '',
      meta);
    if (fq.mode === 'mine') {
      if (!m.public) meta.prepend(el('span', { className: 'badge', textContent: 'unlisted' }));
      const del = el('button', { className: 'btn link danger', textContent: 'Delete' });
      del.addEventListener('click', async () => {
        if (!confirm('Delete this meme? Its share link will stop working.')) return;
        try { await api('DELETE', `/api/memes/${m.id}`); card.remove(); } catch (err) { alert(err.message); }
      });
      meta.append(del);
    }
    return card;
  }

  $('feed-sort').addEventListener('click', (e) => {
    const s = e.target.dataset.sort;
    if (!s) return;
    fq.sort = s;
    [...$('feed-sort').children].forEach((b) => b.classList.toggle('active', b.dataset.sort === s));
    loadFeed('feed', true);
  });
  $('feed-more').addEventListener('click', () => loadFeed(fq.mode, false));

  // ---------- Boot ----------
  api('GET', '/api/me')
    .then(({ user: u }) => setUser(u))
    .catch(() => setUser(null))
    .finally(route);
})();
