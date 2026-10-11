// Meme Forge — template gallery + canvas meme editor. No dependencies.
(() => {
  const $ = (id) => document.getElementById(id);
  const canvas = $('canvas');
  const ctx = canvas.getContext('2d');

  const state = {
    templates: [],
    image: null,
    name: '',
    layers: [],          // { text, x, y (0..1 of canvas), size (fraction of width) }
    active: 0,
    style: { font: $('font').value, color: '#ffffff', stroke: '#000000', uppercase: true },
  };

  // ---------- Gallery ----------
  async function loadTemplates() {
    try {
      const res = await fetch('https://api.imgflip.com/get_memes');
      const json = await res.json();
      if (!json.success) throw new Error('API error');
      state.templates = json.data.memes;
      renderGallery();
    } catch (err) {
      $('gallery-status').textContent = 'Could not load templates. You can still upload your own image.';
    }
  }

  function renderGallery() {
    const q = $('search').value.trim().toLowerCase();
    const list = state.templates.filter((t) => t.name.toLowerCase().includes(q));
    $('gallery-status').textContent = list.length
      ? `${list.length} template${list.length === 1 ? '' : 's'}`
      : 'No templates match that search.';
    const gallery = $('gallery');
    gallery.replaceChildren(...list.map((t) => {
      const card = document.createElement('button');
      card.className = 'card';
      const img = document.createElement('img');
      img.src = t.url;
      img.alt = t.name;
      img.loading = 'lazy';
      const label = document.createElement('span');
      label.textContent = t.name;
      card.append(img, label);
      card.addEventListener('click', () => openEditor(t.url, t.name, t.box_count));
      return card;
    }));
  }

  // ---------- Editor ----------
  function defaultLayers(count) {
    if (count <= 2) {
      return [
        { text: 'TOP TEXT', x: 0.5, y: 0.08, size: 0.09 },
        { text: 'BOTTOM TEXT', x: 0.5, y: 0.92, size: 0.09 },
      ].slice(0, Math.max(count, 2));
    }
    // Spread extra boxes vertically down the image.
    return Array.from({ length: count }, (_, i) => ({
      text: `TEXT ${i + 1}`, x: 0.5, y: (i + 0.5) / count, size: 0.06,
    }));
  }

  function openEditor(src, name, boxCount = 2) {
    const img = new Image();
    img.crossOrigin = 'anonymous';
    img.onload = () => {
      state.image = img;
      state.name = name;
      state.layers = defaultLayers(boxCount);
      state.active = 0;
      // Cap working resolution so huge uploads stay snappy.
      const scale = Math.min(1, 1200 / Math.max(img.naturalWidth, img.naturalHeight));
      canvas.width = Math.round(img.naturalWidth * scale);
      canvas.height = Math.round(img.naturalHeight * scale);
      $('template-name').textContent = name;
      $('editor-status').textContent = '';
      $('gallery-view').hidden = true;
      $('editor-view').hidden = false;
      renderLayers();
      draw();
      window.scrollTo(0, 0);
    };
    img.onerror = () => alert('Could not load that image.');
    img.src = src;
  }

  function renderLayers() {
    const wrap = $('layers');
    wrap.replaceChildren(...state.layers.map((layer, i) => {
      const row = document.createElement('div');
      row.className = 'layer' + (i === state.active ? ' active' : '');

      const ta = document.createElement('textarea');
      ta.value = layer.text;
      ta.placeholder = `Text ${i + 1}`;
      ta.addEventListener('input', () => { layer.text = ta.value; draw(); });
      ta.addEventListener('focus', () => setActive(i));

      const sizeCol = document.createElement('label');
      sizeCol.className = 'col';
      const range = document.createElement('input');
      range.type = 'range'; range.min = 0.02; range.max = 0.2; range.step = 0.005;
      range.value = layer.size;
      range.addEventListener('input', () => { layer.size = +range.value; draw(); });
      sizeCol.append(range, 'size');

      const del = document.createElement('button');
      del.className = 'btn remove';
      del.title = 'Remove text box';
      del.textContent = '×';
      del.addEventListener('click', () => {
        state.layers.splice(i, 1);
        state.active = Math.max(0, Math.min(state.active, state.layers.length - 1));
        renderLayers(); draw();
      });

      row.append(ta, sizeCol, del);
      return row;
    }));
  }

  function setActive(i) {
    state.active = i;
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
    ctx.font = `bold ${fontSize}px ${state.style.font}`;
    const text = state.style.uppercase ? layer.text.toUpperCase() : layer.text;
    const lines = wrapLines(text, W * 0.94);
    const lineHeight = fontSize * 1.1;
    const height = lines.length * lineHeight;
    const width = Math.max(0, ...lines.map((l) => ctx.measureText(l).width));
    // Keep the block inside the image.
    const cx = layer.x * W;
    let top = layer.y * H - height / 2;
    top = Math.max(4, Math.min(top, H - height - 4));
    return { lines, fontSize, lineHeight, cx, top, box: { x: cx - width / 2, y: top, w: width, h: height } };
  }

  function draw() {
    if (!state.image) return;
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(state.image, 0, 0, canvas.width, canvas.height);
    ctx.textAlign = 'center';
    ctx.textBaseline = 'top';
    ctx.lineJoin = 'round';
    for (const layer of state.layers) {
      const { lines, fontSize, lineHeight, cx, top } = layout(layer);
      ctx.fillStyle = state.style.color;
      ctx.strokeStyle = state.style.stroke;
      ctx.lineWidth = Math.max(2, fontSize / 8);
      lines.forEach((line, i) => {
        const y = top + i * lineHeight;
        ctx.strokeText(line, cx, y);
        ctx.fillText(line, cx, y);
      });
    }
  }

  // ---------- Dragging ----------
  let drag = null;
  function toCanvas(e) {
    const r = canvas.getBoundingClientRect();
    return { x: (e.clientX - r.left) * (canvas.width / r.width), y: (e.clientY - r.top) * (canvas.height / r.height) };
  }
  canvas.addEventListener('pointerdown', (e) => {
    const p = toCanvas(e);
    // Topmost layer wins.
    for (let i = state.layers.length - 1; i >= 0; i--) {
      const { box } = layout(state.layers[i]);
      const pad = 10;
      if (p.x >= box.x - pad && p.x <= box.x + box.w + pad && p.y >= box.y - pad && p.y <= box.y + box.h + pad) {
        drag = { i, dx: p.x - state.layers[i].x * canvas.width, dy: p.y - state.layers[i].y * canvas.height };
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
    const layer = state.layers[drag.i];
    layer.x = Math.min(1, Math.max(0, (p.x - drag.dx) / canvas.width));
    layer.y = Math.min(1, Math.max(0, (p.y - drag.dy) / canvas.height));
    draw();
  });
  const endDrag = () => { drag = null; canvas.classList.remove('dragging'); };
  canvas.addEventListener('pointerup', endDrag);
  canvas.addEventListener('pointercancel', endDrag);

  // ---------- Controls ----------
  $('search').addEventListener('input', renderGallery);
  $('back').addEventListener('click', () => { $('editor-view').hidden = true; $('gallery-view').hidden = false; });
  $('add-text').addEventListener('click', () => {
    state.layers.push({ text: 'NEW TEXT', x: 0.5, y: 0.5, size: 0.07 });
    state.active = state.layers.length - 1;
    renderLayers(); draw();
    $('layers').lastElementChild.querySelector('textarea').select();
  });
  $('font').addEventListener('change', (e) => { state.style.font = e.target.value; draw(); });
  $('color').addEventListener('input', (e) => { state.style.color = e.target.value; draw(); });
  $('stroke').addEventListener('input', (e) => { state.style.stroke = e.target.value; draw(); });
  $('uppercase').addEventListener('change', (e) => { state.style.uppercase = e.target.checked; draw(); });

  $('upload').addEventListener('change', (e) => {
    const file = e.target.files[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => openEditor(reader.result, file.name.replace(/\.[^.]+$/, ''), 2);
    reader.readAsDataURL(file);
    e.target.value = '';
  });

  $('download').addEventListener('click', () => {
    const a = document.createElement('a');
    a.download = `${state.name.replace(/[^\w-]+/g, '-').toLowerCase() || 'meme'}.png`;
    a.href = canvas.toDataURL('image/png');
    a.click();
  });

  $('copy').addEventListener('click', () => {
    if (!navigator.clipboard || !window.ClipboardItem) {
      $('editor-status').textContent = 'Copying images is not supported in this browser — use Download.';
      return;
    }
    canvas.toBlob(async (blob) => {
      try {
        await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]);
        $('editor-status').textContent = 'Copied to clipboard!';
      } catch {
        $('editor-status').textContent = 'Copy failed — use Download instead.';
      }
    });
  });

  loadTemplates();
})();
