package fixtures

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// headersResponse is the /headers payload: every request header verbatim plus
// whether the connection was TLS. Client-hint fields (Sec-CH-UA*) are passed
// through as received so a test can assert UA/CH consistency.
type headersResponse struct {
	TLS             bool                `json:"tls"`
	Method          string              `json:"method"`
	Path            string              `json:"path"`
	Proto           string              `json:"proto"`
	Host            string              `json:"host"`
	UserAgent       string              `json:"user_agent"`
	AcceptLanguage  string              `json:"accept_language"`
	SecCHUA         string              `json:"sec_ch_ua"`
	SecCHUAMobile   string              `json:"sec_ch_ua_mobile"`
	SecCHUAPlatform string              `json:"sec_ch_ua_platform"`
	Headers         map[string][]string `json:"headers"`
}

// handleHeaders reports the request headers as JSON.
func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) { s.writeHeaders(w, r) }

// handleHeadersTLS is the same payload on the TLS listener, where tls is true.
func (s *Server) handleHeadersTLS(w http.ResponseWriter, r *http.Request) { s.writeHeaders(w, r) }

func (s *Server) writeHeaders(w http.ResponseWriter, r *http.Request) {
	resp := headersResponse{
		TLS:             r.TLS != nil,
		Method:          r.Method,
		Path:            r.URL.RequestURI(),
		Proto:           r.Proto,
		Host:            r.Host,
		UserAgent:       r.Header.Get("User-Agent"),
		AcceptLanguage:  r.Header.Get("Accept-Language"),
		SecCHUA:         r.Header.Get("Sec-CH-UA"),
		SecCHUAMobile:   r.Header.Get("Sec-CH-UA-Mobile"),
		SecCHUAPlatform: r.Header.Get("Sec-CH-UA-Platform"),
		Headers:         map[string][]string{},
	}
	for k, v := range r.Header {
		cp := make([]string, len(v))
		copy(cp, v)
		resp.Headers[k] = cp
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "")
	_ = enc.Encode(resp)
}

// handleSlow sleeps for ms (default 500, capped at 5000) then answers.
//
// The wait listens on the request context, so a client that gives up releases
// the handler immediately instead of leaving a sleeping goroutine behind.
func (s *Server) handleSlow(w http.ResponseWriter, r *http.Request) {
	ms := defaultSlowMillis
	if v := r.URL.Query().Get("ms"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeHTML(w, http.StatusBadRequest, htmlDoc("Bad milliseconds", "<h1>Bad ms</h1>"))
			return
		}
		ms = n
	}
	if ms > maxSlowMillis {
		ms = maxSlowMillis
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.Context().Done():
		return
	}
	body := fmt.Sprintf("<h1>slow</h1>\n<p id=ms>%d</p>", ms)
	writeHTML(w, http.StatusOK, htmlDoc("Slow", body))
}

// framesPage has two same-origin iframes and one top-level button, so a
// top-level-only snapshot has to report the frames as unsupported.
const framesPage = `<h1>Frames</h1>
<button id=outer-btn type=button>Outer action</button>
<iframe id=frame-a name=frame-a title="Frame A" src="/frame/inner?a"></iframe>
<iframe id=frame-b name=frame-b title="Frame B" src="/frame/inner?b"></iframe>`

// handleFrames serves the frames page.
func (s *Server) handleFrames(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/frames" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Frames", framesPage))
}

// frameInnerPage is the iframe document. The querystring is echoed so both
// frames have distinguishable text while sharing one accessible button name.
const frameInnerPageTpl = `<h1>Inner frame</h1>
<p id=which>%s</p>
<button id=inner type=button>Inner action</button>`

// handleFrameInner serves the iframe document.
func (s *Server) handleFrameInner(w http.ResponseWriter, r *http.Request) {
	which := "frame"
	if q := r.URL.RawQuery; q != "" {
		which = "frame?" + q
	}
	body := fmt.Sprintf(frameInnerPageTpl, htmlEscape(which))
	writeHTML(w, http.StatusOK, htmlDoc("Inner frame", body))
}

// dialogPage triggers blocking dialogs. #after proves the alert was handled:
// script only continues past alert() once the dialog is dismissed.
const dialogPage = `<h1>Dialogs</h1>
<button id=alert type=button>Show alert</button>
<button id=confirm type=button>Ask confirm</button>
<p id=after>before</p>
<script>
document.getElementById('alert').addEventListener('click', function () {
  alert('hi');
  document.getElementById('after').textContent = 'after';
});
document.getElementById('confirm').addEventListener('click', function () {
  var ok = confirm('sure?');
  document.getElementById('after').textContent = ok ? 'confirmed' : 'dismissed';
});
</script>`

// handleDialog serves the dialog page.
func (s *Server) handleDialog(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dialog" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Dialogs", dialogPage))
}

// popupPage tries to open a child window.
const popupPage = `<h1>Popup</h1>
<button id=open type=button>Open child window</button>
<p id=status>idle</p>
<script>
document.getElementById('open').addEventListener('click', function () {
  var w = window.open('/form');
  document.getElementById('status').textContent = w ? 'opened' : 'blocked';
});
</script>`

// handlePopup serves the popup page.
func (s *Server) handlePopup(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/popup" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Popup", popupPage))
}

// downloadPage offers a downloadable file.
const downloadPage = `<h1>Download</h1>
<p><a id=dl href="/download/file" download="x.txt">Download x.txt</a></p>`

// handleDownload serves the download page.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/download" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Download", downloadPage))
}

// handleDownloadFile serves the attachment body.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="x.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("hello"))
}

// permPage queries four permission names and renders the states as JSON.
const permPage = `<h1>Permissions</h1>
<pre id=perm>pending</pre>
<script>
(function () {
  var names = ['geolocation', 'notifications', 'camera', 'microphone'];
  var out = {};
  var done = 0;
  function finish() {
    if (done < names.length) { return; }
    document.getElementById('perm').textContent = JSON.stringify(out);
  }
  names.forEach(function (name) {
    if (!navigator.permissions || !navigator.permissions.query) {
      out[name] = 'unsupported';
      done += 1;
      return;
    }
    navigator.permissions.query({name: name}).then(function (st) {
      out[name] = st.state;
    }).catch(function (e) {
      out[name] = 'error:' + (e && e.name ? e.name : 'unknown');
    }).then(function () {
      done += 1;
      finish();
    });
  });
  finish();
})();
</script>`

// handlePerm serves the permissions page.
func (s *Server) handlePerm(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/perm" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Permissions", permPage))
}

// geoPage asks for one position and writes the outcome.
const geoPage = `<h1>Geolocation</h1>
<pre id=geo>pending</pre>
<script>
(function () {
  var out = document.getElementById('geo');
  if (!navigator.geolocation) {
    out.textContent = '{"error":"unsupported"}';
    return;
  }
  navigator.geolocation.getCurrentPosition(function (pos) {
    out.textContent = JSON.stringify({
      lat: pos.coords.latitude,
      lon: pos.coords.longitude,
      accuracy: pos.coords.accuracy
    });
  }, function (err) {
    out.textContent = JSON.stringify({error: err.code, message: err.message});
  }, {timeout: 3000});
})();
</script>`

// handlePermGeo serves the geolocation page.
func (s *Server) handlePermGeo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/perm/geo" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Geolocation", geoPage))
}

// handleBig renders n repeated characters inside <p id=big> for snapshot
// budget tests. The body is built once into a sized buffer, not concatenated.
func (s *Server) handleBig(w http.ResponseWriter, r *http.Request) {
	n := defaultBigChars
	if v := r.URL.Query().Get("chars"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			writeHTML(w, http.StatusBadRequest, htmlDoc("Bad chars", "<h1>Bad chars</h1>"))
			return
		}
		n = parsed
	}
	if n > maxBigChars {
		n = maxBigChars
	}
	prefix := "<!doctype html>\n<html lang=en>\n<head>\n<meta charset=utf-8>\n<title>Big</title>\n</head>\n<body>\n<h1>Big page</h1>\n<p id=big>"
	suffix := "</p>\n</body>\n</html>\n"
	buf := make([]byte, 0, len(prefix)+n+len(suffix))
	buf = append(buf, prefix...)
	for i := 0; i < n; i++ {
		// Deterministic filler: an ASCII letter cycling a-b-c... keeps the
		// text non-collapsible so the accessibility snapshot really is big.
		buf = append(buf, byte('a'+i%26))
	}
	buf = append(buf, suffix...)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}
