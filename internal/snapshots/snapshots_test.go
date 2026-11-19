package snapshots

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xtrcom/xt-nichrome/internal/browser"
)

// node builds an AX node with the fields the renderer reads.
func node(id, parent, role, name string, backend int, children ...string) browser.AXNode {
	n := browser.AXNode{NodeID: id, ParentID: parent, BackendDOMNodeID: backend, ChildIDs: children}
	if role != "" {
		n.Role = &browser.AXValue{Type: "role", Value: role}
	}
	if name != "" {
		n.Name = &browser.AXValue{Type: "computedString", Value: name}
	}
	return n
}

func tree() []browser.AXNode {
	return []browser.AXNode{
		node("1", "", "RootWebArea", "Fixture form", 1, "2", "3", "4"),
		node("2", "1", "heading", "Fixture form", 2),
		node("3", "1", "textbox", "Full name", 3),
		node("4", "1", "button", "Submit", 4),
	}
}

func TestRenderTreeProducesStableRefs(t *testing.T) {
	stored := &Stored{ID: "s1", Generation: 7, Refs: map[string]*Ref{}}
	text, truncated := renderTree(tree(), stored, 4096, 100, false)
	if truncated {
		t.Fatal("a tiny tree was reported truncated")
	}
	for _, want := range []string{"rootwebarea", "heading", "textbox", `"Full name"`, "@e1", "@e2", "button"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered text is missing %q:\n%s", want, text)
		}
	}
	if len(stored.Refs) != 2 {
		t.Fatalf("got %d refs, want 2 (textbox and button only)", len(stored.Refs))
	}
	// Refs must be assigned in document order and be stable across renders of
	// the same tree, or an agent's cached ref silently points elsewhere.
	stored2 := &Stored{ID: "s2", Generation: 7, Refs: map[string]*Ref{}}
	text2, _ := renderTree(tree(), stored2, 4096, 100, false)
	if text != text2 {
		t.Errorf("two renders of the same tree differ:\n%s\n---\n%s", text, text2)
	}
	if got := stored.Refs["e1"].Name; got != "Full name" {
		t.Errorf("e1 is %q, want the first interactive node", got)
	}
}

func TestRefBudgetIsEnforced(t *testing.T) {
	nodes := []browser.AXNode{node("1", "", "RootWebArea", "root", 1)}
	kids := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("%d", i+2)
		kids = append(kids, id)
		nodes = append(nodes, node(id, "1", "button", fmt.Sprintf("B%d", i), i+2))
	}
	nodes[0].ChildIDs = kids
	stored := &Stored{ID: "s", Refs: map[string]*Ref{}}
	_, truncated := renderTree(nodes, stored, 1000000, 7, false)
	if !truncated {
		t.Error("a ref budget of 7 over 50 buttons did not report truncation")
	}
	if len(stored.Refs) > 7 {
		t.Errorf("got %d refs with a budget of 7", len(stored.Refs))
	}
}

func TestCharBudgetIsEnforced(t *testing.T) {
	nodes := []browser.AXNode{node("1", "", "RootWebArea", "root", 1)}
	for i := 0; i < 200; i++ {
		nodes = append(nodes, node(fmt.Sprintf("%d", i+2), "1", "button", strings.Repeat("x", 60), i+2))
		nodes[0].ChildIDs = append(nodes[0].ChildIDs, fmt.Sprintf("%d", i+2))
	}
	stored := &Stored{ID: "s", Refs: map[string]*Ref{}}
	text, truncated := renderTree(nodes, stored, 500, 1000, false)
	if !truncated {
		t.Error("a char budget of 500 over 200 buttons did not report truncation")
	}
	if len(text) > 500+200 { // one line may overshoot before the check
		t.Errorf("rendered %d chars against a 500 budget", len(text))
	}
}

func TestHiddenNodesAreSkippedButChildrenAreKept(t *testing.T) {
	nodes := []browser.AXNode{
		node("1", "", "RootWebArea", "root", 1, "2"),
		{NodeID: "2", ParentID: "1", Ignored: true, ChildIDs: []string{"3"}},
		node("3", "2", "button", "Real", 3),
	}
	stored := &Stored{ID: "s", Refs: map[string]*Ref{}}
	text, _ := renderTree(nodes, stored, 4096, 100, false)
	if !strings.Contains(text, "Real") {
		t.Errorf("a child of an ignored node was dropped:\n%s", text)
	}
	if len(stored.Refs) != 1 {
		t.Errorf("got %d refs, want 1", len(stored.Refs))
	}
}

func TestNoRefsForNonInteractiveNodes(t *testing.T) {
	nodes := []browser.AXNode{
		node("1", "", "RootWebArea", "root", 1, "2", "3"),
		node("2", "1", "heading", "Title", 2),
		node("3", "1", "StaticText", "just words", 3),
	}
	stored := &Stored{ID: "s", Refs: map[string]*Ref{}}
	renderTree(nodes, stored, 4096, 100, false)
	if len(stored.Refs) != 0 {
		t.Fatalf("non-interactive nodes got references: %+v", stored.Refs)
	}
}

func TestStatesAreReported(t *testing.T) {
	n := node("1", "", "button", "Go", 1)
	n.Properties = []browser.AXProperty{
		{Name: "disabled", Value: browser.AXValue{Value: true}},
		{Name: "focused", Value: browser.AXValue{Value: true}},
	}
	states := collectStates(&n, "button")
	joined := strings.Join(states, ",")
	for _, want := range []string{"disabled", "focused"} {
		if !strings.Contains(joined, want) {
			t.Errorf("states %v missing %q", states, want)
		}
	}
}

func TestRegistryInvalidateAndSupersede(t *testing.T) {
	reg := NewRegistry()
	first := &Stored{ID: "s1", Generation: 1, Refs: map[string]*Ref{}}
	second := &Stored{ID: "s2", Generation: 2, Refs: map[string]*Ref{}}
	reg.Put("tab", first)
	if cur := reg.Current("tab"); cur == nil || cur.ID != "s1" {
		t.Fatalf("current is %v", cur)
	}
	reg.Put("tab", second)
	if cur := reg.Current("tab"); cur == nil || cur.ID != "s2" {
		t.Fatalf("current did not advance")
	}
	if got := reg.Get("tab", "s1"); got == nil {
		t.Error("the previous snapshot was dropped immediately; an in-flight action could not be classified as stale")
	}
	reg.Invalidate("tab")
	if cur := reg.Current("tab"); cur != nil {
		t.Error("Invalidate left a snapshot behind")
	}
}

func TestRegistryIsBounded(t *testing.T) {
	reg := NewRegistry()
	for i := 0; i < 100; i++ {
		reg.Put("tab", &Stored{ID: fmt.Sprintf("s%d", i), Refs: map[string]*Ref{}})
	}
	reg.mu.Lock()
	n := len(reg.byTab["tab"])
	reg.mu.Unlock()
	if n > maxStoredSnapshots {
		t.Fatalf("%d snapshots retained, cap is %d", n, maxStoredSnapshots)
	}
}

func TestNewIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestSanitizeNameBounds(t *testing.T) {
	long := strings.Repeat("a", 400)
	if got := len([]rune(sanitizeName(long))); got > MaxNameLen {
		t.Errorf("sanitizeName kept %d runes, cap is %d", got, MaxNameLen)
	}
	if got := sanitizeName("a\nb\tc"); strings.ContainsAny(got, "\n\t") {
		t.Errorf("sanitizeName left control characters: %q", got)
	}
}
