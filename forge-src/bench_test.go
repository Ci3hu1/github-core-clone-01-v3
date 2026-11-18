package main

import (
	"bytes"
	"testing"
)

// buildCloneHistoryRepo builds a 200-commit linear history like the bench's
// clone fixture: each commit changes one file with a 512-byte blob.
func buildCloneHistoryRepo(b *testing.B) (*store, *repo, string) {
	s := newStore()
	r := s.createRepo(viewerLogin, "bench-clone", "", false)
	var parent string
	var tip string
	for i := 0; i < 200; i++ {
		content := make([]byte, 512)
		for j := range content {
			content[j] = byte('a' + ((i + j) % 26))
		}
		blob := buildBlob(r, content)
		tree := buildTreeFromFlat(r, map[string]flatEntry{"file.txt": {oid: blob, mode: "100644"}})
		var parents []string
		if parent != "" {
			parents = []string{parent}
		}
		tip = buildCommit(r, tree, parents, "commit "+intStr(i), fixedEpochSec)
		parent = tip
	}
	r.refs["refs/heads/main"] = tip
	return s, r, tip
}

func BenchmarkCloneFetch(b *testing.B) {
	_, r, tip := buildCloneHistoryRepo(b)
	// Simulate the depth-1 fetch: want tip, deepen 1.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		deepen := 1
		plan := planFetch(r, []string{tip}, nil, nil, &deepen)
		pack := writePack(plan.toSend)
		_ = pack
	}
}

func BenchmarkCloneFetchFull(b *testing.B) {
	_, r, tip := buildCloneHistoryRepo(b)
	// Full fetch handler path (v2 upload-pack fetch) for depth 1.
	lines := []string{
		"command=fetch",
		"object-format=sha1",
		"agent=benchmark",
		"want " + tip,
		"deepen 1",
		"done",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := handleUploadPackV2Fetch(r, lines)
		if len(out) == 0 {
			b.Fatal("empty fetch response")
		}
		_ = bytes.Count(out, []byte{0})
	}
}

func BenchmarkReadIssue(b *testing.B) {
	s := newStore()
	r := s.createRepo(viewerLogin, "bench-read", "", false)
	r.counter++
	is := &issue{number: r.counter, title: "bench", body: "body", state: "open", seq: s.nextSeq()}
	r.issues[is.number] = is
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var w jw
		writeIssueView(&w, r, is)
	}
}
