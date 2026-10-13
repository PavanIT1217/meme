// Meme Forge frontend. Pages are rendered by the server (so search engines see real
// content); this script adds the interactive parts on top. No dependencies.
(() => {
  document.documentElement.classList.add('js');

  // Links from before real URLs existed (/#/editor/12, /#/feed …) still work.
  if (location.hash.startsWith('#/')) {
    const [, page, arg] = location.hash.split('/');
    const to = page === 'editor' && /^\d+$/.test(arg || '') ? `/templates/${arg}`
      : page === 'feed' ? '/feed' : page === 'mine' ? '/me/memes' : '/';
    location.replace(to);
    return;
  }

  const $ = (id) => document.getElementById(id);
  const el = (tag, props = {}, ...children) => {
    const node = Object.assign(document.createElement(tag), props);
    node.append(...children.filter((c) => c != null && c !== false));
    return node;
  };
  const view = document.body.dataset.view;
  const memeName = (name) => name.trim().replace(/\s+memes?$/i, '') || name; // mirrors memeName() on the server
  const pageNum = Math.max(1, parseInt(new URLSearchParams(location.search).get('page'), 10) || 1);

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

  // ---------- Notices ----------
  let noticeTimer;
  function notice(msg) {
    const n = $('notice');
    n.textContent = msg;
    n.hidden = false;
    clearTimeout(noticeTimer);
    noticeTimer = setTimeout(() => { n.hidden = true; }, 6000);
  }

  // ---------- Auth (Google) ----------
  let user = null;
  let googleLogin = false;

  function setUser(u) {
    user = u;
    document.querySelectorAll('.auth-only').forEach((n) => { n.hidden = !user; });
    document.querySelectorAll('.anon-only').forEach((n) => { n.hidden = !!user; });
    $('whoami').textContent = user ? user.username : '';
    $('avatar').hidden = !user?.avatar_url;
    if (user?.avatar_url) $('avatar').src = user.avatar_url;
  }

  // Resolves once we know who is signed in.
  const authReady = api('GET', '/api/me')
    .then(({ user: u, google_login: g }) => { googleLogin = g; setUser(u); })
    .catch(() => setUser(null));

  // Sends the user to Google and brings them back to this page.
  function signIn() {
    if (!googleLogin) {
      notice('Sign-in is not configured on this server yet.');
      return;
    }
    location.href = `/auth/google/login?next=${encodeURIComponent(location.pathname + location.search)}`;
  }

  // Runs fn now if signed in; otherwise signs in first. onRedirect can save state before leaving.
  function requireLogin(fn, onRedirect) {
    if (user) return fn();
    if (googleLogin && onRedirect) onRedirect();
    signIn();
  }

  $('login').addEventListener('click', signIn);
  $('logout').addEventListener('click', async () => {
    await api('POST', '/api/auth/logout').catch(() => {});
    if (view === 'mine') location.href = '/'; else location.reload();
  });
  $('whoami').addEventListener('click', async () => {
    const name = prompt('Choose a username (3-24 letters, digits or _):', user.username);
    if (!name || name === user.username) return;
    try {
      const { user: u } = await api('PATCH', '/api/me', { username: name });
      setUser(u);
      notice(`You are now ${u.username}.`);
    } catch (err) { notice(err.message); }
  });

  document.querySelectorAll('dialog').forEach((d) => {
    d.addEventListener('click', (e) => { if (e.target.closest('[data-close]')) d.close(); });
  });

  // ---------- Cards (same markup as the server templates) ----------
  function templateCard(t) {
    const card = el('div', { className: 'card' },
      el('a', { className: 'card-link', href: t.url },
        el('img', { src: t.thumb_url, alt: `${memeName(t.name)} meme template`, width: t.width, height: t.height, loading: 'lazy', decoding: 'async' }),
        el('span', { className: 'card-title', textContent: t.name })),
      favButton(t.id, t.name, t.favorite));
    if (t.can_delete) {
      card.append(el('button', { type: 'button', className: 'icon-btn del', title: 'Delete template', textContent: '×' }));
      card.lastChild.dataset.delTemplate = t.id;
      card.lastChild.dataset.name = t.name;
    }
    return card;
  }

  function favButton(id, name, on) {
    const b = el('button', { type: 'button', className: 'icon-btn fav' });
    b.dataset.fav = id;
    b.setAttribute('aria-label', `Favourite ${name}`);
    setFav(b, on);
    return b;
  }
  function setFav(b, on) {
    b.setAttribute('aria-pressed', on);
    b.textContent = on ? '★' : '☆';
  }

  function memeCard(m, mine) {
    const alt = m.title || (m.template_name ? `${memeName(m.template_name)} meme by ${m.author}` : `Meme by ${m.author}`);
    const like = el('button', { type: 'button', className: 'like' });
    like.dataset.like = m.id;
    like.setAttribute('aria-label', 'Like');
    setLike(like, m.liked, m.likes);
    const meta = el('div', { className: 'meme-meta' },
      mine && !m.public ? el('span', { className: 'badge', textContent: 'unlisted' }) : null,
      el('span', { className: 'author', textContent: `by ${m.author}` }), like);
    if (mine) {
      const del = el('button', { type: 'button', className: 'btn link danger', textContent: 'Delete' });
      del.dataset.delMeme = m.id;
      meta.append(del);
    }
    return el('article', { className: 'meme-card' },
      el('a', { href: m.share_url },
        el('img', { src: m.thumb_url, alt, width: m.width, height: m.height, loading: 'lazy', decoding: 'async' })),
      m.title ? el('p', { className: 'meme-title', textContent: m.title }) : null,
      meta);
  }
  function setLike(b, liked, likes) {
    b.setAttribute('aria-pressed', liked);
    b.classList.toggle('liked', liked);
    b.dataset.likes = likes;
    b.textContent = `${liked ? '♥' : '♡'} ${likes}`;
  }

  // One click handler for favourite / like / delete buttons, wherever they are on the page.
  document.addEventListener('click', (e) => {
    const fav = e.target.closest('[data-fav]');
    const like = e.target.closest('[data-like]');
    const delT = e.target.closest('[data-del-template]');
    const delM = e.target.closest('[data-del-meme]');
    if (fav) {
      requireLogin(async () => {
        const on = fav.getAttribute('aria-pressed') !== 'true';
        try { await api(on ? 'PUT' : 'DELETE', `/api/templates/${fav.dataset.fav}/favorite`); setFav(fav, on); }
        catch (err) { notice(err.message); }
      });
    } else if (like) {
      requireLogin(async () => {
        const liked = like.getAttribute('aria-pressed') === 'true';
        try {
          const r = await api(liked ? 'DELETE' : 'PUT', `/api/memes/${like.dataset.like}/like`);
          document.querySelectorAll(`[data-like="${like.dataset.like}"]`).forEach((b) => setLike(b, r.liked, r.likes));
        } catch (err) { notice(err.message); }
      });
    } else if (delT) {
      if (!confirm(`Delete template "${delT.dataset.name}"?`)) return;
      api('DELETE', `/api/templates/${delT.dataset.delTemplate}`)
        .then(() => delT.closest('.card').remove()).catch((err) => notice(err.message));
    } else if (delM) {
      if (!confirm('Delete this meme? Its share link will stop working.')) return;
      api('DELETE', `/api/memes/${delM.dataset.delMeme}`)
        .then(() => delM.closest('.meme-card').remove()).catch((err) => notice(err.message));
    }
  });

  // ---------- Template gallery (home and tag pages) ----------
  const gallery = { q: '', sort: '', tag: '', favorites: false, offset: 0, total: 0 };

  function initGallery() {
    const section = $('view-templates');
    Object.assign(gallery, {
      q: section.dataset.q, sort: section.dataset.sort, tag: document.body.dataset.tag || '',
      total: +section.dataset.total, offset: (pageNum - 1) * 60 + +section.dataset.count,
    });

    let searchTimer;
    $('search').addEventListener('input', () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(() => { gallery.q = $('search').value.trim(); loadTemplates(true); }, 250);
    });
    $('search-form').addEventListener('submit', (e) => { e.preventDefault(); gallery.q = $('search').value.trim(); loadTemplates(true); });
    $('sort').addEventListener('change', () => { gallery.sort = $('sort').value; loadTemplates(true); });
    $('templates-more').addEventListener('click', (e) => { e.preventDefault(); loadTemplates(false); });

    $('open-upload').addEventListener('click', () => requireLogin(() => {
      $('upload-error').textContent = '';
      $('upload-dialog').showModal();
    }));
    $('upload-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      try {
        const t = await api('POST', '/api/templates', new FormData(e.target));
        location.href = t.url;
      } catch (err) {
        $('upload-error').textContent = err.message;
      }
    });
  }

  // Keeps the address bar in sync with the search so results can be shared (home page only).
  function syncGalleryURL() {
    if (view !== 'home') return;
    const p = new URLSearchParams();
    if (gallery.q) p.set('q', gallery.q);
    if (gallery.sort && gallery.sort !== 'popular') p.set('sort', gallery.sort);
    history.replaceState(null, '', p.toString() ? `/?${p}` : '/');
  }

  async function loadTemplates(reset) {
    const offset = reset ? 0 : gallery.offset;
    const params = new URLSearchParams({ sort: gallery.sort || 'popular', limit: 60, offset });
    if (gallery.q) params.set('q', gallery.q);
    if (gallery.tag) params.set('tag', gallery.tag);
    if (gallery.favorites) params.set('favorites', '1');
    try {
      const { templates, total } = await api('GET', `/api/templates?${params}`);
      const grid = $('template-grid');
      if (reset) grid.replaceChildren();
      grid.append(...templates.map(templateCard));
      gallery.total = total;
      gallery.offset = offset + templates.length;
      $('templates-status').textContent = total
        ? `${total} template${total === 1 ? '' : 's'}`
        : gallery.favorites ? 'No favourites yet — tap ☆ on a template to save it.' : 'No templates match.';
      $('templates-more').hidden = gallery.offset >= total;
      if (reset) syncGalleryURL();
    } catch (err) {
      notice(`Could not load templates: ${err.message}`); // keep what the server rendered
    }
  }

  function addFavouritesChip() {
    const chip = el('button', { type: 'button', className: 'chip', textContent: '★ Favourites' });
    chip.addEventListener('click', () => {
      gallery.favorites = !gallery.favorites;
      chip.classList.toggle('active', gallery.favorites);
      loadTemplates(true);
    });
    $('tags').firstElementChild.after(chip);
  }

  // ---------- Editor ----------
  const canvas = $('canvas');
  const ctx = canvas?.getContext('2d');
  const ed = {
    image: null,
    template: null, // { id, name, box_count }; id is null for a local image
    layers: [],     // { text, x, y (0..1 of canvas), size (fraction of width) }
    active: 0,
    style: { font: $('font')?.value, color: '#ffffff', stroke: '#000000', uppercase: true },
  };

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

  // Editor drafts survive the round trip to Google's sign-in page.
  const DRAFT_KEY = 'meme-draft';
  function saveDraft(action) {
    if (!ed.template?.id) return;
    try {
      sessionStorage.setItem(DRAFT_KEY, JSON.stringify({ templateId: ed.template.id, layers: ed.layers, style: ed.style, action }));
    } catch { /* storage unavailable: the draft is simply lost */ }
  }
  function takeDraft(templateId) {
    try {
      const d = JSON.parse(sessionStorage.getItem(DRAFT_KEY));
      sessionStorage.removeItem(DRAFT_KEY);
      return d && d.templateId === templateId ? d : null;
    } catch { return null; }
  }

  function startEditor(src, template) {
    const img = new Image();
    img.crossOrigin = 'anonymous';
    img.onload = () => {
      const draft = template.id ? takeDraft(template.id) : null;
      ed.image = img;
      ed.template = template;
      ed.layers = draft?.layers || defaultLayers(template.box_count);
      if (draft?.style) {
        Object.assign(ed.style, draft.style);
        $('font').value = ed.style.font;
        $('color').value = ed.style.color;
        $('stroke').value = ed.style.stroke;
        $('uppercase').checked = ed.style.uppercase;
      }
      ed.active = 0;
      // Cap working resolution so huge images stay snappy and uploads stay small.
      const scale = Math.min(1, 1200 / Math.max(img.naturalWidth, img.naturalHeight));
      canvas.width = Math.round(img.naturalWidth * scale);
      canvas.height = Math.round(img.naturalHeight * scale);
      canvas.hidden = false;
      $('editor-placeholder')?.remove();
      if ($('local-picker')) $('local-picker').hidden = true;
      $('editor-status').textContent = '';
      renderLayers();
      draw();
      if (draft?.action === 'save') authReady.then(() => { if (user) openSaveDialog(); });
    };
    img.onerror = () => { $('editor-status').textContent = 'Could not load that image.'; };
    img.src = src;
  }

  function renderLayers() {
    $('layers').replaceChildren(...ed.layers.map((layer, i) => {
      const ta = el('textarea', { value: layer.text, placeholder: `Text ${i + 1}` });
      ta.setAttribute('aria-label', `Text ${i + 1}`);
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

  const fileName = () => `${(ed.template?.name || '').replace(/[^\w-]+/g, '-').toLowerCase() || 'meme'}.png`;
  const canvasBlob = () => new Promise((resolve) => canvas.toBlob(resolve, 'image/png'));

  function openSaveDialog() {
    $('save-form').hidden = false;
    $('save-result').hidden = true;
    $('save-error').textContent = '';
    $('save-dialog').showModal();
  }

  function initEditor() {
    // Dragging text
    let drag = null;
    const toCanvas = (e) => {
      const r = canvas.getBoundingClientRect();
      return { x: (e.clientX - r.left) * (canvas.width / r.width), y: (e.clientY - r.top) * (canvas.height / r.height) };
    };
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

    $('download').addEventListener('click', () => {
      if (!ed.image) return;
      el('a', { download: fileName(), href: canvas.toDataURL('image/png') }).click();
    });
    $('copy').addEventListener('click', async () => {
      if (!ed.image) return;
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

    $('open-save').addEventListener('click', () => {
      if (!ed.image) return;
      requireLogin(openSaveDialog, () => saveDraft('save'));
    });
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
    $('copy-link').addEventListener('click', () => copyText($('share-link').value, $('copy-link')));

    if (view === 'template') {
      const t = JSON.parse($('template-data').textContent);
      startEditor(t.image_url, t);
    } else {
      $('local-image').addEventListener('change', (e) => {
        const file = e.target.files[0];
        if (!file) return;
        const reader = new FileReader();
        reader.onload = () => startEditor(reader.result, { id: null, name: file.name.replace(/\.[^.]+$/, ''), box_count: 2 });
        reader.readAsDataURL(file);
      });
    }
  }

  async function copyText(text, button) {
    try { await navigator.clipboard.writeText(text); button.textContent = 'Copied!'; }
    catch { prompt('Copy this link:', text); }
    setTimeout(() => { button.textContent = button.id === 'copy-page-link' ? 'Copy link' : 'Copy'; }, 1500);
  }

  // ---------- Feed & my memes ----------
  const feed = { mode: 'feed', sort: 'new', offset: 0 };

  function initFeed() {
    const section = $('view-feed');
    Object.assign(feed, { mode: section.dataset.mode, sort: section.dataset.sort,
      offset: (pageNum - 1) * 30 + +section.dataset.count });
    $('feed-more').addEventListener('click', (e) => { e.preventDefault(); loadFeed(false); });
  }

  async function loadFeed(reset) {
    const limit = 30;
    const offset = reset ? (feed.mode === 'mine' ? 0 : (pageNum - 1) * 30) : feed.offset;
    const path = feed.mode === 'mine'
      ? `/api/me/memes?limit=${limit}&offset=${offset}`
      : `/api/memes?sort=${feed.sort}&limit=${limit}&offset=${offset}`;
    try {
      const { memes } = await api('GET', path);
      const grid = $('meme-grid');
      if (reset) grid.replaceChildren();
      grid.append(...memes.map((m) => memeCard(m, feed.mode === 'mine')));
      feed.offset = offset + memes.length;
      $('feed-more').hidden = memes.length < limit;
      $('feed-status').textContent = grid.children.length ? ''
        : feed.mode === 'mine' ? 'You haven\'t saved any memes yet. Pick a template and hit "Save & share".'
          : 'No memes yet. Make the first one!';
    } catch (err) {
      notice(`Could not load memes: ${err.message}`);
    }
  }

  // ---------- Meme page ----------
  function initMemePage() {
    $('copy-page-link').addEventListener('click', () => copyText(location.origin + location.pathname, $('copy-page-link')));
  }

  // Pages are rendered for anonymous visitors; fill in per-user state once we know who is here.
  async function personalise() {
    if (view === 'mine') {
      if (!user) {
        $('feed-status').replaceChildren('Sign in to see the memes you have saved. ',
          el('button', { className: 'btn primary', textContent: 'Sign in with Google', onclick: signIn }));
        $('feed-more').hidden = true;
        return;
      }
      return loadFeed(true);
    }
    if (!user) return;
    if (view === 'home' || view === 'tag') {
      addFavouritesChip();
      gallery.offset = (pageNum - 1) * 60;
      const params = new URLSearchParams({ sort: gallery.sort || 'popular', limit: Math.max(60, $('template-grid').children.length), offset: gallery.offset });
      if (gallery.q) params.set('q', gallery.q);
      if (gallery.tag) params.set('tag', gallery.tag);
      try {
        const { templates } = await api('GET', `/api/templates?${params}`);
        $('template-grid').replaceChildren(...templates.map(templateCard));
        gallery.offset += templates.length;
      } catch { /* keep the server-rendered cards */ }
    } else if (view === 'feed') {
      loadFeed(true);
    } else if (view === 'meme') {
      try {
        const m = await api('GET', `/api/memes/${document.body.dataset.memeId}`);
        document.querySelectorAll(`[data-like="${m.id}"]`).forEach((b) => setLike(b, m.liked, m.likes));
      } catch { /* ignore */ }
    }
  }

  // ---------- Boot ----------
  if (view === 'home' || view === 'tag') initGallery();
  if (view === 'template' || view === 'editor') initEditor();
  if (view === 'feed' || view === 'mine') initFeed();
  if (view === 'meme') initMemePage();

  const params = new URLSearchParams(location.search);
  if (params.has('login')) {
    if (params.get('login') === 'failed') notice('Google sign-in failed. Please try again.');
    params.delete('login');
    history.replaceState(null, '', location.pathname + (params.toString() ? `?${params}` : ''));
  }
  authReady.then(personalise);
})();
