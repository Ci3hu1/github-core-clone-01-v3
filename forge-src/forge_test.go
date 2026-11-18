package main

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestHashObjectVectors(t *testing.T) {
	if got := hashObject("blob", nil); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Fatalf("empty blob: got %s", got)
	}
	if got := hashObject("blob", []byte("hello\n")); got != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("hello blob: got %s", got)
	}
	// Cross-check against the sha1 helper on a commit-shaped payload.
	data := []byte("tree 0000000000000000000000000000000000000000\n")
	header := "commit " + intStr(len(data)) + "\x00"
	if got := hashObject("commit", data); got != sha1Hex(append([]byte(header), data...)) {
		t.Fatalf("commit hash mismatch: got %s", got)
	}
}

func TestIsAncestor(t *testing.T) {
	s := newStore()
	r := s.createRepo(viewerLogin, "t", "", false)
	// c1 <- c2 <- c3 (linear), plus a side branch c2b from c1.
	blob := buildBlob(r, []byte("x\n"))
	tree := buildTreeFromFlat(r, map[string]flatEntry{"f.txt": {oid: blob, mode: "100644"}})
	c1 := buildCommit(r, tree, nil, "c1", fixedEpochSec)
	c2 := buildCommit(r, tree, []string{c1}, "c2", fixedEpochSec)
	c3 := buildCommit(r, tree, []string{c2}, "c3", fixedEpochSec)
	c2b := buildCommit(r, tree, []string{c1}, "c2b", fixedEpochSec)
	if !isAncestor(r, c1, c3) {
		t.Fatal("c1 should be an ancestor of c3")
	}
	if !isAncestor(r, c2, c3) {
		t.Fatal("c2 should be an ancestor of c3")
	}
	if isAncestor(r, c3, c1) {
		t.Fatal("c3 must not be an ancestor of c1")
	}
	if isAncestor(r, c2b, c3) {
		t.Fatal("c2b must not be an ancestor of c3")
	}
	if !isAncestor(r, c1, c1) {
		t.Fatal("a commit is its own ancestor")
	}
	if isAncestor(r, c1, "0000000000000000000000000000000000000000") {
		t.Fatal("zero oid must not be an ancestor")
	}
}

func TestStoreOps(t *testing.T) {
	s := newStore()
	r := s.createRepo(viewerLogin, "repo1", "desc", false)
	if r.dbID != 1 {
		t.Fatalf("dbID: got %d", r.dbID)
	}
	if got := s.getRepo(viewerLogin, "repo1"); got != r {
		t.Fatal("getRepo did not return the created repo")
	}
	if s.getRepo("someone-else", "repo1") != nil {
		t.Fatal("getRepo must be owner-scoped")
	}
	blob := buildBlob(r, []byte("hello\n"))
	if len(blob) != 40 {
		t.Fatalf("blob oid length: %d", len(blob))
	}
	// buildTreeFromFlat + flattenTree round-trip.
	flat := map[string]flatEntry{
		"a.txt":     {oid: blob, mode: "100644"},
		"dir/b.txt": {oid: blob, mode: "100644"},
	}
	treeOID := buildTreeFromFlat(r, flat)
	back := flattenTree(r, treeOID, "")
	if len(back) != 2 || back["a.txt"].oid != blob || back["dir/b.txt"].oid != blob {
		t.Fatalf("flatten round-trip failed: %+v", back)
	}
	// history walks parents in order.
	c1 := buildCommit(r, treeOID, nil, "c1", fixedEpochSec)
	c2 := buildCommit(r, treeOID, []string{c1}, "c2", fixedEpochSec)
	h := history(r, c2, 10)
	if len(h) != 2 || h[0] != c2 || h[1] != c1 {
		t.Fatalf("history: %v", h)
	}
	// mergeBase of c2 and c1 is c1.
	if mb := mergeBase(r, c2, c1); mb != c1 {
		t.Fatalf("mergeBase: got %s want %s", mb, c1)
	}
}

func TestInflateEntrySizes(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	mk := func(n int, kind int) []byte {
		b := make([]byte, n)
		switch kind {
		case 0: // compressible: repeating text
			for i := range b {
				b[i] = byte('a' + (i % 26))
			}
		case 1: // random (incompressible)
			rng.Read(b)
		default: // text with structure
			words := []string{"the ", "quick ", "brown ", "fox "}
			for i := 0; i < n; {
				w := words[rng.Intn(len(words))]
				copy(b[i:], w)
				i += len(w)
			}
		}
		return b
	}
	for _, n := range []int{0, 1, 10, 100, 1000, 4096, 70000} {
		for kind := 0; kind < 3; kind++ {
			data := mk(n, kind)
			oid := hashObject("blob", data)
			pack := writePack([]packObject{{typ: objBlob, oid: oid, data: data}})
			objs, err := parsePack(pack, nil)
			if err != nil {
				t.Fatalf("parsePack n=%d kind=%d: %v", n, kind, err)
			}
			got, ok := objs[oid]
			if !ok {
				t.Fatalf("object %s missing after round-trip (n=%d kind=%d)", oid, n, kind)
			}
			if !bytes.Equal(got.data, data) {
				t.Fatalf("data mismatch after round-trip (n=%d kind=%d)", n, kind)
			}
			// inflateEntry directly on the pack body: the zlib stream
			// starts after the 12-byte pack header + object header varint,
			// and ends right before the 20-byte trailer checksum.
			streamStart := 12 + packHeaderLen(pack, 12)
			dec, end, err := inflateEntry(pack, streamStart, len(pack))
			if err != nil {
				t.Fatalf("inflateEntry n=%d kind=%d: %v", n, kind, err)
			}
			if !bytes.Equal(dec, data) {
				t.Fatalf("inflateEntry data mismatch (n=%d kind=%d)", n, kind)
			}
			if end != len(pack)-20 {
				t.Fatalf("inflateEntry end: got %d want %d (n=%d kind=%d)", end, len(pack)-20, n, kind)
			}
		}
	}
}

// packHeaderLen returns the length of the pack object header (type+size
// varint) starting at off.
func packHeaderLen(buf []byte, off int) int {
	i := off
	for {
		if buf[i]&0x80 == 0 {
			return i - off + 1
		}
		i++
	}
}

func TestApplyDelta(t *testing.T) {
	base := bytes.Repeat([]byte("abcdefgh"), 500)
	derived := append(append([]byte{}, base...), []byte("TAIL")...)
	derived[100] = 'Z'
	// Insert-only delta: base size, target size, then literal runs.
	var d []byte
	d = append(d, encodeVarint(len(base))...)
	d = append(d, encodeVarint(len(derived))...)
	for rest := derived; len(rest) > 0; {
		n := len(rest)
		if n > 127 {
			n = 127
		}
		d = append(d, byte(n))
		d = append(d, rest[:n]...)
		rest = rest[n:]
	}
	out, err := applyDelta(base, d)
	if err != nil {
		t.Fatalf("applyDelta: %v", err)
	}
	if !bytes.Equal(out, derived) {
		t.Fatal("applyDelta produced wrong data")
	}
	// A copy-based delta: copy base[0:100], insert 'Z', copy base[101:4000],
	// insert "TAIL". Copy opcode bits: 0x01/0x02/0x04/0x08 = offset bytes
	// 0-3 present, 0x10/0x20/0x40 = size bytes 0-2 present.
	var d2 []byte
	d2 = append(d2, encodeVarint(len(base))...)
	d2 = append(d2, encodeVarint(len(derived))...)
	d2 = append(d2, 0x90, 0x64)       // copy offset 0, size 100
	d2 = append(d2, 0x01, 'Z')       // insert 'Z'
	d2 = append(d2, 0xB1, 0x65, 0x3B, 0x0F) // copy offset 101, size 3899
	d2 = append(d2, 0x04, 'T', 'A', 'I', 'L') // insert "TAIL"
	out2, err := applyDelta(base, d2)
	if err != nil {
		t.Fatalf("applyDelta (copy): %v", err)
	}
	if !bytes.Equal(out2, derived) {
		t.Fatal("applyDelta (copy) produced wrong data")
	}
}

func encodeVarint(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n == 0 {
			out = append(out, b)
			return out
		}
		out = append(out, b|0x80)
	}
}
