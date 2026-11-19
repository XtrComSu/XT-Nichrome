package fixtures

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// storageCookie is the cookie name used by the storage fixtures.
const storageCookie = "sid"

// newSessionID returns a random cookie value. This is the only nondeterminism
// in the package, and it is exactly what makes persistence observable: a
// second page proves it read the same cookie the first page wrote.
func newSessionID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallbacksession"
	}
	return hex.EncodeToString(b[:])
}

// storageState renders the <pre id=state> block: exactly the two values the
// page can read synchronously (cookie, localStorage). The textContent is
// rewritten by page script once it has read them for real. sessionStorage and
// IndexedDB outcomes live in a separate <p id=stores> so #state stays stable
// and its contents are exactly "cookie=...\nlocal=...".
func storageState(cookie, local string) string {
	return fmt.Sprintf("<pre id=state>cookie=%s\nlocal=%s</pre>",
		htmlEscape(cookie), htmlEscape(local))
}

// storageStores renders the secondary line for the non-synchronous stores.
func storageStores(session, idb string) string {
	return fmt.Sprintf("<p id=stores>session=%s;idb=%s</p>",
		htmlEscape(session), htmlEscape(idb))
}

// storageScript reads cookie/localStorage/sessionStorage synchronously and
// rewrites #state, then (optionally) writes the durable stores.
func storageScript(write bool) string {
	var b strings.Builder
	b.WriteString(`<script>
(function () {
  var pre = document.getElementById('state');
  var m = {};
  document.cookie.split(';').forEach(function (p) {
    var i = p.indexOf('=');
    if (i < 0) { return; }
    var k = p.slice(0, i).trim();
    if (k) { m[k] = decodeURIComponent(p.slice(i + 1).trim()); }
  });
  var cookie = m['sid'] || 'none';
`)
	if write {
		fmt.Fprintf(&b, `  try { localStorage.setItem('k', %q); } catch (e) {}
  try { sessionStorage.setItem('k', %q); } catch (e) {}
  var idbState = 'write-pending';
`, storageLocalValue, storageSessionValue)
		fmt.Fprintf(&b, "  var IDB_VALUE = %q;\n", storageIDBValue)
	} else {
		b.WriteString(`  var idbState = 'read-only';
`)
	}
	b.WriteString(`  var local = 'none', session = 'none';
  try { local = localStorage.getItem('k') || 'none'; } catch (e) { local = 'error'; }
  try { session = sessionStorage.getItem('k') || 'none'; } catch (e) { session = 'error'; }
  pre.textContent = 'cookie=' + cookie + '\nlocal=' + local;
  var stores = document.getElementById('stores');
  function showStores(idb) {
    if (stores) { stores.textContent = 'session=' + session + ';idb=' + idb; }
  }
  showStores(idbState);
`)
	if write {
		b.WriteString(`  try {
    var req = indexedDB.open('fixture', 1);
    req.onupgradeneeded = function () {
      var db = req.result;
      if (!db.objectStoreNames.contains('kv')) { db.createObjectStore('kv'); }
    };
    req.onsuccess = function () {
      var db = req.result;
      try {
        var tx = db.transaction('kv', 'readwrite');
        tx.objectStore('kv').put(IDB_VALUE, 'k');
        tx.oncomplete = function () { showStores('put-' + IDB_VALUE); };
      } catch (e) {
        showStores('error');
      }
    };
    req.onerror = function () { showStores('error'); };
  } catch (e) {
    showStores('error');
  }
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js').catch(function () {});
  }
`)
	}
	b.WriteString("})();\n</script>")
	return b.String()
}

// handleStorage writes state (cookie, localStorage, sessionStorage, IndexedDB)
// and renders whatever it can read back.
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/storage" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	// A stable sid per browser profile is what makes the read-back page a
	// persistence proof. It is minted when the profile has no sid yet, and
	// rotated on demand with ?rotate=1. (Rotating on every load would let any
	// incidental load -- including the service worker's precache fetch of
	// /storage -- invalidate the value a test just captured.)
	sid := ""
	if c, err := r.Cookie(storageCookie); err == nil && r.URL.Query().Get("rotate") != "1" {
		if c.Value != "" && len(c.Value) <= 64 {
			sid = c.Value
		}
	}
	if sid == "" {
		sid = newSessionID()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     storageCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   3600,
		Expires:  time.Now().Add(time.Hour).UTC(),
	})
	body := "<h1>Storage</h1>\n" +
		storageState(sid, "write-pending") +
		"\n" + storageStores("write-pending", "write-pending") +
		"\n" + storageScript(true) +
		"\n<p><a id=read href=\"/storage/read\">Read the stored state back</a></p>"
	writeHTML(w, http.StatusOK, htmlDoc("Storage", body))
}

// handleStorageRead re-renders the state from the cookie the browser already
// has plus localStorage, so a fresh page in the same profile proves the values
// survived.
func (s *Server) handleStorageRead(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/storage/read" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	cookie := "none"
	if c, err := r.Cookie(storageCookie); err == nil && c.Value != "" {
		cookie = c.Value
	}
	body := "<h1>Storage read back</h1>\n" +
		storageState(cookie, "none") +
		"\n" + storageStores("read-only", "read-only") +
		"\n" + storageScript(false) +
		"\n<p><a id=write href=\"/storage\">Write the state again</a></p>"
	writeHTML(w, http.StatusOK, htmlDoc("Storage read back", body))
}

// swScript is the service worker: it precaches /storage into "fixture-v1",
// answers fetch cache-first, and replies to {type:'ping'} with a pong that
// carries the cache name.
const swScript = `'use strict';
var CACHE = 'fixture-v1';
var PRECACHE = ['/storage'];

self.addEventListener('install', function (event) {
  event.waitUntil(
    caches.open(CACHE).then(function (cache) { return cache.addAll(PRECACHE); })
      .then(function () { return self.skipWaiting(); })
  );
});

self.addEventListener('activate', function (event) {
  event.waitUntil(self.clients.claim());
});

// Only the precached paths are served cache-first. Everything else is left
// completely alone: a scope of "/" would otherwise replay a cached /slow,
// /headers or /app response and the hit log would never see the request.
self.addEventListener('fetch', function (event) {
  var req = event.request;
  if (req.method !== 'GET') { return; }
  var url = new URL(req.url);
  if (url.origin !== self.location.origin) { return; }
  if (PRECACHE.indexOf(url.pathname) < 0) { return; }
  event.respondWith(
    caches.match(req).then(function (hit) {
      if (hit) { return hit; }
      return fetch(req).then(function (res) {
        if (res && res.ok) {
          var copy = res.clone();
          caches.open(CACHE).then(function (c) { c.put(req, copy); });
        }
        return res;
      });
    })
  );
});

// A page may post {type:'ping'} with a transferred MessagePort. Reply on that
// port when it exists (it is the only channel a port listener can hear), and
// otherwise fall back to the client that sent the message.
self.addEventListener('message', function (event) {
  var data = event.data || {};
  if (data.type !== 'ping') { return; }
  var reply = {type: 'pong', cache: CACHE};
  if (event.ports && event.ports.length > 0) {
    event.ports[0].postMessage(reply);
  } else if (event.source) {
    event.source.postMessage(reply);
  }
});
`

// handleServiceWorker serves the service worker script.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write([]byte(swScript))
}
