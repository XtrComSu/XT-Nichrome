package fixtures

import (
	"fmt"
	"net/http"
)

// redirectOKTarget is where /redirect/ok lands.
const redirectOKTarget = "/form"

// handleRedirectOK is the healthy redirect: 302 to the form page.
func (s *Server) handleRedirectOK(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, redirectOKTarget, http.StatusFound)
}

// handleRedirectDenied is the blocked-navigation fixture: 302 to a host the
// daemon must refuse to follow. With the default target nothing on the network
// can receive the request, so the absence of a denial observation is the
// expected outcome.
func (s *Server) handleRedirectDenied(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.deniedTarget("/x"), http.StatusFound)
}

// handleRedirectChain is a two-hop redirect: chain -> ok -> form.
func (s *Server) handleRedirectChain(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/redirect/ok", http.StatusFound)
}

// pngPixel is a valid 1x1 transparent PNG, emitted verbatim so no encoder is
// involved and the bytes are identical on every request.
const pngPixel = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01" +
	"\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05" +
	"\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82"

// subresourcePage loads one of each subresource type and only reports "sub-ok"
// once the script has run, so a snapshot proves all three loads succeeded.
// #sub is deliberately placed before the script element: /sub.js resolves the
// node by id and would throw if it ran while the paragraph did not exist yet.
const subresourcePage = `<h1>Subresources</h1>
<img id=img src="/img.png" alt="Fixture pixel">
<link rel=stylesheet href="/sub.css">
<p id=sub>waiting</p>
<script src="/sub.js"></script>`

// subresourceDeniedPage loads the same shapes from the denied host. All three
// must fail silently: the page stays on "waiting" and no script runs.
// Same ordering rule as the healthy page: #sub precedes the script element so
// the denied page keeps an identical DOM shape (only the URLs differ).
const subresourceDeniedPageTpl = `<h1>Denied subresources</h1>
<img id=img src="%s" alt="Denied pixel">
<link rel=stylesheet href="%s">
<p id=sub>waiting</p>
<script src="%s"></script>`

// handleSubresource serves the healthy subresource page.
func (s *Server) handleSubresource(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/subresource" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Subresources", subresourcePage))
}

// handleSubresourceDenied serves the blocked subresource page.
func (s *Server) handleSubresourceDenied(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/subresource/denied" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	body := fmt.Sprintf(subresourceDeniedPageTpl,
		htmlEscape(s.deniedTarget("/i.png")),
		htmlEscape(s.deniedTarget("/s.css")),
		htmlEscape(s.deniedTarget("/s.js")),
	)
	writeHTML(w, http.StatusOK, htmlDoc("Denied subresources", body))
}

// handleImage serves the 1x1 PNG.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(pngPixel))
}

// subJS runs in the healthy subresource page and flips #sub to sub-ok.
const subJS = `document.getElementById('sub').textContent = 'sub-ok';`

// handleSubJS serves the subresource script.
func (s *Server) handleSubJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(subJS))
}

// subCSS is the subresource stylesheet.
const subCSS = "#sub{color:#123456}"

// handleSubCSS serves the subresource stylesheet.
func (s *Server) handleSubCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(subCSS))
}
