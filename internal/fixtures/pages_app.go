package fixtures

import (
	"net/http"
	"strings"
)

// spaPage is the DOM-replacement fixture.
//
// #first is a <button> named "Continue". Its handler replaces the node with a
// brand new element that carries the same accessible name but id=second and a
// different handler, which records stage two. A reference captured before the
// replacement therefore points at a detached node: using it must fail, while a
// fresh lookup by name finds the live replacement.
const spaPage = `<h1>Stage one</h1>
<button id=first type=button>Continue</button>
<p id=note>Stage one is active.</p>
<script>
document.body.dataset.stage = 'one';

function stageTwo() {
  document.body.dataset.stage = 'two';
  if (!document.getElementById('stage')) {
    var p = document.createElement('p');
    p.id = 'stage';
    p.textContent = 'stage two';
    document.body.appendChild(p);
  }
}

// The original handler only swaps the node: the stage stays "one" so a test
// can distinguish "the fresh button was clicked" from "a stale reference was
// clicked" (a detached node does nothing).
var first = document.getElementById('first');
first.addEventListener('click', function () {
  var replacement = document.createElement('button');
  replacement.type = 'button';
  replacement.id = 'second';
  replacement.textContent = 'Continue';
  replacement.addEventListener('click', stageTwo);
  first.parentNode.replaceChild(replacement, first);
  document.getElementById('note').textContent = 'Stage one button was replaced.';
});
</script>`

// handleSPA serves the replacement page.
func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/spa" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	writeHTML(w, http.StatusOK, htmlDoc("Stage one", spaPage))
}

// handleSPAState reports the server-side stage. It starts at "one" and stays
// there: the fixture's stage transition happens in the DOM, so the endpoint is
// a stable baseline the test can compare the DOM against. Tests that want the
// other value can ask for it explicitly with ?stage=two.
func (s *Server) handleSPAState(w http.ResponseWriter, r *http.Request) {
	stage := "one"
	if q := r.URL.Query().Get("stage"); q == "two" {
		stage = "two"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`{"stage":"` + stage + `"}`))
}

// appPage is the action-side-effect fixture.
const appPage = `<h1>Counter</h1>
<p id=count aria-live=polite>0</p>
<button id=inc>Increment</button>
<p><label for=q>Query</label>
   <input id=q name=q type=text autocomplete=off></p>
<form id=f method=get action="/app">
  <label for=term>Search term</label>
  <input id=term name=term type=text autocomplete=off>
  <button id=go type=submit>Search</button>
</form>
<p id=result>term=</p>
<script>
var count = 0;
var out = document.getElementById('count');
document.getElementById('inc').addEventListener('click', function () {
  count += 1;
  out.textContent = String(count);
  // Fire and forget: the click must not wait on the network.
  fetch('/app/click', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({button: 'inc'}),
    keepalive: true
  }).catch(function () {});
});
</script>`

// handleApp serves the counter page and echoes ?term=... after a submit.
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/app" {
		writeHTML(w, http.StatusNotFound, htmlDoc("Not found", "<h1>Not found</h1>"))
		return
	}
	term := r.URL.Query().Get("term")
	page := appPage
	if term != "" {
		// The echo is injected server-side so the result survives a reload and
		// is present in the first snapshot without waiting on script.
		page = strings.Replace(page,
			`<p id=result>term=</p>`,
			`<p id=result>term=`+htmlEscape(sanitizeEcho(term))+`</p>`, 1)
	}
	writeHTML(w, http.StatusOK, htmlDoc("Counter", page))
}

// handleAppClick records the side effect of an increment click.
func (s *Server) handleAppClick(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// The body is intentionally not trusted; only its presence matters.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNoContent)
}
