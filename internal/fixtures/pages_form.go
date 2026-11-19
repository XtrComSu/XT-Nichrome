package fixtures

import (
	"fmt"
	"net/http"
	"strings"
)

// htmlDoc renders a complete document with a doctype, lang and title so every
// fixture is a well-formed accessibility target.
func htmlDoc(title, body string) string {
	var b strings.Builder
	b.Grow(len(body) + 160)
	b.WriteString("<!doctype html>\n<html lang=en>\n<head>\n<meta charset=utf-8>\n<title>")
	b.WriteString(htmlEscape(title))
	b.WriteString("</title>\n</head>\n<body>\n")
	b.WriteString(body)
	b.WriteString("\n</body>\n</html>\n")
	return b.String()
}

// htmlEscape escapes text destined for element content or attribute values.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// writeHTML writes a page with an explicit charset and no caching, so a
// reloaded fixture in an existing profile always shows current behaviour.
func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// handleIndex links every fixture route as a plain list.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, "no such fixture: "+r.URL.Path, http.StatusNotFound)
		return
	}
	type link struct{ href, label string }
	links := []link{
		{"/form", "Form"},
		{"/spa", "Single page app replacement"},
		{"/app", "Counter app"},
		{"/storage", "Storage and service worker"},
		{"/storage/read", "Storage read-back"},
		{"/sw.js", "Service worker script"},
		{"/redirect/ok", "Redirect ok"},
		{"/redirect/denied", "Redirect denied"},
		{"/redirect/chain", "Redirect chain"},
		{"/subresource", "Subresources"},
		{"/subresource/denied", "Denied subresources"},
		{"/img.png", "Image"},
		{"/ws", "WebSocket echo page"},
		{"/headers", "Request headers"},
		{"/slow", "Slow response"},
		{"/frames", "Frames"},
		{"/frame/inner", "Frame inner"},
		{"/dialog", "Dialogs"},
		{"/popup", "Popup"},
		{"/download", "Download"},
		{"/perm", "Permissions"},
		{"/perm/geo", "Geolocation"},
		{"/big", "Big page"},
	}
	var b strings.Builder
	b.WriteString("<h1>Fixture index</h1>\n<ul>\n")
	for _, l := range links {
		fmt.Fprintf(&b, "<li><a href=%q>%s</a></li>\n", l.href, htmlEscape(l.label))
	}
	b.WriteString("</ul>")
	writeHTML(w, http.StatusOK, htmlDoc("Fixture index", b.String()))
}

const formPage = `<h1>Fixture form</h1>
<form id=form method=post action="/form/submit">
  <p><label for=name>Full name</label>
     <input id=name name=name type=text autocomplete=off></p>
  <p><label for=email>Email</label>
     <input id=email name=email type=email autocomplete=off></p>
  <p><label for=password>Password</label>
     <input id=password name=password type=password autocomplete=off></p>
  <p><label for=color>Colour</label>
     <select id=color name=color>
       <option value=red>Red</option>
       <option value=green>Green</option>
       <option value=blue>Blue</option>
     </select></p>
  <p><label for=agree>Agree to terms</label>
     <input id=agree name=agree type=checkbox value=yes></p>
  <p><button id=submit type=submit>Submit</button></p>
</form>
<p><a id=plain-link href="/form/submit?ok=1">Plain link to the submit page</a></p>`

// handleForm serves the form page.
func (s *Server) handleForm(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/form" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Fixture form", formPage))
}

// handleFormSubmit echoes the submitted fields. GET is accepted so a plain
// link to /form/submit works too; in that case every field renders empty.
//
// The password value is never echoed: only the literal "set" or "unset".
func (s *Server) handleFormSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeHTML(w, http.StatusMethodNotAllowed, htmlDoc("Method not allowed", "<h1>Method not allowed</h1>"))
		return
	}
	if err := r.ParseForm(); err != nil {
		writeHTML(w, http.StatusBadRequest, htmlDoc("Bad form", "<h1>Bad form</h1>"))
		return
	}
	name := r.PostFormValue("name")
	email := r.PostFormValue("email")
	color := r.PostFormValue("color")
	if color == "" {
		color = "red"
	}
	agree := "no"
	if r.PostFormValue("agree") != "" {
		agree = "yes"
	}
	password := "unset"
	if r.PostFormValue("password") != "" {
		password = "set"
	}
	echo := fmt.Sprintf("name=%s;email=%s;color=%s;agree=%s;password=%s",
		sanitizeEcho(name), sanitizeEcho(email), sanitizeEcho(color), agree, password)

	// The rendered page carries ?ok=1 on its URL. Setting it with
	// replaceState keeps a single request (no redirect round trip, so the hit
	// log sees one POST) while location.href still ends up as
	// /form/submit?ok=1. Content-Location mirrors it for pure HTTP clients.
	q := r.URL.Query()
	q.Set("ok", "1")
	u := *r.URL
	u.RawQuery = q.Encode()
	w.Header().Set("Content-Location", u.String())

	body := fmt.Sprintf(`<h1>Form submitted</h1>
<p id=echo>%s</p>
<p><a id=back href="/form">Back to form</a></p>
<script>
if (window.history && window.history.replaceState) {
  window.history.replaceState(null, '', %q);
}
</script>`, htmlEscape(echo), u.String())
	writeHTML(w, http.StatusOK, htmlDoc("Form submitted", body))
}

// sanitizeEcho keeps echoed field values single-line and free of separators
// that would make the "k=v;" echo ambiguous.
func sanitizeEcho(v string) string {
	v = strings.Map(func(r rune) rune {
		switch r {
		case ';', '\r', '\n', '\t':
			return ' '
		}
		return r
	}, v)
	if len(v) > 200 {
		v = v[:200]
	}
	return v
}
