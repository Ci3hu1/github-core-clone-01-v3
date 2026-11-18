// In-memory store for the forge: git objects, refs, repos, issues, PRs.
// Everything lives in maps; nothing touches the disk or the clock, so a
// trial is fast and deterministic. Creation order is a monotonic sequence
// number (never a timestamp).
package main

import (
	"bytes"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
)

type objType string

const (
	objCommit objType = "commit"
	objTree   objType = "tree"
	objBlob   objType = "blob"
	objTag    objType = "tag"
)

type gitObject struct {
	typ  objType
	data []byte
}

type label struct {
	name  string
	color string
}

type comment struct {
	id   int
	body string
	seq  int
}

type issue struct {
	number   int
	title    string
	body     string
	state    string // "open" | "closed"
	labels   []string
	comments []*comment
	seq      int
}

type pull struct {
	number   int
	title    string
	body     string
	state    string // "open" | "closed"
	head     string
	base     string
	merged   bool
	mergedAt string
	mergeSha string
	seq      int
}

type repo struct {
	dbID           int
	owner          string
	name           string
	description    string
	isPrivate      bool
	defaultBranch  string
	refs           map[string]string
	objects        map[string]*gitObject
	labels         map[string]*label
	issues         map[int]*issue
	pulls          map[int]*pull
	counter        int // shared issue+PR number counter
	commentCounter int
	seq            int
}

type store struct {
	// mu guards every map in the store. The HTTP server runs one goroutine
	// per connection, so unlike the single-threaded TS version every
	// dispatch takes this lock (read for GETs / upload-pack, write for
	// mutations) — see dispatchUncached in http.go.
	mu       sync.RWMutex
	repos    map[string]*repo // key owner + "/" + name
	nextDbID int
	seq      int
}

func newStore() *store {
	return &store{repos: make(map[string]*repo), nextDbID: 1}
}

func (s *store) nextSeq() int {
	s.seq++
	return s.seq
}

func repoKey(owner, name string) string { return owner + "/" + name }

func (s *store) createRepo(owner, name, description string, isPrivate bool) *repo {
	r := &repo{
		dbID: s.nextDbID, owner: owner, name: name, description: description,
		isPrivate: isPrivate, defaultBranch: defaultBranch,
		refs: make(map[string]string), objects: make(map[string]*gitObject),
		labels: make(map[string]*label), issues: make(map[int]*issue),
		pulls: make(map[int]*pull), seq: s.nextSeq(),
	}
	s.nextDbID++
	s.repos[repoKey(owner, name)] = r
	return r
}

func (s *store) getRepo(owner, name string) *repo {
	return s.repos[repoKey(owner, name)]
}

func (s *store) deleteRepo(owner, name string) bool {
	k := repoKey(owner, name)
	if _, ok := s.repos[k]; !ok {
		return false
	}
	delete(s.repos, k)
	return true
}

func (s *store) listRepos(owner string) []*repo {
	out := make([]*repo, 0, 4)
	for _, r := range s.repos {
		if r.owner == owner {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// --------------------------------------------------------------- git objects

func putObject(r *repo, typ objType, data []byte) string {
	oid := hashObject(string(typ), data)
	if _, ok := r.objects[oid]; !ok {
		r.objects[oid] = &gitObject{typ: typ, data: data}
	}
	return oid
}

func buildBlob(r *repo, content []byte) string {
	return putObject(r, objBlob, content)
}

type treeEntry struct {
	mode  string
	name  string
	oid   string
	isDir bool
}

// parseTree parses a tree object's raw bytes into entries.
func parseTree(data []byte) []treeEntry {
	entries := make([]treeEntry, 0, 4)
	off := 0
	for off < len(data) {
		sp := bytes.IndexByte(data[off:], ' ')
		if sp < 0 {
			break
		}
		sp += off
		mode := string(data[off:sp])
		nul := bytes.IndexByte(data[sp:], 0)
		if nul < 0 {
			break
		}
		nul += sp
		name := string(data[sp+1 : nul])
		if nul+21 > len(data) {
			break
		}
		oid := hex.EncodeToString(data[nul+1 : nul+21])
		entries = append(entries, treeEntry{mode: mode, name: name, oid: oid, isDir: mode == "40000"})
		off = nul + 21
	}
	return entries
}

// serializeTree serializes tree entries (git-sorted) into raw tree bytes.
func serializeTree(entries []treeEntry) []byte {
	sorted := make([]treeEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return treeNameCompare(sorted[i].name, sorted[i].isDir, sorted[j].name, sorted[j].isDir) < 0
	})
	var out []byte
	for _, e := range sorted {
		out = append(out, []byte(e.mode+" "+e.name)...)
		out = append(out, 0)
		raw, _ := hex.DecodeString(e.oid)
		out = append(out, raw...)
	}
	return out
}

type commitInfo struct {
	tree      string
	parents   []string
	author    string
	committer string
	message   string
}

// parseCommit parses a commit object's raw bytes.
func parseCommit(data []byte) commitInfo {
	text := string(data)
	headEnd := strings.Index(text, "\n\n")
	head := text
	message := ""
	if headEnd >= 0 {
		head = text[:headEnd]
		message = text[headEnd+2:]
	}
	var c commitInfo
	for _, line := range strings.Split(head, "\n") {
		switch {
		case strings.HasPrefix(line, "tree "):
			c.tree = strings.TrimSpace(line[5:])
		case strings.HasPrefix(line, "parent "):
			c.parents = append(c.parents, strings.TrimSpace(line[7:]))
		case strings.HasPrefix(line, "author "):
			c.author = line[7:]
		case strings.HasPrefix(line, "committer "):
			c.committer = line[10:]
		}
	}
	c.message = message
	return c
}

func identityLine(epochSec int) string {
	return viewerName + " <" + viewerEmail + "> " + intStr(epochSec) + " +0000"
}

// buildCommit builds a commit object. Parents in order; message verbatim.
func buildCommit(r *repo, tree string, parents []string, message string, epochSec int) string {
	var sb strings.Builder
	sb.WriteString("tree " + tree + "\n")
	for _, p := range parents {
		sb.WriteString("parent " + p + "\n")
	}
	ident := identityLine(epochSec)
	sb.WriteString("author " + ident + "\ncommitter " + ident + "\n\n" + message)
	return putObject(r, objCommit, []byte(sb.String()))
}

// buildTag builds an annotated tag object.
func buildTag(r *repo, target string, typ objType, name, message string, epochSec int) string {
	text := "object " + target + "\ntype " + string(typ) + "\ntag " + name + "\ntagger " + identityLine(epochSec) + "\n\n" + message
	return putObject(r, objTag, []byte(text))
}

type flatEntry struct {
	oid  string
	mode string
}

// flattenTree is a flat map of path -> {oid, mode} for a tree, recursively.
func flattenTree(r *repo, treeOID string, prefix string) map[string]flatEntry {
	out := make(map[string]flatEntry)
	obj := r.objects[treeOID]
	if obj == nil || obj.typ != objTree {
		return out
	}
	for _, e := range parseTree(obj.data) {
		path := e.name
		if prefix != "" {
			path = prefix + "/" + e.name
		}
		if e.isDir {
			for p, v := range flattenTree(r, e.oid, path) {
				out[p] = v
			}
		} else {
			out[path] = flatEntry{oid: e.oid, mode: e.mode}
		}
	}
	return out
}

type flatNode struct {
	files map[string]flatEntry
	dirs  map[string]*flatNode
}

// buildTreeFromFlat builds a nested tree from a flat path map.
func buildTreeFromFlat(r *repo, flat map[string]flatEntry) string {
	root := &flatNode{files: make(map[string]flatEntry), dirs: make(map[string]*flatNode)}
	for path, v := range flat {
		parts := strings.Split(path, "/")
		node := root
		for i := 0; i < len(parts)-1; i++ {
			next := node.dirs[parts[i]]
			if next == nil {
				next = &flatNode{files: make(map[string]flatEntry), dirs: make(map[string]*flatNode)}
				node.dirs[parts[i]] = next
			}
			node = next
		}
		node.files[parts[len(parts)-1]] = v
	}
	var build func(node *flatNode) string
	build = func(node *flatNode) string {
		entries := make([]treeEntry, 0, len(node.files)+len(node.dirs))
		for name, v := range node.files {
			entries = append(entries, treeEntry{mode: v.mode, name: name, oid: v.oid})
		}
		for name, sub := range node.dirs {
			entries = append(entries, treeEntry{mode: "40000", name: name, oid: build(sub), isDir: true})
		}
		return putObject(r, objTree, serializeTree(entries))
	}
	return build(root)
}

// reachable returns all object ids reachable from a set of tips.
func reachable(r *repo, tips []string) map[string]bool {
	seen := make(map[string]bool)
	stack := make([]string, 0, len(tips))
	stack = append(stack, tips...)
	for len(stack) > 0 {
		oid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[oid] {
			continue
		}
		obj := r.objects[oid]
		if obj == nil {
			continue
		}
		seen[oid] = true
		switch obj.typ {
		case objCommit:
			c := parseCommit(obj.data)
			stack = append(stack, c.tree)
			stack = append(stack, c.parents...)
		case objTree:
			for _, e := range parseTree(obj.data) {
				stack = append(stack, e.oid)
			}
		case objTag:
			if m := tagTarget(obj.data); m != "" {
				stack = append(stack, m)
			}
		}
	}
	return seen
}

// tagTarget extracts the "object <oid>" line of a tag object.
func tagTarget(data []byte) string {
	text := string(data)
	if !strings.HasPrefix(text, "object ") {
		return ""
	}
	line := text[7:]
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if len(line) == 40 && isSha40(line) {
		return line
	}
	return ""
}

// history returns the first-parent history from a tip, newest first.
func history(r *repo, tip string, limit int) []string {
	out := make([]string, 0, 16)
	seen := make(map[string]bool)
	cur := tip
	for cur != "" && !seen[cur] && len(out) < limit {
		seen[cur] = true
		obj := r.objects[cur]
		if obj == nil || obj.typ != objCommit {
			break
		}
		out = append(out, cur)
		parents := parseCommit(obj.data).parents
		if len(parents) == 0 {
			break
		}
		cur = parents[0]
	}
	return out
}

// isAncestor reports whether ancestor is reachable from tip.
func isAncestor(r *repo, ancestor, tip string) bool {
	if ancestor == tip {
		return true
	}
	// Fast path: walk the first-parent chain. Sequential pushes have the
	// old tip as the new tip's direct parent, so this exits in one step.
	cur := tip
	seen := make(map[string]bool)
	for cur != "" && !seen[cur] {
		seen[cur] = true
		obj := r.objects[cur]
		if obj == nil || obj.typ != objCommit {
			break
		}
		parents := parseCommit(obj.data).parents
		for _, p := range parents {
			if p == ancestor {
				return true
			}
		}
		if len(parents) == 0 {
			break
		}
		cur = parents[0]
	}
	// Fallback: full reachable walk (merge histories, non-first-parent).
	return reachable(r, []string{tip})[ancestor]
}

// mergeBase is the first common ancestor of a and b over first-parent chains.
func mergeBase(r *repo, a, b string) string {
	ah := make(map[string]bool)
	for _, c := range history(r, a, 10000) {
		ah[c] = true
	}
	for _, c := range history(r, b, 10000) {
		if ah[c] {
			return c
		}
	}
	return ""
}

type diffItem struct {
	path   string
	status string // "added" | "modified" | "removed"
	oldOID string
	newOID string
}

// diffFlat diffs two flat trees.
func diffFlat(oldFlat, newFlat map[string]flatEntry) []diffItem {
	paths := make(map[string]bool)
	for p := range oldFlat {
		paths[p] = true
	}
	for p := range newFlat {
		paths[p] = true
	}
	out := make([]diffItem, 0, len(paths))
	for p := range paths {
		o, hasO := oldFlat[p]
		n, hasN := newFlat[p]
		switch {
		case hasO && !hasN:
			out = append(out, diffItem{path: p, status: "removed", oldOID: o.oid})
		case !hasO && hasN:
			out = append(out, diffItem{path: p, status: "added", newOID: n.oid})
		case hasO && hasN && o.oid != n.oid:
			out = append(out, diffItem{path: p, status: "modified", oldOID: o.oid, newOID: n.oid})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// lineDiff counts additions/deletions from multiset difference of lines.
func lineDiff(r *repo, oldOID, newOID string) (additions, deletions int) {
	linesOf := func(oid string) []string {
		if oid == "" {
			return nil
		}
		obj := r.objects[oid]
		if obj == nil || obj.typ != objBlob {
			return nil
		}
		text := string(obj.data)
		if text == "" {
			return nil
		}
		lines := strings.Split(text, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		return lines
	}
	oldLines := linesOf(oldOID)
	newLines := linesOf(newOID)
	oldCount := make(map[string]int)
	for _, l := range oldLines {
		oldCount[l]++
	}
	for _, l := range newLines {
		if c := oldCount[l]; c > 0 {
			oldCount[l] = c - 1
		} else {
			additions++
		}
	}
	for _, c := range oldCount {
		deletions += c
	}
	return additions, deletions
}

// mergeConflicts is the three-way conflict check: a file changed on both
// sides of merge-base with different results.
func mergeConflicts(r *repo, baseTip, headTip string) []string {
	base := mergeBase(r, baseTip, headTip)
	if base == "" {
		return nil
	}
	baseCommit := r.objects[base]
	if baseCommit == nil || baseCommit.typ != objCommit {
		return nil
	}
	baseFlat := flattenTree(r, parseCommit(baseCommit.data).tree, "")
	headCommit := r.objects[headTip]
	tipCommit := r.objects[baseTip]
	if headCommit == nil || tipCommit == nil || headCommit.typ != objCommit || tipCommit.typ != objCommit {
		return nil
	}
	headFlat := flattenTree(r, parseCommit(headCommit.data).tree, "")
	tipFlat := flattenTree(r, parseCommit(tipCommit.data).tree, "")
	var conflicts []string
	for path, hv := range headFlat {
		bv, hasB := baseFlat[path]
		tv, hasT := tipFlat[path]
		headChanged := !hasB || bv.oid != hv.oid
		tipChanged := (!hasB && hasT) || (hasB && hasT && bv.oid != tv.oid) || (hasB && !hasT)
		if headChanged && tipChanged {
			tipVal := ""
			if hasT {
				tipVal = tv.oid
			}
			if hv.oid != tipVal {
				conflicts = append(conflicts, path)
			}
		}
	}
	for path, bv := range baseFlat {
		if _, hasH := headFlat[path]; !hasH {
			if tv, hasT := tipFlat[path]; hasT && tv.oid != bv.oid {
				conflicts = append(conflicts, path)
			}
		}
	}
	return conflicts
}
