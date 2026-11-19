package detect

// ProbePageHTML is the self-contained HTML + JS adversarial detection page.
// It runs automatically on load, renders an interactive dark-theme report UI,
// exposes window.__runDetectProbes(label) for programmatic evaluation, and
// records behavioral mouse/keyboard telemetry on #probe-input and #probe-btn.
const ProbePageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>XT-Nichrome Adversarial Detection Suite</title>
<style>
  :root { color-scheme: dark; }
  body { background: #0a0a0a; color: #e8e6e3; font-family: "Segoe UI", system-ui, sans-serif; margin: 24px; line-height: 1.45; }
  h1 { font-size: 20px; letter-spacing: 0.04em; border-bottom: 1px solid #262626; padding-bottom: 8px; }
  .bar { display: flex; gap: 16px; align-items: center; margin: 16px 0; padding: 12px; background: #121212; border: 1px solid #262626; }
  .badge { padding: 4px 10px; font-weight: 700; font-size: 13px; }
  .pass { background: #0f2f1e; color: #4ade80; border: 1px solid #166534; }
  .fail { background: #3b0d0d; color: #ff2e2e; border: 1px solid #7f1d1d; }
  .interactive { margin: 16px 0; padding: 14px; background: #141414; border: 1px solid #2a2a2a; display: flex; gap: 12px; align-items: center; }
  input[type=text] { background: #0a0a0a; color: #e8e6e3; border: 1px solid #3f3f46; padding: 6px 10px; width: 240px; }
  button { background: #e8e6e3; color: #0a0a0a; border: none; padding: 7px 16px; font-weight: 600; cursor: pointer; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; margin-top: 12px; }
  th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #1f1f1f; vertical-align: top; }
  th { color: #a1a1aa; font-weight: 600; }
  code { font-family: Consolas, monospace; font-size: 12px; color: #d4d4d8; }
</style>
</head>
<body>
<h1>XT-NICHROME // ADVERSARIAL DETECTION &amp; BEHAVIORAL PROBE</h1>
<div class="bar">
  <span id="classification" class="badge">RUNNING...</span>
  <span>Suspicion Score: <strong id="score">-</strong></span>
  <span>Passed: <strong id="passed">-</strong></span>
  <span>Failed: <strong id="failed">-</strong></span>
  <span>WebGL Params: <strong id="webgl-count">-</strong></span>
</div>

<div class="interactive">
  <label for="probe-input">Behavioral Input Probe</label>
  <input id="probe-input" name="probe-input" type="text" autocomplete="off" placeholder="Type here...">
  <button id="probe-btn" type="button">Verify Human Action</button>
  <span id="action-status" aria-live="polite">awaiting-action</span>
</div>

<table>
  <thead>
    <tr><th>Status</th><th>Category</th><th>Probe ID</th><th>Observed</th><th>Expected / Note</th></tr>
  </thead>
  <tbody id="probe-rows"></tbody>
</table>

<script>
(() => {
  'use strict';

  const tel = {
    mouseMoves: [],
    mouseDowns: [],
    mouseUps: [],
    clicks: [],
    keyDowns: [],
    keyUps: [],
    inputs: [],
    eventOrder: [],
    untrustedCount: 0,
  };
  window.__behaviorTelemetry = tel;

  const recMouse = (kind, e) => {
    if (!e.isTrusted) tel.untrustedCount++;
    const item = {
      t: performance.now(),
      clientX: e.clientX,
      clientY: e.clientY,
      screenX: e.screenX,
      screenY: e.screenY,
      button: e.button,
      buttons: e.buttons,
      isTrusted: !!e.isTrusted,
    };
    if (kind === 'move') {
      if (tel.mouseMoves.length < 256) tel.mouseMoves.push(item);
    } else if (kind === 'down') {
      const r = e.currentTarget && e.currentTarget.getBoundingClientRect ? e.currentTarget.getBoundingClientRect() : null;
      if (r) {
        item.relX = (e.clientX - r.left) / Math.max(1, r.width);
        item.relY = (e.clientY - r.top) / Math.max(1, r.height);
      }
      tel.mouseDowns.push(item);
      tel.eventOrder.push('mousedown');
    } else if (kind === 'up') {
      tel.mouseUps.push(item);
      tel.eventOrder.push('mouseup');
    } else if (kind === 'click') {
      tel.clicks.push(item);
      tel.eventOrder.push('click');
    }
  };

  window.addEventListener('mousemove', (e) => recMouse('move', e), { passive: true });
  const inputEl = document.getElementById('probe-input');
  const btnEl = document.getElementById('probe-btn');
  const statusEl = document.getElementById('action-status');

  [inputEl, btnEl].forEach((el) => {
    el.addEventListener('mousedown', (e) => recMouse('down', e));
    el.addEventListener('mouseup', (e) => recMouse('up', e));
    el.addEventListener('focus', () => tel.eventOrder.push('focus:' + el.id));
    el.addEventListener('click', (e) => recMouse('click', e));
  });

  inputEl.addEventListener('keydown', (e) => {
    if (!e.isTrusted) tel.untrustedCount++;
    tel.keyDowns.push({
      t: performance.now(),
      key: e.key,
      code: e.code,
      keyCode: e.keyCode,
      shiftKey: e.shiftKey,
      isTrusted: !!e.isTrusted,
    });
    tel.eventOrder.push('keydown:' + e.key);
  });
  inputEl.addEventListener('keyup', (e) => {
    if (!e.isTrusted) tel.untrustedCount++;
    tel.keyUps.push({
      t: performance.now(),
      key: e.key,
      code: e.code,
      keyCode: e.keyCode,
      shiftKey: e.shiftKey,
      isTrusted: !!e.isTrusted,
    });
    tel.eventOrder.push('keyup:' + e.key);
  });
  inputEl.addEventListener('input', (e) => {
    tel.inputs.push({ t: performance.now(), value: inputEl.value, inputType: e.inputType || '' });
  });

  btnEl.addEventListener('click', () => {
    statusEl.textContent = 'verified:' + (inputEl.value || 'empty');
    runProbes('after-action');
  });

  function fnv1aHex(bytes) {
    let h1 = 0x811c9dc5 >>> 0;
    let h2 = 0x811c9dc5 >>> 0;
    for (let i = 0; i < bytes.length; i++) {
      const v = bytes[i] & 0xff;
      if ((i & 1) === 0) {
        h1 ^= v;
        h1 = Math.imul(h1, 0x01000193) >>> 0;
      } else {
        h2 ^= v;
        h2 = Math.imul(h2, 0x01000193) >>> 0;
      }
    }
    return h1.toString(16).padStart(8, '0') + h2.toString(16).padStart(8, '0');
  }

  function probeCanvas() {
    try {
      const c = document.createElement('canvas');
      c.width = 280;
      c.height = 64;
      const ctx = c.getContext('2d');
      const grad = ctx.createLinearGradient(0, 0, 280, 64);
      grad.addColorStop(0, '#ff2e2e');
      grad.addColorStop(0.5, '#1e293b');
      grad.addColorStop(1, '#10b981');
      ctx.fillStyle = grad;
      ctx.fillRect(0, 0, 280, 64);
      ctx.font = '16px "Segoe UI", Arial, sans-serif';
      ctx.fillStyle = '#e8e6e3';
      ctx.fillText('XT-Nichrome Ni-28 58.6934 \u2603 \ud83d\udd12', 10, 28);
      ctx.globalCompositeOperation = 'multiply';
      ctx.fillStyle = 'rgba(59, 130, 246, 0.75)';
      ctx.beginPath();
      ctx.arc(210, 32, 22, 0, Math.PI * 2, true);
      ctx.fill('evenodd');
      const img = ctx.getImageData(0, 0, 280, 64).data;
      let nonZero = 0;
      for (let i = 0; i < img.length; i += 4) {
        if (img[i] !== 0 || img[i+1] !== 0 || img[i+2] !== 0) nonZero++;
      }
      return {
        hash: fnv1aHex(img),
        nonZeroPixels: nonZero,
        totalPixels: 280 * 64,
      };
    } catch (e) {
      return { hash: 'error', nonZeroPixels: 0, error: String(e) };
    }
  }

  async function probeAudio() {
    try {
      const Ctor = window.OfflineAudioContext || window.webkitOfflineAudioContext;
      if (!Ctor) return { hash: 'unsupported', sampleRate: 0, energy: 0 };
      const ctx = new Ctor(1, 4800, 48000);
      const osc = ctx.createOscillator();
      osc.type = 'triangle';
      osc.frequency.value = 10000;
      const comp = ctx.createDynamicsCompressor();
      comp.threshold.value = -50;
      comp.knee.value = 40;
      comp.ratio.value = 12;
      comp.attack.value = 0;
      comp.release.value = 0.25;
      osc.connect(comp);
      comp.connect(ctx.destination);
      osc.start(0);
      const buf = await ctx.startRendering();
      const ch = buf.getChannelData(0);
      let energy = 0;
      const qBytes = new Uint8Array(1000);
      for (let i = 500; i < 1500 && i < ch.length; i++) {
        const s = ch[i];
        energy += Math.abs(s);
        qBytes[i - 500] = Math.floor((s + 1.0) * 127.5) & 0xff;
      }
      let liveSampleRate = 0;
      try {
        const AC = window.AudioContext || window.webkitAudioContext;
        if (AC) {
          const ac = new AC();
          liveSampleRate = ac.sampleRate;
          ac.close && ac.close();
        }
      } catch (e) {}
      return {
        hash: fnv1aHex(qBytes),
        offlineSampleRate: buf.sampleRate,
        liveSampleRate: liveSampleRate || buf.sampleRate,
        energy: Math.round(energy * 1e6) / 1e6,
      };
    } catch (e) {
      return { hash: 'error', sampleRate: 0, energy: 0, error: String(e) };
    }
  }

  function probeWebGL() {
    const c1 = document.createElement('canvas');
    const gl1 = c1.getContext('webgl');
    const c2 = document.createElement('canvas');
    const gl2 = c2.getContext('webgl2');
    if (!gl1) return { count: 0, params: {}, extensions: [] };

    const dbg = gl1.getExtension('WEBGL_debug_renderer_info');
    const aniso = gl1.getExtension('EXT_texture_filter_anisotropic') ||
                  gl1.getExtension('WEBKIT_EXT_texture_filter_anisotropic');

    const gl1Consts = [
      ['VENDOR', 0x1F00], ['RENDERER', 0x1F01], ['VERSION', 0x1F02],
      ['SHADING_LANGUAGE_VERSION', 0x8B8C],
      ['UNMASKED_VENDOR_WEBGL', dbg ? dbg.UNMASKED_VENDOR_WEBGL : 0x9245],
      ['UNMASKED_RENDERER_WEBGL', dbg ? dbg.UNMASKED_RENDERER_WEBGL : 0x9246],
      ['MAX_TEXTURE_SIZE', 0x0D33], ['MAX_CUBE_MAP_TEXTURE_SIZE', 0x851C],
      ['MAX_RENDERBUFFER_SIZE', 0x84E8], ['MAX_VIEWPORT_DIMS', 0x0D3A],
      ['ALIASED_POINT_SIZE_RANGE', 0x846D], ['ALIASED_LINE_WIDTH_RANGE', 0x846E],
      ['MAX_VERTEX_ATTRIBS', 0x8869], ['MAX_VERTEX_UNIFORM_VECTORS', 0x8DFB],
      ['MAX_VARYING_VECTORS', 0x8DFC], ['MAX_COMBINED_TEXTURE_IMAGE_UNITS', 0x8B4D],
      ['MAX_VERTEX_TEXTURE_IMAGE_UNITS', 0x8B4C], ['MAX_TEXTURE_IMAGE_UNITS', 0x8872],
      ['MAX_FRAGMENT_UNIFORM_VECTORS', 0x8DFD], ['RED_BITS', 0x0D52],
      ['GREEN_BITS', 0x0D53], ['BLUE_BITS', 0x0D54], ['ALPHA_BITS', 0x0D55],
      ['DEPTH_BITS', 0x0D56], ['STENCIL_BITS', 0x0D57], ['SUBPIXEL_BITS', 0x0D50],
      ['SAMPLE_BUFFERS', 0x80A8], ['SAMPLES', 0x80A9],
      ['COMPRESSED_TEXTURE_FORMATS', 0x86A3], ['CULL_FACE_MODE', 0x0B45],
      ['FRONT_FACE', 0x0B46], ['GENERATE_MIPMAP_HINT', 0x8192],
      ['IMPLEMENTATION_COLOR_READ_FORMAT', 0x8B9B], ['IMPLEMENTATION_COLOR_READ_TYPE', 0x8B9A],
      ['STENCIL_BACK_VALUE_MASK', 0x8CA4], ['STENCIL_BACK_WRITEMASK', 0x8CA5],
      ['STENCIL_VALUE_MASK', 0x0B93], ['STENCIL_WRITEMASK', 0x0B98],
      ['UNPACK_ALIGNMENT', 0x0CF5], ['PACK_ALIGNMENT', 0x0D05],
    ];
    if (aniso) {
      gl1Consts.push(['MAX_TEXTURE_MAX_ANISOTROPY_EXT', aniso.MAX_TEXTURE_MAX_ANISOTROPY_EXT]);
    }

    const gl2Consts = [
      ['GL2_VERSION', 0x1F02], ['GL2_SHADING_LANGUAGE_VERSION', 0x8B8C],
      ['MAX_3D_TEXTURE_SIZE', 0x8073], ['MAX_ARRAY_TEXTURE_LAYERS', 0x88FF],
      ['MAX_COLOR_ATTACHMENTS', 0x8CDF], ['MAX_DRAW_BUFFERS', 0x8824],
      ['MAX_ELEMENT_INDEX', 0x8D6B], ['MAX_ELEMENTS_INDICES', 0x80E9],
      ['MAX_ELEMENTS_VERTICES', 0x80E8], ['MAX_FRAGMENT_INPUT_COMPONENTS', 0x9125],
      ['MAX_FRAGMENT_UNIFORM_BLOCKS', 0x8A2D], ['MAX_FRAGMENT_UNIFORM_COMPONENTS', 0x8B49],
      ['MAX_PROGRAM_TEXEL_OFFSET', 0x8905], ['MIN_PROGRAM_TEXEL_OFFSET', 0x8904],
      ['MAX_SAMPLES', 0x8D57], ['MAX_SERVER_WAIT_TIMEOUT', 0x9111],
      ['MAX_TEXTURE_LOD_BIAS', 0x84FD],
      ['MAX_TRANSFORM_FEEDBACK_INTERLEAVED_COMPONENTS', 0x8C8A],
      ['MAX_TRANSFORM_FEEDBACK_SEPARATE_ATTRIBS', 0x8C8B],
      ['MAX_TRANSFORM_FEEDBACK_SEPARATE_COMPONENTS', 0x8C80],
      ['MAX_UNIFORM_BLOCK_SIZE', 0x8A30], ['MAX_UNIFORM_BUFFER_BINDINGS', 0x8A2F],
      ['MAX_VARYING_COMPONENTS', 0x8B4B], ['MAX_VERTEX_OUTPUT_COMPONENTS', 0x9122],
      ['MAX_VERTEX_UNIFORM_BLOCKS', 0x8A2B], ['MAX_VERTEX_UNIFORM_COMPONENTS', 0x8B4A],
      ['UNIFORM_BUFFER_OFFSET_ALIGNMENT', 0x8A34], ['MAX_COMBINED_UNIFORM_BLOCKS', 0x8A2E],
      ['MAX_COMBINED_VERTEX_UNIFORM_COMPONENTS', 0x8A31],
      ['MAX_COMBINED_FRAGMENT_UNIFORM_COMPONENTS', 0x8A33],
      ['MAX_CLIENT_WAIT_TIMEOUT_WEBGL', 0x9247],
    ];

    const norm = (v) => {
      if (v && (ArrayBuffer.isView(v) || Array.isArray(v))) return Array.from(v);
      return v;
    };

    const params = {};
    for (const [name, code] of gl1Consts) {
      try { params[name] = norm(gl1.getParameter(code)); } catch (e) { params[name] = null; }
    }
    if (gl2) {
      for (const [name, code] of gl2Consts) {
        try { params[name] = norm(gl2.getParameter(code)); } catch (e) { params[name] = null; }
      }
    }

    const precisions = {};
    const shaders = [['VS', gl1.VERTEX_SHADER], ['FS', gl1.FRAGMENT_SHADER]];
    const pTypes = [['LOW_FLOAT', gl1.LOW_FLOAT], ['MED_FLOAT', gl1.MEDIUM_FLOAT], ['HIGH_FLOAT', gl1.HIGH_FLOAT],
                    ['LOW_INT', gl1.LOW_INT], ['MED_INT', gl1.MEDIUM_INT], ['HIGH_INT', gl1.HIGH_INT]];
    for (const [sName, sCode] of shaders) {
      for (const [pName, pCode] of pTypes) {
        try {
          const pf = gl1.getShaderPrecisionFormat(sCode, pCode);
          precisions[sName + '_' + pName] = pf ? [pf.rangeMin, pf.rangeMax, pf.precision] : null;
        } catch (e) {}
      }
    }

    const exts = gl1.getSupportedExtensions() || [];
    return {
      count: Object.keys(params).length + Object.keys(precisions).length,
      params,
      precisions,
      extensions: exts,
    };
  }

  function probeFonts() {
    const c = document.createElement('canvas');
    const ctx = c.getContext('2d');
    const sample = 'mmmmmmmmmmlliWWWMQ@#0123456789';
    const bases = ['monospace', 'sans-serif', 'serif'];
    const baseWidths = {};
    for (const b of bases) {
      ctx.font = '72px ' + b;
      baseWidths[b] = ctx.measureText(sample).width;
    }
    const measureFam = (fam) => {
      const w = {};
      let resolved = false;
      for (const b of bases) {
        ctx.font = '72px "' + fam + '", ' + b;
        const m = ctx.measureText(sample);
        w[b] = m.width;
        if (m.width !== baseWidths[b]) resolved = true;
      }
      return { resolved, widths: w };
    };

    const winFamilies = [
      'Segoe UI', 'Calibri', 'Cambria', 'Consolas', 'Candara', 'Corbel',
      'Constantia', 'Arial', 'Times New Roman', 'Courier New', 'Georgia',
      'Verdana', 'Tahoma', 'Trebuchet MS', 'Impact', 'Comic Sans MS',
    ];
    const linuxFamilies = [
      'DejaVu Sans', 'Ubuntu', 'Cantarell', 'FreeSans', 'Nimbus Sans', 'Droid Sans',
    ];

    const winResolved = [];
    const winWidths = {};
    for (const f of winFamilies) {
      const m = measureFam(f);
      winWidths[f] = m.widths['monospace'];
      if (m.resolved) winResolved.push(f);
    }
    const linuxLeaked = [];
    for (const f of linuxFamilies) {
      if (measureFam(f).resolved) linuxLeaked.push(f);
    }

    // Differential test: distinct Windows families must NOT all measure to the
    // same width (which would expose a naive alias-to-one-font spoof).
    const diffPairs = [
      ['Segoe UI', 'Consolas'],
      ['Calibri', 'Cambria'],
      ['Arial', 'Courier New'],
      ['Georgia', 'Verdana'],
      ['Impact', 'Times New Roman'],
    ];
    const identicalPairs = [];
    for (const [a, b] of diffPairs) {
      if (winWidths[a] === winWidths[b]) {
        identicalPairs.push(a + '==' + b);
      }
    }

    return {
      windowsResolvedCount: winResolved.length,
      windowsResolved: winResolved,
      linuxLeakedCount: linuxLeaked.length,
      linuxLeaked,
      differentialIdenticalPairs: identicalPairs,
      widths: winWidths,
    };
  }

  async function runProbes(label) {
    const probes = [];
    const add = (id, category, passed, weight, observed, expected, note) => {
      probes.push({ id, category, passed: !!passed, weight, observed, expected, note });
    };

    // 1. Classic Surface
    const ua = navigator.userAgent || '';
    add('ua_no_headless', 'surface', !/Headless/i.test(ua), 15, ua, 'No HeadlessChrome token');
    add('ua_windows_nt', 'surface', ua.includes('Windows NT 10.0; Win64; x64'), 10, ua, 'Windows NT 10.0; Win64; x64');
    add('platform_win32', 'surface', navigator.platform === 'Win32', 10, navigator.platform, 'Win32');
    add('webdriver_false', 'surface', navigator.webdriver === false, 15, navigator.webdriver, 'false');
    add('outer_dimensions_nonzero', 'surface', window.outerWidth > 0 && window.outerHeight > 0 && window.outerHeight > window.innerHeight, 10,
      [window.innerWidth, window.innerHeight, window.outerWidth, window.outerHeight], 'outerWidth > 0 and outerHeight > innerHeight');
    add('screen_desktop_geometry', 'surface', screen.width >= 1280 && screen.height >= 720 && screen.availHeight <= screen.height, 5,
      [screen.width, screen.height, screen.availWidth, screen.availHeight], '>= 1280x720 with taskbar');
    add('plugins_populated', 'surface', navigator.plugins && navigator.plugins.length >= 5 && navigator.pdfViewerEnabled === true, 8,
      { plugins: navigator.plugins ? navigator.plugins.length : 0, pdf: navigator.pdfViewerEnabled }, '5 PDF viewer plugins');
    add('window_chrome_complete', 'surface', !!(window.chrome && window.chrome.app && typeof window.chrome.loadTimes === 'function' && typeof window.chrome.csi === 'function'), 8,
      !!window.chrome, 'window.chrome with app/loadTimes/csi');
    add('document_has_focus', 'surface', document.hasFocus() === true, 5, document.hasFocus(), 'true');

    // 2. High-Entropy UA-CH
    let uach = null;
    if (navigator.userAgentData && typeof navigator.userAgentData.getHighEntropyValues === 'function') {
      try {
        uach = await navigator.userAgentData.getHighEntropyValues([
          'architecture', 'bitness', 'model', 'platform', 'platformVersion',
          'uaFullVersion', 'fullVersionList', 'wow64', 'formFactors',
        ]);
      } catch (e) {}
    }
    const uachPlatformOK = uach && uach.platform === 'Windows' && parseInt((uach.platformVersion || '0').split('.')[0], 10) >= 10;
    add('uach_platform_windows', 'uach', uachPlatformOK, 10, uach ? { platform: uach.platform, platformVersion: uach.platformVersion } : null, 'Windows platformVersion >= 10.0.0');
    const fullBrandsOK = uach && Array.isArray(uach.fullVersionList) && uach.fullVersionList.some((b) => b.brand === 'Google Chrome' && b.version.split('.').length === 4);
    add('uach_full_version_four_part', 'uach', fullBrandsOK, 8, uach ? uach.fullVersionList : null, 'Four-part version in fullVersionList');

    // 3. WebGL 60+ Parameter Sweep
    const gl = probeWebGL();
    const unmaskedR = String((gl.params && gl.params.UNMASKED_RENDERER_WEBGL) || '');
    const unmaskedV = String((gl.params && gl.params.UNMASKED_VENDOR_WEBGL) || '');
    const noSoftGL = !/SwiftShader|llvmpipe|softpipe|Mesa|X11|Gallium/i.test(unmaskedR + ' ' + unmaskedV);
    add('webgl_sweep_60_params', 'webgl', gl.count >= 60, 5, gl.count, '>= 60 WebGL1/WebGL2 parameters & precision formats');
    add('webgl_no_software_or_linux_leak', 'webgl', noSoftGL && unmaskedR.includes('ANGLE (') && unmaskedR.includes('Direct3D11'), 15,
      { vendor: unmaskedV, renderer: unmaskedR }, 'ANGLE (... Direct3D11 ...) without SwiftShader/Mesa/llvmpipe');
    const gl2Ver = String((gl.params && gl.params.GL2_VERSION) || '');
    add('webgl2_version_coherent', 'webgl', gl2Ver.startsWith('WebGL 2.0'), 6, gl2Ver, 'WebGL 2.0 (OpenGL ES 3.0 Chromium)');

    // 4. Font Metrics + Differential Check
    const fonts = probeFonts();
    add('fonts_windows_pack_present', 'fonts', fonts.windowsResolvedCount >= 12, 10,
      { count: fonts.windowsResolvedCount, resolved: fonts.windowsResolved }, '>= 12 Windows families resolved via fontconfig');
    add('fonts_no_linux_leak', 'fonts', fonts.linuxLeakedCount === 0, 10,
      fonts.linuxLeaked, '0 Linux host families (DejaVu/Ubuntu/Cantarell) visible');
    add('fonts_differential_distinct', 'fonts', fonts.differentialIdenticalPairs.length === 0, 10,
      fonts.differentialIdenticalPairs, 'Distinct Windows font pairs measure to distinct glyph widths');

    // 5. Audio Context + Sample Buffer Hash
    const audio = await probeAudio();
    add('audio_sample_rate_48k', 'audio', audio.offlineSampleRate === 48000 && audio.liveSampleRate === 48000, 8,
      { offline: audio.offlineSampleRate, live: audio.liveSampleRate }, '48000 Hz Windows desktop mix rate');
    add('audio_buffer_rendered', 'audio', audio.energy > 0 && audio.hash.length === 16, 4,
      { hash: audio.hash, energy: audio.energy }, 'Non-zero rendered OfflineAudioContext sample buffer');

    // 6. Canvas 2D Pixel Hash
    const canvas = probeCanvas();
    add('canvas_2d_rendered', 'canvas', canvas.nonZeroPixels > 1000 && canvas.hash.length === 16, 4,
      { hash: canvas.hash, nonZeroPixels: canvas.nonZeroPixels }, 'Deterministic 2D canvas pixel hash');

    // 7. Permissions, Media, Speech, MatchMedia, Intl
    const notifPerm = typeof Notification !== 'undefined' ? Notification.permission : 'missing';
    add('notification_permission_prompt', 'permissions', notifPerm === 'prompt', 8, notifPerm, 'prompt (not headless denied)');
    const ptrFine = window.matchMedia('(pointer: fine)').matches && window.matchMedia('(hover: hover)').matches && !window.matchMedia('(pointer: none)').matches;
    add('matchmedia_fine_pointer', 'permissions', ptrFine, 8, ptrFine, '(pointer: fine) && (hover: hover)');
    const voices = window.speechSynthesis ? window.speechSynthesis.getVoices() : [];
    const hasWinVoice = voices.some((v) => /Microsoft/i.test(v.name));
    add('speech_voices_windows', 'permissions', hasWinVoice, 5, voices.map((v) => v.name), 'Microsoft SAPI voices present');
    let devCount = 0;
    if (navigator.mediaDevices && navigator.mediaDevices.enumerateDevices) {
      try { devCount = (await navigator.mediaDevices.enumerateDevices()).length; } catch (e) {}
    }
    add('media_devices_populated', 'permissions', devCount >= 2, 4, devCount, 'Desktop audio/video devices enumerated');
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || '';
    add('intl_timezone_non_utc', 'permissions', tz !== 'UTC' && !tz.startsWith('Etc/'), 5, tz, 'Regional desktop timezone');

    // 8. Prototype & Function.prototype.toString Integrity
    const platGetter = Object.getOwnPropertyDescriptor(Navigator.prototype, 'platform').get;
    const wdGetter = Object.getOwnPropertyDescriptor(Navigator.prototype, 'webdriver').get;
    const glGetParam = WebGLRenderingContext.prototype.getParameter;
    const toStrOK = Function.prototype.toString.call(platGetter).includes('[native code]') &&
                    Function.prototype.toString.call(wdGetter).includes('[native code]') &&
                    Function.prototype.toString.call(glGetParam).includes('[native code]');
    add('function_tostring_native', 'integrity', toStrOK, 10,
      Function.prototype.toString.call(platGetter), 'function get platform() { [native code] }');
    const autoGlobals = Object.getOwnPropertyNames(window).filter((k) => /^cdc_|^\$cdc_|__webdriver|__playwright|domAutomation|_phantom/i.test(k));
    add('no_automation_globals', 'integrity', autoGlobals.length === 0, 10, autoGlobals, 'No cdc_/webdriver/playwright globals');

    // 9. Behavioral Telemetry (evaluated when actions have been performed on the page)
    if (tel.clicks.length > 0 || tel.keyDowns.length > 0) {
      const trustedAll = tel.untrustedCount === 0;
      const moveCountOK = tel.mouseMoves.length >= 4;
      const lastDown = tel.mouseDowns[tel.mouseDowns.length - 1] || {};
      const lastUp = tel.mouseUps[tel.mouseUps.length - 1] || {};
      const dwellMs = (lastUp.t || 0) - (lastDown.t || 0);
      const nonCenter = typeof lastDown.relX === 'number' ? (Math.abs(lastDown.relX - 0.5) > 0.0001 || Math.abs(lastDown.relY - 0.5) > 0.0001) : true;
      const chromeOffsetOK = (lastDown.screenY || 0) > (lastDown.clientY || 0);
      const keysAuthentic = tel.keyDowns.length > 0 && tel.keyDowns.length === tel.keyUps.length &&
        tel.keyDowns.every((k) => k.code && k.keyCode > 0 && k.isTrusted);
      add('behavior_human_pointer_and_keys', 'behavior',
        trustedAll && moveCountOK && dwellMs >= 10 && nonCenter && chromeOffsetOK && keysAuthentic,
        10,
        {
          mouseMoves: tel.mouseMoves.length,
          dwellMs: Math.round(dwellMs),
          relClick: [lastDown.relX, lastDown.relY],
          screenMinusClientY: (lastDown.screenY || 0) - (lastDown.clientY || 0),
          keyDowns: tel.keyDowns.length,
          keyUps: tel.keyUps.length,
          untrusted: tel.untrustedCount,
        },
        'Curved mouse trajectory, >10ms press dwell, non-midpoint target, chrome screenY offset, per-char trusted keyDown/keyUp');
    }

    let suspicion = 0;
    let maxScore = 0;
    let passedCount = 0;
    let failedCount = 0;
    const failedProbes = [];
    for (const p of probes) {
      maxScore += p.weight;
      if (p.passed) {
        passedCount++;
      } else {
        failedCount++;
        suspicion += p.weight;
        failedProbes.push(p.id);
      }
    }
    const normScore = maxScore > 0 ? Math.round((suspicion / maxScore) * 100) : 0;
    const classification = normScore === 0 ? 'HUMAN_DESKTOP_WIN11' : (normScore < 25 ? 'SUSPICIOUS' : 'HEADLESS_LINUX_BOT');

    const report = {
      label: label || 'auto',
      suspicionScore: normScore,
      maxScore: 100,
      classification,
      passedCount,
      failedCount,
      probes,
      failedProbes,
      canvasHash: canvas.hash,
      audioHash: audio.hash,
      webglParamCount: gl.count,
      fontSummary: {
        windowsResolvedCount: fonts.windowsResolvedCount,
        linuxLeakedCount: fonts.linuxLeakedCount,
        differentialIdenticalPairs: fonts.differentialIdenticalPairs,
      },
      behavior: {
        mouseMoves: tel.mouseMoves.length,
        mouseDowns: tel.mouseDowns.length,
        mouseUps: tel.mouseUps.length,
        clicks: tel.clicks.length,
        keyDowns: tel.keyDowns.length,
        keyUps: tel.keyUps.length,
        inputs: tel.inputs.length,
        untrustedCount: tel.untrustedCount,
        eventOrder: tel.eventOrder.slice(0, 32),
      },
      honestLimits: [
        'Canvas 2D pixel hashes reflect the real Chromium Skia/SwiftShader rasterizer on the host.',
        'OfflineAudioContext float sample hashes reflect the real Chromium Blink DSP pipeline.',
        'TLS ClientHello (JA3/JA4/ALPN) and TCP/IP stack parameters belong to the OS/network layer and are not spoofable from JS.',
      ],
    };

    renderUI(report);
    try {
      await fetch('/api/report', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(report),
      });
    } catch (e) {}
    window.__lastDetectReport = report;
    return report;
  }

  function renderUI(rep) {
    const clsEl = document.getElementById('classification');
    clsEl.textContent = rep.classification;
    clsEl.className = 'badge ' + (rep.suspicionScore === 0 ? 'pass' : 'fail');
    document.getElementById('score').textContent = rep.suspicionScore + ' / 100';
    document.getElementById('passed').textContent = String(rep.passedCount);
    document.getElementById('failed').textContent = String(rep.failedCount);
    document.getElementById('webgl-count').textContent = String(rep.webglParamCount);

    const tbody = document.getElementById('probe-rows');
    tbody.innerHTML = '';
    for (const p of rep.probes) {
      const tr = document.createElement('tr');
      const st = document.createElement('td');
      st.innerHTML = '<span class="badge ' + (p.passed ? 'pass' : 'fail') + '">' + (p.passed ? 'PASS' : 'FAIL') + '</span>';
      const cat = document.createElement('td');
      cat.textContent = p.category;
      const id = document.createElement('td');
      id.innerHTML = '<code>' + p.id + '</code>';
      const obs = document.createElement('td');
      obs.innerHTML = '<code>' + JSON.stringify(p.observed) + '</code>';
      const exp = document.createElement('td');
      exp.textContent = p.expected || '';
      tr.append(st, cat, id, obs, exp);
      tbody.appendChild(tr);
    }
  }

  window.__runDetectProbes = runProbes;
  window.addEventListener('load', () => { runProbes('initial'); });
})();
</script>
</body>
</html>
`
