# Adversarial Detection & Behavioral Telemetry (`internal/detect`)

XT-Nichrome ships an adversarial detection harness (`internal/detect`), a standalone probe server (`cmd/detect-server`), and an end-to-end comparison test (`tests/detect_test.go`) that runs the exact same probe page against:

1. **Stock Headless Chromium** (`--headless=new` on the pinned Linux Chromium 141.0.7390.37 binary without XT-Nichrome hardening).
2. **XT-Nichrome** (`win11-chrome-141` persona, early `Page.addScriptToEvaluateOnNewDocument` stealth patch, isolated Windows 11 fontconfig jail, and humanized mouse/keystroke input virtualization).

## Running the Standalone Detection Server

```bash
./bin/detect-server -listen 127.0.0.1:7799
```

Endpoints:
- `GET /` — Interactive dark-theme adversarial probe UI (`window.__runDetectProbes(label)` runs automatically on load and again after `#probe-btn` is clicked).
- `POST /api/report` — Receives the JSON report from the browser and enriches it with server-observed HTTP/TLS connection metadata.
- `GET /api/latest` — Returns the most recent browser report (`curl -s http://127.0.0.1:7799/api/latest | jq .`).
- `GET /api/connection` — Returns raw HTTP protocol, TLS/ALPN state, and `Sec-CH-UA*` request headers observed on the wire.
- `GET /healthz` — Liveness probe (`{"status":"ok"}`).

## Measured Comparison (`TestAdversarialDetectionSuite`)

Reproduce with:

```bash
go test ./tests/ -run TestAdversarialDetectionSuite -v -count=1
```

| Metric | Stock Headless Chromium (`--headless=new`) | XT-Nichrome (`win11-chrome-141`) |
| --- | --- | --- |
| **Composite Suspicion Score** | `62 / 100` | `0 / 100` |
| **Classification** | `HEADLESS_LINUX_BOT` | `HUMAN_DESKTOP_WIN11` |
| **Passed Probes** | `13` | `28 / 28` |
| **Failed Probes** | `14` | `0` |
| **WebGL1 + WebGL2 + Precision Params** | `84` (Software SwiftShader/Mesa leaked) | `84` (Coherent ANGLE Direct3D11) |
| **Windows Font Families Resolved** | `0 / 16` | `16 / 16` |
| **Linux Host Fonts Leaked** | `3+` (`DejaVu Sans`, etc.) | `0` |
| **Differential Font Width Collisions** | `5 / 5` (all fallback to same base font) | `0 / 5` (distinct glyph metrics per family) |
| **2D Canvas FNV-1a Pixel Hash** | `89ded92c36cb200c` | `036269c10913e003` (Segoe UI glyph raster) |
| **OfflineAudioContext Sample Hash** | `94b4800178104777` | `94b4800178104777` (48 kHz real Blink DSP) |

## Probe Matrix (28 Probes Across 9 Categories)

### 1. Classic Surface (`surface`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `ua_no_headless` | 15 | FAIL (`HeadlessChrome/141...`) | PASS | No `HeadlessChrome` token in `navigator.userAgent` |
| `ua_windows_nt` | 10 | FAIL (`X11; Linux x86_64`) | PASS | `Windows NT 10.0; Win64; x64` token present |
| `platform_win32` | 10 | FAIL (`Linux x86_64`) | PASS | `navigator.platform === "Win32"` |
| `webdriver_false` | 15 | FAIL (`true`) | PASS | `navigator.webdriver === false` via prototype getter |
| `outer_dimensions_nonzero` | 10 | PASS | PASS | `outerWidth > 0` and `outerHeight > innerHeight` (87 px desktop toolbar) |
| `screen_desktop_geometry` | 5 | FAIL (`800x600`) | PASS | `1920x1080` screen with `1920x1040` taskbar `availHeight` |
| `plugins_populated` | 8 | PASS | PASS | 5 standard Chrome PDF `PluginArray` entries & `pdfViewerEnabled === true` |
| `window_chrome_complete` | 8 | PASS | PASS | `window.chrome.app`, `chrome.csi()`, `chrome.loadTimes()`, `chrome.runtime` |
| `document_has_focus` | 5 | PASS | PASS | `document.hasFocus() === true` and `visibilityState === "visible"` |

### 2. High-Entropy Client Hints (`uach`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `uach_platform_windows` | 10 | FAIL (`Linux`) | PASS | `navigator.userAgentData.getHighEntropyValues` reports `Windows` `15.0.0` |
| `uach_full_version_four_part` | 8 | FAIL (`HeadlessChrome`) | PASS | `fullVersionList` contains 4-part `Google Chrome` `141.0.7390.37` |

### 3. WebGL1 & WebGL2 60+ Parameter Sweep (`webgl`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `webgl_sweep_60_params` | 5 | PASS (84) | PASS (84) | Sweeps 41 WebGL1 params, 31 WebGL2 params, and 12 shader precision formats |
| `webgl_no_software_or_linux_leak` | 15 | FAIL (`Google SwiftShader`) | PASS | Unmasked renderer reports `ANGLE (Intel, Intel(R) UHD Graphics 630 ... Direct3D11 ...)` with coherent limits (`MAX_TEXTURE_SIZE=16384`, `MAX_SAMPLES=16`, `MAX_UNIFORM_BLOCK_SIZE=65536`) |
| `webgl2_version_coherent` | 6 | PASS | PASS | `WebGL2RenderingContext` reports `WebGL 2.0 (OpenGL ES 3.0 Chromium)` and `WebGL GLSL ES 3.00` |

### 4. Fontconfig Jail & Differential Metrics (`fonts`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `fonts_windows_pack_present` | 10 | FAIL (0/16) | PASS (16/16) | Windows families (`Segoe UI`, `Calibri`, `Cambria`, `Consolas`, `Arial`, `Times New Roman`, `Georgia`, `Verdana`, `Impact`, etc.) resolve via isolated `FONTCONFIG_FILE` |
| `fonts_no_linux_leak` | 10 | FAIL (`DejaVu Sans` etc.) | PASS (0) | Host `/usr/share/fonts` families (`DejaVu Sans`, `Ubuntu`, `Cantarell`, `FreeSans`, `Nimbus Sans`) do not resolve |
| `fonts_differential_distinct` | 10 | FAIL (5 collisions) | PASS (0 collisions) | Pairs (`Segoe UI` vs `Consolas`, `Calibri` vs `Cambria`, `Arial` vs `Courier New`, `Georgia` vs `Verdana`, `Impact` vs `Times New Roman`) measure to distinct pixel widths (defeating single-font alias spoofing) |

### 5. Audio Context & DSP Buffer (`audio`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `audio_sample_rate_48k` | 8 | PASS | PASS | `AudioContext` and `OfflineAudioContext` run at `48000 Hz` Windows WASAPI default mix rate |
| `audio_buffer_rendered` | 4 | PASS | PASS | Renders a real triangle oscillator + `DynamicsCompressorNode` graph and hashes 1,000 samples |

### 6. 2D Canvas Fingerprint (`canvas`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `canvas_2d_rendered` | 4 | PASS | PASS | Renders linear gradients, `"Segoe UI"` + emoji text, and `multiply` composited arcs, computing an FNV-1a pixel hash |

### 7. Permissions, Media, Speech, MatchMedia & Intl (`permissions`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `notification_permission_prompt` | 8 | FAIL (`denied`) | PASS (`prompt`) | `Notification.permission` and `navigator.permissions.query({name:'notifications'})` report `'prompt'`, not headless `'denied'` |
| `matchmedia_fine_pointer` | 8 | FAIL | PASS | `(pointer: fine)` and `(hover: hover)` match; `(pointer: none)` is false |
| `speech_voices_windows` | 5 | FAIL (`[]`) | PASS | `speechSynthesis.getVoices()` returns Windows SAPI voices (`Microsoft David`, `Microsoft Zira`, `Microsoft Mark`) |
| `media_devices_populated` | 4 | PASS | PASS | `navigator.mediaDevices.enumerateDevices()` lists desktop audio/video devices |
| `intl_timezone_non_utc` | 5 | PASS | PASS | `Intl.DateTimeFormat().resolvedOptions().timeZone` is `America/New_York` |

### 8. Prototype & `Function.prototype.toString` Integrity (`integrity`)
| Probe ID | Weight | Stock Headless | XT-Nichrome | What It Verifies |
| --- | --- | --- | --- | --- |
| `function_tostring_native` | 10 | PASS | PASS | `Function.prototype.toString.call` on patched getters/methods returns `function ...() { [native code] }` via WeakMap-backed registration (no `.toString` property attached to wrapped functions) |
| `no_automation_globals` | 10 | PASS | PASS | Zero `cdc_`, `$cdc_`, `__webdriver`, `__playwright`, or `domAutomation` globals on `window` |

### 9. Humanized Input Virtualization (`behavior`)
| Probe ID | Weight | Naive CDP Dispatch | XT-Nichrome (`internal/browser/human.go`) | What It Verifies |
| --- | --- | --- | --- | --- |
| `behavior_human_pointer_and_keys` | 10 | FAIL (0 `mousemove`, 0ms dwell, exact `(0.5, 0.5)` midpoint, `screenY == clientY`, `insertText` with 0 `keydown`) | PASS | Verified on live `fill` + `click`:<br>• **Curved trajectory:** Cubic Bézier path with Flash & Hogan minimum-jerk velocity profile (`10u³ - 15u⁴ + 6u⁵`) + physiological micro-tremor (`14+` `mousemove` events)<br>• **Non-midpoint target:** 2D triangular distribution inside the target's safe interior box, verified via `document.elementFromPoint`<br>• **Press-hold dwell:** `18–42 ms` between `mousedown` and `mouseup`<br>• **Desktop chrome offset:** `MouseEvent.screenY = clientY + (outerHeight - innerHeight)`<br>• **Per-character keystrokes:** Authentic US-QWERTY `keyDown`/`keyUp` with `windowsVirtualKeyCode`, `ShiftLeft` (`modifiers: 8`) transitions, and human digraph cadence (`untrustedCount == 0`) |

## Honest Boundaries (What Cannot Be Spoofed in JS)

XT-Nichrome never weakens a probe to fabricate a pass. Three properties are determined by the host kernel, network stack, or native C++ rasterizer rather than JavaScript:

1. **2D Canvas & WebGL Pixel Hashes:** While font metrics change the canvas hash (`036269c10913e003` with the Windows `Segoe UI` pack vs. `89ded92c36cb200c` on stock headless), sub-pixel anti-aliasing and blending reflect the underlying Chromium Skia rasterizer on the host CPU/GPU. Injecting random noise into `getImageData` is an anti-goal because modern detectors flag per-call canvas non-determinism immediately.
2. **OfflineAudioContext DSP Float Buffer:** The rendered float samples (`94b4800178104777`) come from Blink's real C++ audio pipeline at `48000 Hz`.
3. **Network-Layer Fingerprints (TLS JA3/JA4, HTTP/2 SETTINGS, TCP/IP SYN):** Observed on the server side (`GET /api/connection`), TLS ClientHello cipher/extension order comes from Chromium's BoringSSL (which already matches desktop Chrome 141), while TCP window/TTL parameters come from the host Linux kernel unless routed through an external OS-shaping proxy.
