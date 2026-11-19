/* XT-Nichrome preload patch.
 *
 * Runs at document_start in EVERY frame of every context belonging to one
 * session, before any page script. Goal: make the JS surface agree with the
 * persona AND with the real Chromium network/TLS/JS behaviour underneath.
 *
 * Rules this file obeys:
 *  1. Never delete or re-add an API. Only redefine existing properties, or add
 *     properties that the real Chrome on the persona's OS really has.
 *  2. Every patched function keeps `[native code]` in toString and keeps its
 *     original `length` and `name`, otherwise Function.prototype.toString
 *     becomes an instant fingerprint.
 *  3. No global marker, no closure name that pages can find by enumeration.
 *  4. Values must be internally consistent: if UA says Windows the platform,
 *     UA-CH platformVersion, fonts, WebGL renderer, audio rate, connection type
 *     and permission defaults all have to follow.
 *  5. Idempotent. Installing twice must not stack getters.
 */
(() => {
  'use strict';
  const P = __PERSONA__;
  if (window.__xtn_installed__) return;
  try {
    Object.defineProperty(window, '__xtn_installed__', { value: true, enumerable: false, configurable: false, writable: false });
  } catch (e) { return; }

  const nativeToString = Function.prototype.toString;
  const patched = new WeakMap();
  const toStringProxy = new Proxy(nativeToString, {
    apply(target, thisArg, args) {
      if (patched.has(thisArg)) return patched.get(thisArg);
      return Reflect.apply(target, thisArg, args);
    },
  });

  // Native getters report "function get prop() { [native code] }" through
  // Function.prototype.toString. A wrapper arrow leaks its own source, which is
  // a louder tell than the value we were fixing, so every getter is masked.
  function ownDesc(obj, prop) {
    for (let o = obj, d = 0; o && d < 8; o = Object.getPrototypeOf(o), d++) {
      const desc = Object.getOwnPropertyDescriptor(o, prop);
      if (desc) return desc;
    }
    return null;
  }

  function defineGetter(proto, prop, getter, opts) {
    const existing = ownDesc(proto, prop);
    if (existing && existing.get && patched.has(existing.get)) return; // already ours
    const enumerable = opts && 'enumerable' in opts ? opts.enumerable
      : (existing ? !!existing.enumerable : true);
    const masked = mask(getter, 'get ' + prop, 0);
    try {
      Object.defineProperty(proto, prop, { get: masked, configurable: true, enumerable });
    } catch (e) { /* non-configurable: leave the real value */ }
  }

  function defineValue(obj, prop, value) {
    try {
      Object.defineProperty(obj, prop, { value, writable: true, configurable: true, enumerable: true });
    } catch (e) { /* ignore */ }
  }

  // Marks a function as native-looking and preserves name/length.
  function mask(fn, name, length) {
    const src = 'function ' + (name || fn.name || '') + '() { [native code] }';
    patched.set(fn, src);
    try {
      Object.defineProperty(fn, 'name', { value: name || fn.name, configurable: true });
    } catch (e) {}
    try {
      Object.defineProperty(fn, 'length', { value: length === undefined ? fn.length : length, configurable: true });
    } catch (e) {}
    return fn;
  }

  // Each section below is independently guarded. A section that throws must not
  // prevent later sections from installing: a half-applied persona (patched UA,
  // unpatched geometry) is more detectable than either state alone.
  const failures = [];
  function guard(label, fn) {
    try { fn(); } catch (e) { failures.push(label + ': ' + (e && e.message)); }
  }

  // ---------------------------------------------------------------- navigator
  guard('navigator-core', () => {
  const N = Navigator.prototype;
  defineGetter(N, 'platform', () => P.platform);
  defineGetter(N, 'vendor', () => 'Google Inc.');
  defineGetter(N, 'vendorSub', () => '');
  defineGetter(N, 'productSub', () => '20030107');
  defineGetter(N, 'product', () => 'Gecko');
  defineGetter(N, 'appVersion', () => P.appVersion);
  defineGetter(N, 'appName', () => 'Netscape');
  defineGetter(N, 'appCodeName', () => 'Mozilla');
  defineGetter(N, 'userAgent', () => P.userAgent);
  defineGetter(N, 'language', () => P.languages[0]);
  defineGetter(N, 'languages', () => Object.freeze(P.languages.slice()));
  defineGetter(N, 'hardwareConcurrency', () => P.hardwareConcurrency);
  defineGetter(N, 'deviceMemory', () => P.deviceMemory);
  defineGetter(N, 'maxTouchPoints', () => P.maxTouchPoints);
  defineGetter(N, 'webdriver', () => false);
  defineGetter(N, 'doNotTrack', () => null);
  defineGetter(N, 'pdfViewerEnabled', () => true);
  defineGetter(N, 'onLine', () => true);

  });

  // navigator.plugins / mimeTypes: real Chrome-on-Windows PDF viewer set.
  // A zero-plugin array is a headless tell; five PDF entries is what a stock
  // desktop Chrome reports.
  guard('navigator-plugins', () => {
  if (navigator.plugins.length === 0 || !navigator.plugins.length) {
    const pluginData = [
      ['PDF Viewer', 'internal-pdf-viewer', 'application/pdf', 'pdf', 'Portable Document Format'],
      ['Chrome PDF Viewer', 'internal-pdf-viewer', 'application/pdf', 'pdf', 'Portable Document Format'],
      ['Chromium PDF Viewer', 'internal-pdf-viewer', 'application/pdf', 'pdf', 'Portable Document Format'],
      ['Microsoft Edge PDF Viewer', 'internal-pdf-viewer', 'application/pdf', 'pdf', 'Portable Document Format'],
      ['WebKit built-in PDF', 'internal-pdf-viewer', 'application/pdf', 'pdf', 'Portable Document Format'],
    ];
    const mimes = [];
    const plugins = [];
    for (const [pname, filename, mtype, suffixes, desc] of pluginData) {
      const mime = Object.create(MimeType.prototype);
      defineValue(mime, 'type', mtype);
      defineValue(mime, 'suffixes', suffixes);
      defineValue(mime, 'description', desc);
      defineValue(mime, 'enabledPlugin', null);
      mimes.push(mime);
      const plug = Object.create(Plugin.prototype);
      defineValue(plug, 'name', pname);
      defineValue(plug, 'filename', filename);
      defineValue(plug, 'description', desc);
      defineValue(plug, 'length', 1);
      defineValue(plug, 0, mime);
      defineValue(mime, 'enabledPlugin', plug);
      plugins.push(plug);
    }
    const pluginArray = Object.create(PluginArray.prototype);
    plugins.forEach((p, i) => defineValue(pluginArray, i, p));
    defineValue(pluginArray, 'length', plugins.length);
    ['item', 'namedItem', 'refresh'].forEach((m) => {
      defineValue(pluginArray, m, mask(function () {}, m, 1));
    });
    const mimeArray = Object.create(MimeTypeArray.prototype);
    mimes.forEach((m, i) => defineValue(mimeArray, i, m));
    defineValue(mimeArray, 'length', mimes.length);
    ['item', 'namedItem'].forEach((m) => defineValue(mimeArray, m, mask(function () {}, m, 1)));
    defineGetter(N, 'plugins', () => pluginArray);
    defineGetter(N, 'mimeTypes', () => mimeArray);
  }

  });

  // navigator.connection: Windows desktop chrome reports 4g/10Mbps-ish; a
  // missing connection object or "slow-2g" is a datacenter tell.
  guard('navigator-connection', () => {
  if (navigator.connection) {
    const C = NetworkInformation.prototype;
    defineGetter(C, 'effectiveType', () => P.connectionType);
    defineGetter(C, 'rtt', () => P.connectionRtt);
    defineGetter(C, 'downlink', () => P.connectionDownlink);
    defineGetter(C, 'saveData', () => false);
    defineGetter(C, 'type', () => 'ethernet');
  }

  });

  // navigator.userAgentData: present only in secure contexts in real Chrome,
  // so only patch when it already exists (otherwise we would create a tell).
  guard('navigator-ua-ch', () => {
  if (navigator.userAgentData) {
    const U = Object.getPrototypeOf(navigator.userAgentData);
    const highEntropy = {
      architecture: P.architecture,
      bitness: P.bitness,
      model: '',
      platformVersion: P.osVersion,
      uaFullVersion: P.uaFullVersion,
      fullVersionList: P.fullBrands,
      wow64: false,
      formFactors: ['Desktop'],
      platform: P.osName,
      mobile: false,
      brands: P.brands,
    };
    const getHigh = mask(async function getHighEntropyValues(hints) {
      const out = {
        brands: P.brands.map((b) => ({ brand: b.brand, version: b.version })),
        mobile: false,
        platform: P.osName,
      };
      for (const h of (hints || [])) {
        if (h in highEntropy) {
          const v = highEntropy[h];
          out[h] = Array.isArray(v) ? v.map((x) => (typeof x === 'object' ? Object.assign({}, x) : x)) : v;
        }
      }
      return out;
    }, 'getHighEntropyValues', 1);
    defineGetter(U, 'brands', () => P.brands.map((b) => ({ brand: b.brand, version: b.version })));
    defineGetter(U, 'mobile', () => false);
    defineGetter(U, 'platform', () => P.osName);
    defineValue(U, 'getHighEntropyValues', getHigh);
    defineValue(U, 'toJSON', mask(function toJSON() {
      return { brands: P.brands, mobile: false, platform: P.osName };
    }, 'toJSON', 0));
  } else if (window.isSecureContext) {
    // Secure context but no userAgentData: Chrome would have it. Build it.
    const brands = P.brands.map((b) => ({ brand: b.brand, version: b.version }));
    const uad = {
      brands,
      mobile: false,
      platform: P.osName,
      getHighEntropyValues: mask(async function getHighEntropyValues(hints) {
        const all = {
          architecture: P.architecture, bitness: P.bitness, model: '',
          platformVersion: P.osVersion, uaFullVersion: P.uaFullVersion,
          fullVersionList: P.fullBrands, wow64: false, formFactors: ['Desktop'],
        };
        const out = { brands: brands.slice(), mobile: false, platform: P.osName };
        for (const h of (hints || [])) if (h in all) out[h] = all[h];
        return out;
      }, 'getHighEntropyValues', 1),
      toJSON: mask(function toJSON() { return { brands, mobile: false, platform: P.osName }; }, 'toJSON', 0),
    };
    defineGetter(N, 'userAgentData', () => uad);
  }

  });

  // ------------------------------------------------------------------- screen
  guard('screen', () => {
  const S = Screen.prototype;
  defineGetter(S, 'width', () => P.screenWidth);
  defineGetter(S, 'height', () => P.screenHeight);
  defineGetter(S, 'availWidth', () => P.availWidth);
  defineGetter(S, 'availHeight', () => P.availHeight);
  defineGetter(S, 'colorDepth', () => P.colorDepth);
  defineGetter(S, 'pixelDepth', () => P.colorDepth);
  defineGetter(S, 'availLeft', () => 0);
  defineGetter(S, 'availTop', () => 0);
  const orientationObj = (() => {
    // Screen.prototype.orientation is an accessor: reading it on the prototype
    // throws Illegal invocation. The instance is the only legal receiver.
    let real = null;
    try { real = screen.orientation; } catch (e) { real = null; }
    if (real) {
      // Keep the real object identity and only fix the reported type/angle, so
      // screen.orientation === screen.orientation still holds as on Chrome.
      try {
        Object.defineProperty(Object.getPrototypeOf(real), 'type', {
          get: mask(function () { return 'landscape-primary'; }, 'get type', 0),
          configurable: true, enumerable: true,
        });
        Object.defineProperty(Object.getPrototypeOf(real), 'angle', {
          get: mask(function () { return 0; }, 'get angle', 0),
          configurable: true, enumerable: true,
        });
      } catch (e) {}
      return real;
    }
    const o = { type: 'landscape-primary', angle: 0, onchange: null,
      lock: mask(function lock() { return Promise.resolve(); }, 'lock', 0),
      unlock: mask(function unlock() {}, 'unlock', 0),
      addEventListener() {}, removeEventListener() {}, dispatchEvent() { return false; } };
    return o;
  })();
  defineGetter(S, 'orientation', () => orientationObj);

  });

  // Window geometry. A real Windows Chrome window at 1920x1080 with the bookmark
  // bar reports outerHeight ~1040 and innerHeight ~= outerHeight - 87..90.
  // Headless reports 0x0 outer — one of the loudest tells.
  guard('window-geometry', () => {
  const outerW = P.screenWidth;
  const outerH = P.availHeight + 8;
  defineGetter(window, 'outerWidth', () => outerW);
  defineGetter(window, 'outerHeight', () => outerH);
  defineGetter(window, 'screenX', () => 0);
  defineGetter(window, 'screenY', () => 0);
  defineGetter(window, 'screenLeft', () => 0);
  defineGetter(window, 'screenTop', () => 0);
  defineGetter(window, 'devicePixelRatio', () => P.deviceScaleFactor);
  defineGetter(window, 'clientWidth', () => window.innerWidth);
  defineGetter(window, 'clientHeight', () => window.innerHeight);

  });

  // matchMedia: headless reports pointer: none / hover: none because there is no
  // OS cursor. A desktop Windows session is fine+fine.
  guard('matchmedia', () => {
  const mm = window.matchMedia.bind(window);
  const MM_OVERRIDES = {
    '(pointer: fine)': true,
    '(pointer: coarse)': false,
    '(pointer: none)': false,
    '(any-pointer: fine)': true,
    '(any-pointer: coarse)': false,
    '(hover: hover)': true,
    '(hover: none)': false,
    '(any-hover: hover)': true,
    '(prefers-color-scheme: light)': true,
    '(prefers-color-scheme: dark)': false,
    '(prefers-reduced-motion: reduce)': false,
    '(prefers-reduced-transparency: reduce)': false,
    '(prefers-contrast: more)': false,
    '(forced-colors: active)': false,
    '(inverted-colors: inverted)': false,
    '(color-gamut: srgb)': true,
    '(color-gamut: p3)': false,
    '(color-gamut: rec2020)': false,
    '(dynamic-range: standard)': true,
    '(dynamic-range: high)': false,
    '(display-mode: browser)': true,
    '(any-hover: none)': false,
  };
  const mqlCache = new Map();
  defineValue(window, 'matchMedia', mask(function matchMedia(q) {
    const key = String(q).trim().toLowerCase();
    if (mqlCache.has(key)) return mqlCache.get(key);
    const res = mm(q);
    if (key in MM_OVERRIDES) {
      try {
        Object.defineProperty(res, 'matches', {
          get: mask(function () { return MM_OVERRIDES[key]; }, 'get matches', 0),
          configurable: true, enumerable: true,
        });
      } catch (e) {}
    }
    mqlCache.set(key, res);
    return res;
  }, 'matchMedia', 1));

  });

  // ------------------------------------------------------------------- webgl
  guard('webgl', () => {
  const GL_PARAMS = {
    0x1F00: P.glVendor,          // VENDOR
    0x1F01: P.glRenderer,        // RENDERER
    0x1F02: P.glVersion,         // VERSION
    0x8B8C: P.glslVersion,       // SHADING_LANGUAGE_VERSION
    0x0D33: P.maxTextureSize,    // MAX_TEXTURE_SIZE
    0x84E8: P.maxRenderbufferSize,
    0x851C: P.maxCubeMapSize,
    0x0D3A: P.maxViewportDim,    // MAX_VIEWPORT_DIMS (needs Int32Array)
    0x846D: P.aliasedPointMax,   // ALIASED_POINT_SIZE_RANGE (Float32Array)
    0x846E: P.aliasedLineMax,    // ALIASED_LINE_WIDTH_RANGE
    // ANGLE D3D11 vs SwiftShader parameter alignment across WebGL1 & WebGL2.
    0x8869: 16,                  // MAX_VERTEX_ATTRIBS
    0x8DFB: 4096,                // MAX_VERTEX_UNIFORM_VECTORS
    0x8DFC: 30,                  // MAX_VARYING_VECTORS
    0x8B4D: 32,                  // MAX_COMBINED_TEXTURE_IMAGE_UNITS
    0x8B4C: 16,                  // MAX_VERTEX_TEXTURE_IMAGE_UNITS
    0x8872: 16,                  // MAX_TEXTURE_IMAGE_UNITS
    0x8DFD: 1024,                // MAX_FRAGMENT_UNIFORM_VECTORS
    0x84FF: 16,                  // MAX_TEXTURE_MAX_ANISOTROPY_EXT
    0x8073: 2048,                // MAX_3D_TEXTURE_SIZE
    0x88FF: 2048,                // MAX_ARRAY_TEXTURE_LAYERS
    0x8CDF: 8,                   // MAX_COLOR_ATTACHMENTS
    0x8824: 8,                   // MAX_DRAW_BUFFERS
    0x8D57: 8,                   // MAX_SAMPLES
    0x8A2F: 72,                  // MAX_UNIFORM_BUFFER_BINDINGS
    0x8A30: 65536,               // MAX_UNIFORM_BLOCK_SIZE
    0x8A2E: 24,                  // MAX_COMBINED_UNIFORM_BLOCKS
    0x8B4B: 120,                 // MAX_VARYING_COMPONENTS
    0x8B4A: 16384,               // MAX_VERTEX_UNIFORM_COMPONENTS
    0x8B49: 4096,                // MAX_FRAGMENT_UNIFORM_COMPONENTS
  };
  const dbgVendor = 0x9245;   // UNMASKED_VENDOR_WEBGL
  const dbgRenderer = 0x9246; // UNMASKED_RENDERER_WEBGL

  function patchGL(proto, isGL2) {
    if (!proto) return;
    const orig = proto.getParameter;
    if (!orig || patched.has(orig)) return;
    const patchedGet = mask(function getParameter(p) {
      if (p === dbgVendor) return P.glUnmaskedVendor;
      if (p === dbgRenderer) return P.glUnmaskedRenderer;
      if (isGL2 && p === 0x1F02) return 'WebGL 2.0 (OpenGL ES 3.0 Chromium)';
      if (isGL2 && p === 0x8B8C) return 'WebGL GLSL ES 3.00 (OpenGL ES GLSL ES 3.0 Chromium)';
      if (p === 0x0D3A) { const a = new Int32Array([P.maxViewportDim, P.maxViewportDim]); return a; }
      if (p === 0x846D) { const a = new Float32Array([1, P.aliasedPointMax]); return a; }
      if (p === 0x846E) { const a = new Float32Array([1, P.aliasedLineMax]); return a; }
      if (p in GL_PARAMS) return GL_PARAMS[p];
      return Reflect.apply(orig, this, arguments);
    }, 'getParameter', 1);
    defineValue(proto, 'getParameter', patchedGet);

    const origExt = proto.getSupportedExtensions;
    if (origExt) {
      defineValue(proto, 'getSupportedExtensions', mask(function getSupportedExtensions() {
        return P.glExtensions.slice();
      }, 'getSupportedExtensions', 0));
    }
    // getExtension must still hand out real objects; only the debug_renderer_info
    // path is intercepted by getParameter above, so pass through untouched.
  }
  patchGL(window.WebGLRenderingContext && WebGLRenderingContext.prototype, false);
  patchGL(window.WebGL2RenderingContext && WebGL2RenderingContext.prototype, true);

  });

  // ---------------------------------------------------------------- audio
  // Sample rate and channel count differ between datacenter containers (44100/1)
  // and a real Windows desktop (48000/2).
  guard('audio', () => {
  if (window.AudioContext || window.webkitAudioContext) {
    const AC = (window.AudioContext || window.webkitAudioContext).prototype;
    const BaseAC = Object.getPrototypeOf(AC);
    const sampleRateDesc = Object.getOwnPropertyDescriptor(BaseAC, 'sampleRate');
    if (sampleRateDesc && sampleRateDesc.get) {
      defineGetter(BaseAC, 'sampleRate', () => P.audioSampleRate);
    }
  }
  if (window.OfflineAudioContext) {
    const OAC = OfflineAudioContext.prototype;
    const origCtor = OfflineAudioContext;
    const proxyCtor = new Proxy(origCtor, {
      construct(t, args, nt) {
        // Force the persona's sample rate regardless of what the page asked for.
        const [ch, len] = args;
        return Reflect.construct(t, [ch, len, P.audioSampleRate], nt);
      },
    });
    mask(proxyCtor, 'OfflineAudioContext', 3);
    try {
      Object.defineProperty(proxyCtor, 'prototype', { value: origCtor.prototype, configurable: false, writable: false });
    } catch (e) {}
    defineValue(window, 'OfflineAudioContext', proxyCtor);
    if (window.webkitOfflineAudioContext) defineValue(window, 'webkitOfflineAudioContext', proxyCtor);
  }
  if (navigator.mediaDevices) {
    const MD = Object.getPrototypeOf(navigator.mediaDevices);
    const origEnum = navigator.mediaDevices.enumerateDevices;
    defineValue(MD, 'enumerateDevices', mask(async function enumerateDevices() {
      const list = [];
      const mk = (kind, label, id, group) => ({
        deviceId: id, kind, label, groupId: group,
        toJSON() { return { deviceId: id, kind, label, groupId: group }; },
      });
      // Windows desktop exposes a default communications mic + speakers + camera.
      for (let i = 0; i < P.audioInputs; i++) {
        list.push(mk('audioinput', i === 0 ? 'Default - Microphone (Realtek(R) Audio)' : 'Microphone ' + (i + 1) + ' (Realtek(R) Audio)', 'default', 'a1'));
      }
      for (let i = 0; i < P.audioOutputs; i++) {
        list.push(mk('audiooutput', i === 0 ? 'Default - Speakers (Realtek(R) Audio)' : 'Speakers ' + (i + 1), 'default', 'a2'));
      }
      for (let i = 0; i < P.videoInputs; i++) {
        list.push(mk('videoinput', 'Integrated Camera (04f2:b6d9)', 'cam' + i, 'v1'));
      }
      return list;
    }, 'enumerateDevices', 0));
    if (origEnum && !navigator.mediaDevices.ondevicechange) {
      // leave events alone
    }
  }

  });

  // ------------------------------------------------------------- permissions
  // Headless with no display reports "denied" for notifications, which real
  // desktop Chrome reports as "prompt".
  guard('permissions', () => {
  if (navigator.permissions) {
    const Perm = Permissions.prototype;
    const origQuery = Perm.query;
    defineValue(Perm, 'query', mask(function query(desc) {
      const name = desc && desc.name;
      const known = ['geolocation', 'notifications', 'push', 'midi', 'camera', 'microphone', 'background-sync',
        'ambient-light-sensor', 'accelerometer', 'gyroscope', 'magnetometer', 'clipboard-read',
        'clipboard-write', 'payment-handler', 'persistent-storage', 'storage-access', 'window-management',
        'local-fonts', 'screen-wake-lock', 'idle-detection'];
      if (known.indexOf(name) === -1) return origQuery.call(this, desc);
      const state = (name === 'notifications' || name === 'push') ? P.notificationPermission : 'prompt';
      const status = {
        state, name, onchange: null,
        addEventListener() {}, removeEventListener() {}, dispatchEvent() { return false; },
      };
      return Promise.resolve(status);
    }, 'query', 1));
  }

  });

  // Notification.permission is a static getter on the constructor, not a
  // permission query. Headless reports "denied" because no display exists, so a
  // page reading it directly sees a no-display environment.
  guard('notification-permission', () => {
    if (typeof Notification === 'undefined') return;
    const desc = Object.getOwnPropertyDescriptor(Notification, 'permission');
    if (desc && desc.get && patched.has(desc.get)) return;
    Object.defineProperty(Notification, 'permission', {
      get: mask(function () { return P.notificationPermission; }, 'get permission', 0),
      configurable: true, enumerable: true,
    });
    const reqPerm = Notification.requestPermission;
    if (reqPerm) {
      defineValue(Notification, 'requestPermission', mask(function requestPermission(cb) {
        const p = Promise.resolve(P.notificationPermission);
        if (typeof cb === 'function') { try { cb(P.notificationPermission); } catch (e) {} }
        return p;
      }, 'requestPermission', 1));
    }
  });

  // ------------------------------------------------------------ speech synth
  guard('speech', () => {
  if (window.speechSynthesis) {
    const SS = Object.getPrototypeOf(window.speechSynthesis);
    defineValue(SS, 'getVoices', mask(function getVoices() {
      return P.speechVoices.map((v, i) => ({
        voiceURI: v.name, name: v.name, lang: v.lang,
        localService: v.localService, default: !!v.default || i === 0,
        toJSON() { return this; },
      }));
    }, 'getVoices', 0));
  }

  });

  // ------------------------------------------------------------------- chrome
  // window.chrome exists on real Chrome; a page can read it in one line. Build
  // the object graph a Windows Chrome exposes, with native-looking methods.
  guard('chrome-object', () => {
  if (P.chromeObject) {
    // window.chrome is non-configurable and enumerable on a real Chrome. Adding
    // keys to the live object keeps those flags; re-defining it does not.
    let chrome = window.chrome;
    if (!chrome || typeof chrome !== 'object') {
      try {
        Object.defineProperty(window, 'chrome', { value: {}, writable: true, enumerable: true, configurable: false });
      } catch (e) { chrome = {}; }
      chrome = window.chrome || {};
    }
    const ensure = (obj, key, val) => { if (!(key in obj)) { try { obj[key] = val; } catch (e) {} } };

    ensure(chrome, 'app', {
      isInstalled: false,
      getDetails: mask(function getDetails() { return null; }, 'getDetails', 1),
      getIsInstalled: mask(function getIsInstalled() { return false; }, 'getIsInstalled', 0),
      installState: mask(function installState() { return 'not_installed'; }, 'installState', 1),
      runningState: mask(function runningState() { return 'cannot_run'; }, 'runningState', 0),
      InstallState: { DISABLED: 'disabled', INSTALLED: 'installed', NOT_INSTALLED: 'not_installed' },
      RunningState: { CANNOT_RUN: 'cannot_run', READY_TO_RUN: 'ready_to_run', RUNNING: 'running' },
    });

    // Only touch runtime when the browser already provides it (extension context).
    if (chrome.runtime) ensure(chrome, 'runtime', {
      OnInstalledReason: { CHROME_UPDATE: 'chrome_update', INSTALL: 'install', SHARED_MODULE_UPDATE: 'shared_module_update', UPDATE: 'update' },
      OnRestartRequiredReason: { APP_UPDATE: 'app_update', OS_UPDATE: 'os_update', PERIODIC: 'periodic' },
      PlatformArch: { ARM: 'arm', ARM64: 'arm64', MIPS: 'mips', MIPS64: 'mips64', X86_32: 'x86-32', X86_64: 'x86-64' },
      PlatformNaclArch: { ARM: 'arm', MIPS: 'mips', MIPS64: 'mips64', X86_32: 'x86-32', X86_64: 'x86-64' },
      PlatformOs: { ANDROID: 'android', CROS: 'cros', LINUX: 'linux', MAC: 'mac', OPENBSD: 'openbsd', WIN: 'win' },
      RequestUpdateCheckStatus: { NO_UPDATE: 'no_update', THROTTLED: 'throttled', UPDATE_AVAILABLE: 'update_available' },
      connect: mask(function connect() { throw new Error('Could not establish connection. Receiving end does not exist.'); }, 'connect', 1),
      sendMessage: mask(function sendMessage() { throw new Error('Could not establish connection. Receiving end does not exist.'); }, 'sendMessage', 1),
      id: undefined,
    });

    if (!chrome.loadTimes) {
      defineValue(chrome, 'loadTimes', mask(function loadTimes() {
        const t0 = performance.timeOrigin / 1000;
        const nav = performance.getEntriesByType('navigation')[0];
        const done = nav ? (nav.responseEnd / 1000) + t0 : t0 + 0.05;
        return {
          requestTime: t0, startLoadTime: t0, commitLoadTime: t0 + 0.02,
          finishDocumentLoadTime: done, finishLoadTime: done + 0.01,
          firstPaintTime: done, firstPaintAfterLoadTime: 0,
          navigationType: 'Other', wasFetchedViaSpdy: true, wasNpnNegotiated: true,
          npnNegotiatedProtocol: 'h2', wasAlternateProtocolAvailable: false, connectionInfo: 'h2',
        };
      }, 'loadTimes', 0));
    }
    if (!chrome.csi) {
      defineValue(chrome, 'csi', mask(function csi() {
        const t0 = performance.timeOrigin;
        return { startE: Math.round(t0), onloadT: Math.round(t0 + performance.now()), pageT: performance.now(), tran: 15 };
      }, 'csi', 0));
    }
  }

  });

  // ------------------------------------------------------------ font surface
  // Canvas text metrics reveal which families actually exist. We ship Windows
  // metric-compatible faces via fontconfig (see internal/stealth/fonts.go), so
  // real metrics already resolve; only families with no shipped stand-in are
  // aliased here to the shipped metrics so probing agrees with the persona.
  guard('fonts', () => {
  const fontAlias = P.fontAliases || {};
  if (Object.keys(fontAlias).length) {
    const canvas2d = HTMLCanvasElement.prototype.getContext;
    defineValue(HTMLCanvasElement.prototype, 'getContext', mask(function getContext(type, ...rest) {
      const ctx = Reflect.apply(canvas2d, this, [type, ...rest]);
      return ctx;
    }, 'getContext', 1));
    const measure = CanvasRenderingContext2D.prototype.measureText;
    defineValue(CanvasRenderingContext2D.prototype, 'measureText', mask(function measureText(text) {
      const res = Reflect.apply(measure, this, arguments);
      try {
        const m = String(this.font || '');
        const fam = (m.match(/["']?([^"',]+)["']?\s*$/) || [])[1];
        if (fam && fontAlias[fam.trim()]) {
          // Render with the shipped stand-in so widths match the persona.
          const prev = this.font;
          this.font = m.replace(/["']?([^"',]+)["']?\s*$/, '"' + fontAlias[fam.trim()] + '"');
          const res2 = Reflect.apply(measure, this, arguments);
          this.font = prev;
          return res2;
        }
      } catch (e) {}
      return res;
    }, 'measureText', 1));
  }

  });

  // ---------------------------------------------------------- Local Font Access
  guard('local-font-access', () => {
  if (navigator.fonts && navigator.fonts.query) {
    const LF = Object.getPrototypeOf(navigator.fonts);
    defineValue(LF, 'query', mask(async function query() {
      return P.fonts.map((f) => ({
        family: f, fullName: f, postscriptName: f.replace(/\s+/g, ''), style: 'Regular',
        blob() { return Promise.resolve(new Blob([])); },
      }));
    }, 'query', 0));
  }

  });

  // ------------------------------------------------------------- time / locale
  // Intl time zone must match the persona's declared zone, otherwise a single
  // Intl call exposes a datacenter region.
  guard('intl-timezone', () => {
  const origDTF = Intl.DateTimeFormat;
  const tzPatch = (opts) => {
    if (!opts) return opts;
    if (opts.timeZone) return opts; // page picked explicitly; leave it
    return Object.assign({}, opts, { timeZone: P.timeZone });
  };
  const DateTimeFormat = new Proxy(origDTF, {
    construct(t, args, nt) {
      const [locales, options] = args;
      return Reflect.construct(t, [locales, tzPatch(options)], nt);
    },
    apply(t, thisArg, args) {
      return Reflect.apply(t, thisArg, args);
    },
  });
  mask(DateTimeFormat, 'DateTimeFormat', 0);
  defineValue(DateTimeFormat, 'prototype', origDTF.prototype);
  defineValue(DateTimeFormat, 'supportedLocalesOf', origDTF.supportedLocalesOf);
  defineValue(Intl, 'DateTimeFormat', DateTimeFormat);
  // Intl.DateTimeFormat() called without new must still work.
  Object.defineProperty(DateTimeFormat, Symbol.hasInstance, { value: function (x) { return x instanceof origDTF; } });

  const origResolved = origDTF.prototype.resolvedOptions;
  defineValue(origDTF.prototype, 'resolvedOptions', mask(function resolvedOptions() {
    const r = Reflect.apply(origResolved, this, arguments);
    if (!r.timeZone || r.timeZone === 'UTC' || r.timeZone !== P.timeZone) r.timeZone = P.timeZone;
    return r;
  }, 'resolvedOptions', 0));

  // Date#getTimezoneOffset must agree with the patched zone.
  const tzOffsetMin = (() => {
    try {
      const fmt = new origDTF('en-US', { timeZone: P.timeZone, timeZoneName: 'shortOffset' });
      const part = fmt.formatToParts(new Date()).find((p) => p.type === 'timeZoneName');
      const m = /GMT([+-])(\d{1,2})(?::(\d{2}))?/.exec(part ? part.value : '');
      if (!m) return null;
      const sign = m[1] === '-' ? -1 : 1;
      return sign * (parseInt(m[2], 10) * 60 + parseInt(m[3] || '0', 10));
    } catch (e) { return null; }
  })();
  if (tzOffsetMin !== null) {
    defineValue(Date.prototype, 'getTimezoneOffset', mask(function getTimezoneOffset() {
      return -tzOffsetMin;
    }, 'getTimezoneOffset', 0));
  }

  // navigator.language(s) already patched above; locale-sensitive defaults:
  if (Intl.NumberFormat) {
    const NF = Intl.NumberFormat;
    const origNF = NF;
    const NumberFormat = new Proxy(origNF, {
      construct(t, args, nt) {
        const [locales, options] = args;
        if (locales === undefined) return Reflect.construct(t, [P.locale, options], nt);
        return Reflect.construct(t, args, nt);
      },
    });
    mask(NumberFormat, 'NumberFormat', 0);
    defineValue(NumberFormat, 'prototype', origNF.prototype);
    defineValue(NumberFormat, 'supportedLocalesOf', origNF.supportedLocalesOf);
    defineValue(Intl, 'NumberFormat', NumberFormat);
  }

  });

  // ------------------------------------------------------------------- misc
  // A page can detect the automation harness by checking that
  // document.hasFocus() is false while the document claims to be visible, or
  // that MouseEvent.screenY equals clientY (zero browser toolbar height).
  guard('pointer-chrome-offset', () => {
    if (typeof MouseEvent !== 'undefined') {
      const MP = MouseEvent.prototype;
      const sxDesc = Object.getOwnPropertyDescriptor(MP, 'screenX');
      const syDesc = Object.getOwnPropertyDescriptor(MP, 'screenY');
      const cxDesc = Object.getOwnPropertyDescriptor(MP, 'clientX');
      const cyDesc = Object.getOwnPropertyDescriptor(MP, 'clientY');
      if (sxDesc && sxDesc.get && cxDesc && cxDesc.get) {
        defineGetter(MP, 'screenX', function () {
          const sx = sxDesc.get.call(this);
          const cx = cxDesc.get.call(this);
          if (sx === cx) return Math.round(cx + (window.screenX || 0));
          return sx;
        });
      }
      if (syDesc && syDesc.get && cyDesc && cyDesc.get) {
        defineGetter(MP, 'screenY', function () {
          const sy = syDesc.get.call(this);
          const cy = cyDesc.get.call(this);
          const chromeH = Math.max(0, (window.outerHeight || 0) - (window.innerHeight || 0));
          if (sy === cy && chromeH > 0) return Math.round(cy + (window.screenY || 0) + chromeH);
          return sy;
        });
      }
    }
  });

  guard('section', () => {
  defineValue(document, 'hasFocus', mask(function hasFocus() { return true; }, 'hasFocus', 0));

  // window.open returning null while popups are policy-blocked is expected; do
  // not fake it (a fake window object is itself a strong tell).

  // Leave the real Function.prototype.toString in place, but make it report
  // [native code] for our patched functions.
  defineValue(Function.prototype, 'toString', toStringProxy);
  });

  // Diagnostics: a non-enumerable record of what installed. The driver reads
  // this once per tab and refuses to hand out a tab whose persona is partial,
  // because a half-applied identity is more detectable than none.
  Object.defineProperty(window, '__xtn_report__', {
    value: Object.freeze({ installed: true, failures: failures.slice(), persona: P.name }),
    enumerable: false, configurable: false, writable: false,
  });

})();
