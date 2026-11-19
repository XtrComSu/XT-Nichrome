// Package snapshots renders compact accessibility snapshots and owns reference
// resolution.
//
// Two properties matter more than completeness here:
//
//  1. A reference must identify one concrete node. A role/name pair is not
//     enough: an SPA can replace a button with an identically named one, so the
//     reference stores the resolved backend node id and a structural signature,
//     and the action path re-checks both immediately before dispatch.
//  2. The output must be bounded. A hostile page can produce a million nodes;
//     the snapshot stops at a character/reference budget and reports truncation
//     instead of growing without limit.
package snapshots

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/browser"
	"github.com/xtrcom/xt-nichrome/internal/types"
)

// Defaults for the output budget (brief §4.1).
const (
	DefaultMaxChars = 16000
	MaxMaxChars     = 200000
	DefaultMaxRefs  = 400
	MaxMaxRefs      = 4000
	MaxNameLen      = 120
	// snapshotTTL bounds how long a reference set stays usable in memory.
	snapshotTTL = 10 * time.Minute
	// maxStoredSnapshots bounds memory per tab.
	maxStoredSnapshots = 8
)

// Ref is a resolved reference to one node.
type Ref struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Name       string `json:"name"`
	BackendID  int    `json:"backendNodeId"`
	Signature  string `json:"signature"`
	Generation int64  `json:"generation"`
	SnapshotID string `json:"snapshotId"`
	// Tag/Type support action compatibility checks.
	Tag        string   `json:"tag"`
	InputType  string   `json:"inputType,omitempty"`
	Actionable bool     `json:"actionable"`
	States     []string `json:"states,omitempty"`
}

// Stored is one snapshot generation kept for reference resolution.
type Stored struct {
	ID         string
	Generation int64
	URL        string
	Created    time.Time
	Refs       map[string]*Ref
	Text       string
	Truncated  bool
}

// Registry holds the per-tab snapshot state. All methods are safe for
// concurrent use; the session layer additionally serialises per tab.
type Registry struct {
	mu    sync.Mutex
	byTab map[string][]*Stored
	// current is the newest snapshot id per tab.
	current map[string]string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{byTab: map[string][]*Stored{}, current: map[string]string{}}
}

// Invalidate drops every snapshot for a tab (navigation, close, crash).
func (r *Registry) Invalidate(tabID string) {
	r.mu.Lock()
	delete(r.byTab, tabID)
	delete(r.current, tabID)
	r.mu.Unlock()
}

// Current returns the newest stored snapshot for a tab.
func (r *Registry) Current(tabID string) *Stored {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.byTab[tabID]
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

// Get returns a stored snapshot by id.
func (r *Registry) Get(tabID, snapshotID string) *Stored {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.byTab[tabID] {
		if s.ID == snapshotID {
			return s
		}
	}
	return nil
}

// Put stores a new snapshot, superseding older ones.
func (r *Registry) Put(tabID string, s *Stored) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := append(r.byTab[tabID], s)
	// Keep a short history so an in-flight action that names the previous
	// snapshot can still be classified as stale rather than simply not found.
	if len(list) > maxStoredSnapshots {
		list = list[len(list)-maxStoredSnapshots:]
	}
	r.byTab[tabID] = list
	r.current[tabID] = s.ID
}

// NewID makes an opaque snapshot id.
func NewID() string {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return "s" + hex.EncodeToString(b[:])
}

// refPrefix is the stable ref label prefix.
const refPrefix = "e"

// interactiveRoles are the AX roles that get a reference.
var interactiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true,
	"checkbox": true, "radio": true, "combobox": true, "listbox": true,
	"option": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "tab": true, "switch": true, "slider": true,
	"spinbutton": true, "treeitem": true, "menubar": true, "toolbar": true,
	"gridcell": true, "row": true, "columnheader": true, "rowheader": true,
	"disclosuretriangle": true, "textboxmultiline": true, "togglebutton": true,
	"listitem": true, "form": true, "dialog": true, "alertdialog": true,
}

// structuralRoles always render even without a name, so the agent sees page
// structure rather than a flat list of buttons.
var structuralRoles = map[string]bool{
	"rootwebarea": true, "document": true, "main": true, "navigation": true,
	"banner": true, "contentinfo": true, "complementary": true, "region": true,
	"form": true, "dialog": true, "alertdialog": true, "heading": true,
	"list": true, "listitem": true, "table": true, "row": true, "cell": true,
	"paragraph": true, "group": true, "tablist": true, "tabpanel": true,
	"menu": true, "menubar": true, "toolbar": true, "status": true,
	"alert": true, "article": true, "section": true, "figure": true,
	"figurecaption": true, "term": true, "definition": true, "log": true,
	"marquee": true, "timer": true,
}

// skipRoles carry no information for an agent.
var skipRoles = map[string]bool{
	"none": true, "presentation": true, "generic": true, "inlinetextbox": true,
	"statictext": true, "linebreak": true, "listmarker": true, "ignored": true,
	"separation": true, "comment": true, "blockquote": true,
}

// Options bound one snapshot render.
type Options struct {
	MaxChars      int
	MaxRefs       int
	IncludeValues bool
	// Scope is "page" for the top-level document. Anything else is reported as
	// unsupported rather than silently returning a partial tree.
	Scope string
	// Timeout bounds the whole render.
	Timeout time.Duration
}

// Result is the rendered snapshot plus the stored reference table.
type Result struct {
	Snapshot *Stored
	Public   types.SnapshotResult
}

// Build renders a snapshot for a page.
func Build(ctx context.Context, reg *Registry, tabID string, pg *browser.Page, opts Options) (*Result, *types.APIError) {
	if opts.Scope != "" && opts.Scope != "page" {
		return nil, &types.APIError{
			Code:    types.CodeUnsupported,
			Message: "only the top-level document scope is supported",
			Detail:  map[string]any{"scope": opts.Scope, "supported": []string{"page"}},
		}
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	if maxChars > MaxMaxChars {
		maxChars = MaxMaxChars
	}
	maxRefs := opts.MaxRefs
	if maxRefs <= 0 {
		maxRefs = DefaultMaxRefs
	}
	if maxRefs > MaxMaxRefs {
		maxRefs = MaxMaxRefs
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	gen := pg.Generation()
	nodes, apiErr := pg.AccessibilityTree(cctx)
	if apiErr != nil {
		return nil, apiErr
	}

	// Frames: report them instead of pretending the snapshot is complete.
	frames, _ := topLevelFrames(cctx, pg)

	snapID := NewID()
	stored := &Stored{
		ID: snapID, Generation: gen, URL: pg.CurrentURL(), Created: time.Now(),
		Refs: map[string]*Ref{},
	}
	rendered, at := renderTree(nodes, stored, maxChars, maxRefs, opts.IncludeValues)

	truncated := at
	if len(rendered) > maxChars {
		rendered = truncateRunes(rendered, maxChars)
		truncated = true
	}
	public := types.SnapshotResult{
		Generation: gen,
		SnapshotID: snapID,
		URL:        stored.URL,
		Title:      pg.Title(),
		Text:       rendered,
		Truncated:  truncated,
		RefCount:   len(stored.Refs),
	}
	if len(frames) > 1 {
		for _, f := range frames[1:] {
			public.UnsupportedFrames = append(public.UnsupportedFrames, f)
		}
	}
	stored.Text = rendered
	stored.Truncated = truncated
	reg.Put(tabID, stored)
	pg.TouchActivity()
	return &Result{Snapshot: stored, Public: public}, nil
}

func topLevelFrames(ctx context.Context, pg *browser.Page) ([]string, error) {
	var res struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
			ChildFrames []struct {
				Frame struct {
					ID  string `json:"id"`
					URL string `json:"url"`
				} `json:"frame"`
			} `json:"childFrames"`
		} `json:"frameTree"`
	}
	err := pg.Call(ctx, "Page.getFrameTree", nil, &res)
	if err != nil {
		return nil, err
	}
	out := []string{res.FrameTree.Frame.ID}
	for _, c := range res.FrameTree.ChildFrames {
		out = append(out, c.Frame.URL)
	}
	return out, nil
}

// renderTree walks the AX tree in document order and emits a compact indented
// outline. Returns the text and whether a budget was hit.
func renderTree(nodes []browser.AXNode, stored *Stored, maxChars, maxRefs int, includeValues bool) (string, bool) {
	byID := make(map[string]*browser.AXNode, len(nodes))
	var roots []*browser.AXNode
	for i := range nodes {
		n := &nodes[i]
		byID[n.NodeID] = n
	}
	for i := range nodes {
		n := &nodes[i]
		if n.ParentID == "" {
			roots = append(roots, n)
			continue
		}
		if _, ok := byID[n.ParentID]; !ok {
			roots = append(roots, n)
		}
	}
	if len(roots) == 0 && len(nodes) > 0 {
		roots = []*browser.AXNode{&nodes[0]}
	}

	var sb strings.Builder
	refSeq := 0
	budgetHit := false
	visited := make(map[string]bool, len(nodes))

	var walk func(n *browser.AXNode, depth int)
	walk = func(n *browser.AXNode, depth int) {
		if n == nil || budgetHit {
			return
		}
		if visited[n.NodeID] {
			return
		}
		visited[n.NodeID] = true

		role := ""
		if n.Role != nil {
			role = strings.ToLower(fmt.Sprint(n.Role.Value))
		}
		name := ""
		if n.Name != nil && n.Name.Value != nil {
			name = sanitizeName(fmt.Sprint(n.Name.Value))
		}
		if n.Ignored && !interactiveRoles[role] {
			for _, c := range n.ChildIDs {
				walk(byID[c], depth)
			}
			return
		}
		if skipRoles[role] && name == "" {
			for _, c := range n.ChildIDs {
				walk(byID[c], depth)
			}
			return
		}

		states := collectStates(n, role)
		line := ""
		refID := ""
		actionable := interactiveRoles[role] && n.BackendDOMNodeID != 0

		if actionable {
			if len(stored.Refs) >= maxRefs {
				budgetHit = true
				return
			}
			refSeq++
			refID = fmt.Sprintf("%s%d", refPrefix, refSeq)
			stored.Refs[refID] = &Ref{
				ID: refID, Role: role, Name: name,
				BackendID: n.BackendDOMNodeID, Generation: stored.Generation,
				SnapshotID: stored.ID, Actionable: true, States: states,
			}
		}

		label := role
		if label == "" {
			label = "node"
		}
		line = strings.Repeat("  ", depth) + label
		if name != "" {
			line += " " + quote(name)
		}
		if includeValues && n.Value != nil && n.Value.Value != nil && role != "textbox" {
			line += " value=" + quote(sanitizeName(fmt.Sprint(n.Value.Value)))
		}
		if len(states) > 0 {
			line += " [" + strings.Join(states, ",") + "]"
		}
		if refID != "" {
			line += " @" + refID
		}
		line += "\n"
		if sb.Len()+len(line) > maxChars {
			budgetHit = true
			return
		}
		sb.WriteString(line)

		for _, c := range n.ChildIDs {
			walk(byID[c], depth+1)
		}
	}

	for _, r := range roots {
		walk(r, 0)
	}
	return sb.String(), budgetHit
}

// collectStates emits only the states that change how an agent should act.
func collectStates(n *browser.AXNode, role string) []string {
	var out []string
	for _, p := range n.Properties {
		v := p.Value.Value
		switch p.Name {
		case "disabled":
			if truthy(v) {
				out = append(out, "disabled")
			}
		case "readonly":
			if truthy(v) {
				out = append(out, "readonly")
			}
		case "required":
			if truthy(v) {
				out = append(out, "required")
			}
		case "checked":
			switch fmt.Sprint(v) {
			case "true":
				out = append(out, "checked")
			case "mixed":
				out = append(out, "mixed")
			}
		case "expanded":
			if truthy(v) {
				out = append(out, "expanded")
			} else if v != nil {
				out = append(out, "collapsed")
			}
		case "selected":
			if truthy(v) {
				out = append(out, "selected")
			}
		case "pressed":
			if truthy(v) {
				out = append(out, "pressed")
			}
		case "focused":
			if truthy(v) {
				out = append(out, "focused")
			}
		case "invalid":
			s := fmt.Sprint(v)
			if s != "" && s != "false" {
				out = append(out, "invalid")
			}
		case "modal":
			if truthy(v) {
				out = append(out, "modal")
			}
		case "level":
			out = append(out, fmt.Sprintf("level=%v", v))
		case "haspopup":
			s := fmt.Sprint(v)
			if s != "" && s != "false" {
				out = append(out, "haspopup")
			}
		case "valuenow", "valuemin", "valuemax":
			// Kept out by default: slider values are live field data.
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	case nil:
		return false
	default:
		return fmt.Sprint(t) == "true"
	}
}

// quote renders a name in a stable, single-line, single-quoted form.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\r' {
			return ' '
		}
		return r
	}, s)
	if len(s) > MaxNameLen {
		return truncateRunes(s, MaxNameLen)
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Resolve validates a reference and returns the node it points at.
//
// Order of checks matters: snapshot ownership, then document generation, then
// node liveness. Each failure has a distinct code so the agent can decide
// whether to re-snapshot (stale) or stop (policy/busy).
func Resolve(reg *Registry, tabID string, pg *browser.Page, snapshotID, ref string, wantGeneration int64) (*Ref, *types.APIError) {
	cur := reg.Current(tabID)
	if cur == nil {
		return nil, &types.APIError{Code: types.CodeStaleRef, Message: "no snapshot is current for this tab; take a fresh snapshot"}
	}
	stored := reg.Get(tabID, snapshotID)
	if stored == nil {
		return nil, &types.APIError{
			Code:    types.CodeStaleRef,
			Message: "snapshot id is unknown or has been superseded; take a fresh snapshot",
			Detail:  map[string]any{"snapshotId": snapshotID, "currentSnapshotId": cur.ID},
		}
	}
	if stored.ID != cur.ID {
		return nil, &types.APIError{
			Code:    types.CodeStaleRef,
			Message: "snapshot has been superseded by a newer snapshot; take a fresh snapshot",
			Detail:  map[string]any{"snapshotId": stored.ID, "currentSnapshotId": cur.ID},
		}
	}
	if wantGeneration != 0 && wantGeneration != stored.Generation {
		return nil, &types.APIError{
			Code:    types.CodeStaleRef,
			Message: "generation does not match the snapshot",
			Detail:  map[string]any{"requested": wantGeneration, "snapshot": stored.Generation},
		}
	}
	live := pg.Generation()
	if stored.Generation != live {
		return nil, &types.APIError{
			Code:    types.CodeStaleRef,
			Message: "the document has navigated since the snapshot; take a fresh snapshot",
			Detail:  map[string]any{"snapshotGeneration": stored.Generation, "currentGeneration": live},
		}
	}
	r, ok := stored.Refs[ref]
	if !ok {
		return nil, &types.APIError{
			Code:    types.CodeStaleRef,
			Message: "reference is not part of the current snapshot",
			Detail:  map[string]any{"ref": ref, "refCount": len(stored.Refs)},
		}
	}
	return r, nil
}
