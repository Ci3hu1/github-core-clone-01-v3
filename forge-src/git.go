// Git smart HTTP: pkt-line framing, packfile read/write (no deltas emitted,
// deltas accepted on input), upload-pack v2 + v0, receive-pack v0.
// Only the standard library is used; there is no git binary anywhere.
package main

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"hash/adler32"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ------------------------------------------------------------ pkt-line

func pkt(data []byte) []byte {
	payload := data
	n := len(payload) + 4
	out := make([]byte, 0, n)
	out = append(out, []byte(hex4(n))...)
	out = append(out, payload...)
	return out
}

func pktStr(s string) []byte { return pkt([]byte(s)) }

func hex4(n int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[(n>>12)&0xf], digits[(n>>8)&0xf], digits[(n>>4)&0xf], digits[n&0xf]})
}

func pktFlush() []byte { return []byte("0000") }
func pktDelim() []byte { return []byte("0001") }

type pktItem struct {
	kind byte // 'd' data, 'f' flush, 'l' delim, 'e' end
	data []byte
}

// parsePktLines parses a pkt-line stream from the start of buf. Stops at
// the first flush.
func parsePktLines(buf []byte) (items []pktItem, rest []byte) {
	off := 0
	for off+4 <= len(buf) {
		n, ok := parseHex4(buf[off : off+4])
		if !ok || n < 0 || n > 65520 {
			break
		}
		switch n {
		case 0:
			items = append(items, pktItem{kind: 'f'})
			return items, buf[off+4:]
		case 1:
			items = append(items, pktItem{kind: 'l'})
			off += 4
			continue
		case 2:
			items = append(items, pktItem{kind: 'e'})
			off += 4
			continue
		}
		if off+n > len(buf) {
			break
		}
		items = append(items, pktItem{kind: 'd', data: buf[off+4 : off+n]})
		off += n
	}
	return items, buf[off:]
}

func parseHex4(b []byte) (int, bool) {
	n := 0
	for i := 0; i < 4; i++ {
		c := b[i]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		n = n*16 + d
	}
	return n, true
}

func pktText(items []pktItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.kind == 'd' {
			out = append(out, string(it.data))
		}
	}
	return out
}

// parseAllPktLines parses every pkt-line in buf (across flush boundaries).
func parseAllPktLines(buf []byte) []pktItem {
	items := make([]pktItem, 0, 8)
	off := 0
	for off+4 <= len(buf) {
		n, ok := parseHex4(buf[off : off+4])
		if !ok || n < 0 || n > 65520 {
			break
		}
		switch n {
		case 0:
			items = append(items, pktItem{kind: 'f'})
			off += 4
			continue
		case 1:
			items = append(items, pktItem{kind: 'l'})
			off += 4
			continue
		case 2:
			items = append(items, pktItem{kind: 'e'})
			off += 4
			continue
		}
		if off+n > len(buf) {
			break
		}
		items = append(items, pktItem{kind: 'd', data: buf[off+4 : off+n]})
		off += n
	}
	return items
}

// ------------------------------------------------------------ packfile

// inflateRaw inflates buf[start:end] as one zlib stream; it returns an
// error unless the slice holds exactly one complete, checksummed stream.
func inflateRaw(buf []byte, start, end int) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(buf[start:end]))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// inflateEntry inflates one zlib stream starting at start and finds its
// exact end. One inflate over buf[start:limit] yields the data (and proves
// the stream is well-formed); the stream's own Adler-32 trailer then pins
// the end: the true end is the first offset E whose last four bytes equal
// adler32(data) and whose re-inflate reproduces the data. A truncated
// stream can never pass the re-inflate check, so a false Adler match
// before the true end is impossible, and scanning stops at the true end
// before any later match is reached. Falls back to a binary search when
// no Adler match is found.
func inflateEntry(buf []byte, start, limit int) (data []byte, end int, err error) {
	data, err = inflateRaw(buf, start, limit)
	if err != nil {
		return nil, 0, errBadPack
	}
	want := adler32.Checksum(data)
	// Scan for the Adler trailer. The stream is at least 6 bytes
	// (2 header + 0 deflate + 4 Adler).
	for e := start + 6; e <= limit; e++ {
		if binary.BigEndian.Uint32(buf[e-4:e]) == want {
			if out, err := inflateRaw(buf, start, e); err == nil && bytes.Equal(out, data) {
				return data, e, nil
			}
		}
	}
	// Fallback: binary search for the smallest succeeding end.
	return inflateEntrySync(buf, start, limit, data)
}

var errBadPack = &packError{"bad zlib stream in pack"}

type packError struct{ msg string }

func (e *packError) Error() string { return e.msg }

// inflateEntrySync is the binary-search fallback: "inflates cleanly" is
// monotonic in end (false below the true stream end, true at/above it).
func inflateEntrySync(buf []byte, start, limit int, known []byte) ([]byte, int, error) {
	probe := func(end int) []byte {
		out, err := inflateRaw(buf, start, end)
		if err != nil {
			return nil
		}
		return out
	}
	if probe(limit) == nil {
		return nil, 0, errBadPack
	}
	lo, hi := start+2, limit
	var found []byte
	for lo < hi {
		mid := (lo + hi) / 2
		if out := probe(mid); out != nil {
			found = out
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if found == nil {
		found = probe(lo)
	}
	if found == nil {
		return nil, 0, errBadPack
	}
	return found, lo, nil
}

// applyDelta applies a git delta to a base buffer.
func applyDelta(base, delta []byte) ([]byte, error) {
	off := 0
	readVarint := func() int {
		v := 0
		shift := uint(0)
		for {
			b := delta[off]
			off++
			v |= int(b&0x7f) << shift
			shift += 7
			if b&0x80 == 0 {
				break
			}
		}
		return v
	}
	srcSize := readVarint()
	tgtSize := readVarint()
	if srcSize != len(base) {
		return nil, &packError{"delta base size mismatch"}
	}
	var out []byte
	total := 0
	for off < len(delta) {
		op := delta[off]
		off++
		if op&0x80 != 0 {
			cpOff := 0
			cpSize := 0
			for i := 0; i < 4; i++ {
				if op&(1<<uint(i)) != 0 {
					cpOff |= int(delta[off]) << (8 * uint(i))
					off++
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<uint(i)) != 0 {
					cpSize |= int(delta[off]) << (8 * uint(i))
					off++
				}
			}
			if cpSize == 0 {
				cpSize = 0x10000
			}
			if cpOff+cpSize > len(base) {
				return nil, &packError{"delta copy out of range"}
			}
			out = append(out, base[cpOff:cpOff+cpSize]...)
			total += cpSize
		} else if op > 0 {
			if off+int(op) > len(delta) {
				return nil, &packError{"delta insert out of range"}
			}
			out = append(out, delta[off:off+int(op)]...)
			total += int(op)
			off += int(op)
		} else {
			return nil, &packError{"delta opcode 0"}
		}
	}
	if total != tgtSize {
		return nil, &packError{"delta target size mismatch"}
	}
	return out, nil
}

type packObject struct {
	typ  objType
	data []byte
	oid  string
}

// parsePack parses a packfile; resolves OFS/REF deltas; verifies the
// trailer checksum. external resolves thin-pack REF_DELTA bases that live
// in the server's object store rather than in the pack itself.
func parsePack(buf []byte, external func(oid string) *gitObject) (map[string]*packObject, error) {
	if len(buf) < 32 || string(buf[0:4]) != "PACK" {
		return nil, &packError{"not a packfile"}
	}
	version := binary.BigEndian.Uint32(buf[4:8])
	if version != 2 && version != 3 {
		return nil, &packError{"unsupported pack version"}
	}
	count := int(binary.BigEndian.Uint32(buf[8:12]))
	trailer := buf[len(buf)-20:]
	sum := sha1.Sum(buf[:len(buf)-20])
	if !bytes.Equal(sum[:], trailer) {
		return nil, &packError{"pack checksum mismatch"}
	}
	byOffset := make(map[int]*packObject)
	byOID := make(map[string]*packObject)
	type pending struct {
		offset     int
		data       []byte
		baseOID    string
		baseOffset int
		hasBaseOff bool
	}
	pendingList := make([]pending, 0, 4)
	off := 12
	limit := len(buf) - 20
	typeName := map[int]objType{1: objCommit, 2: objTree, 3: objBlob, 4: objTag}
	for n := 0; n < count; n++ {
		entryStart := off
		first := buf[off]
		typeCode := int((first >> 4) & 0x07)
		size := int(first & 0x0f)
		shift := uint(4)
		off++
		for buf[off-1]&0x80 != 0 {
			size |= int(buf[off]&0x7f) << shift
			shift += 7
			off++
		}
		var baseOID string
		baseOffset := -1
		if typeCode == 6 {
			b := buf[off]
			off++
			offset := int(b & 0x7f)
			for b&0x80 != 0 {
				b = buf[off]
				off++
				offset = ((offset + 1) << 7) | int(b&0x7f)
			}
			baseOffset = entryStart - offset
		} else if typeCode == 7 {
			baseOID = hex.EncodeToString(buf[off : off+20])
			off += 20
		}
		data, end, err := inflateEntry(buf, off, limit)
		if err != nil {
			return nil, err
		}
		off = end
		if len(data) != size {
			return nil, &packError{"pack entry size mismatch"}
		}
		if typeCode >= 1 && typeCode <= 4 {
			typ := typeName[typeCode]
			oid := hashObject(string(typ), data)
			obj := &packObject{typ: typ, data: data, oid: oid}
			byOffset[entryStart] = obj
			byOID[oid] = obj
		} else if typeCode == 6 || typeCode == 7 {
			pendingList = append(pendingList, pending{offset: entryStart, data: data, baseOID: baseOID, baseOffset: baseOffset, hasBaseOff: typeCode == 6})
		} else {
			return nil, &packError{"unknown pack entry type " + strconv.Itoa(typeCode)}
		}
	}
	// Resolve deltas (possibly chained): iterate until none remain.
	guard := 0
	for len(pendingList) > 0 {
		guard++
		if guard > count+5 {
			return nil, &packError{"delta cycle"}
		}
		item := pendingList[0]
		pendingList = pendingList[1:]
		var base *packObject
		if item.hasBaseOff {
			base = byOffset[item.baseOffset]
		} else {
			base = byOID[item.baseOID]
			if base == nil && external != nil {
				if ext := external(item.baseOID); ext != nil {
					base = &packObject{typ: ext.typ, data: ext.data, oid: item.baseOID}
				}
			}
		}
		if base == nil {
			pendingList = append(pendingList, item)
			continue
		}
		resolved, err := applyDelta(base.data, item.data)
		if err != nil {
			return nil, err
		}
		oid := hashObject(string(base.typ), resolved)
		obj := &packObject{typ: base.typ, data: resolved, oid: oid}
		byOffset[item.offset] = obj
		byOID[oid] = obj
	}
	return byOID, nil
}

// writePack writes a packfile (version 2, no deltas) from objects in the
// given order.
func writePack(objects []packObject) []byte {
	var body bytes.Buffer
	header := make([]byte, 12)
	copy(header[0:4], "PACK")
	binary.BigEndian.PutUint32(header[4:8], 2)
	binary.BigEndian.PutUint32(header[8:12], uint32(len(objects)))
	body.Write(header)
	typeCode := map[objType]int{objCommit: 1, objTree: 2, objBlob: 3, objTag: 4}
	for _, obj := range objects {
		code := typeCode[obj.typ]
		size := len(obj.data)
		head := []byte{byte((code << 4) | (size & 0x0f))}
		s := size >> 4
		for s > 0 {
			head[len(head)-1] |= 0x80
			head = append(head, byte(s&0x7f))
			s >>= 7
		}
		body.Write(head)
		zw, _ := zlib.NewWriterLevel(&body, flate.BestSpeed)
		zw.Write(obj.data)
		zw.Close()
	}
	out := body.Bytes()
	sum := sha1.Sum(out)
	out = append(out, sum[:]...)
	return out
}

// ------------------------------------------------------------ ref helpers

type refLine struct {
	ref string
	oid string
}

func refLines(r *repo) []refLine {
	out := make([]refLine, 0, len(r.refs))
	for ref, oid := range r.refs {
		out = append(out, refLine{ref: ref, oid: oid})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	return out
}

func headTarget(r *repo) string {
	return "refs/heads/" + r.defaultBranch
}

// peelTag resolves an annotated tag chain to its ultimate target.
func peelTag(r *repo, oid string) (string, objType) {
	cur := oid
	for i := 0; i < 10; i++ {
		obj := r.objects[cur]
		if obj == nil || obj.typ != objTag {
			if obj == nil {
				return cur, objCommit
			}
			return cur, obj.typ
		}
		text := string(obj.data)
		// object <oid>\ntype <type>
		if !strings.HasPrefix(text, "object ") {
			return cur, objTag
		}
		rest := text[7:]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return cur, objTag
		}
		typLine := rest[nl+1:]
		typ := objCommit
		if strings.HasPrefix(typLine, "type ") {
			t := typLine[5:]
			if i2 := strings.IndexByte(t, '\n'); i2 >= 0 {
				t = t[:i2]
			}
			typ = objType(t)
		}
		_ = typ
		cur = rest[:nl]
	}
	return cur, objCommit
}

var uploadCapsV2 = []string{"agent=forge/1.0", "ls-refs=unborn", "fetch=shallow", "object-format=sha1"}

const uploadCapsV0 = "side-band-64k thin-pack ofs-delta multi_ack_detailed shallow agent=forge/1.0 symref=HEAD:refs/heads/main object-format=sha1"
const receiveCapsV0 = "report-status side-band-64k delete-refs ofs-delta agent=forge/1.0 symref=HEAD:refs/heads/main"

func advertisementV2() []byte {
	var out []byte
	out = append(out, pktStr("version 2\n")...)
	for _, cap := range uploadCapsV2 {
		out = append(out, pktStr(cap+"\n")...)
	}
	out = append(out, pktFlush()...)
	return out
}

func advertisementV0(r *repo, service string) []byte {
	caps := uploadCapsV0
	if service == "git-receive-pack" {
		caps = receiveCapsV0
	}
	var out []byte
	out = append(out, pktStr("# service="+service+"\n")...)
	out = append(out, pktFlush()...)
	refs := refLines(r)
	head := headTarget(r)
	headOID, hasHead := r.refs[head]
	if len(refs) == 0 {
		out = append(out, pktStr(zeroOID+" capabilities^{}\x00"+caps+"\n")...)
	} else {
		first := true
		emit := func(oid, ref string) {
			if first {
				out = append(out, pktStr(oid+" "+ref+"\x00"+caps+"\n")...)
				first = false
			} else {
				out = append(out, pktStr(oid+" "+ref+"\n")...)
			}
		}
		if hasHead {
			emit(headOID, "HEAD")
		}
		for _, rl := range refs {
			emit(rl.oid, rl.ref)
			if obj := r.objects[rl.oid]; obj != nil && obj.typ == objTag {
				peeled, _ := peelTag(r, rl.oid)
				out = append(out, pktStr(peeled+" "+rl.ref+"^{}\n")...)
			}
		}
	}
	out = append(out, pktFlush()...)
	return out
}

// ------------------------------------------------------------ fetch computation

type fetchPlan struct {
	toSend    []packObject
	shallow   []string
	unshallow []string
	acks      []string
}

// haveObjects: objects the client already has, given its have tips and its
// current shallow boundaries.
func haveObjects(r *repo, haves []string, shallowBoundaries map[string]bool) map[string]bool {
	seen := make(map[string]bool)
	stack := append([]string{}, haves...)
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
			if !shallowBoundaries[oid] {
				stack = append(stack, c.parents...)
			}
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

// planFetch computes the objects a fetch must send, with depth limiting.
func planFetch(r *repo, wants, haves, priorShallow []string, deepen *int) fetchPlan {
	shallowSet := make(map[string]bool, len(priorShallow))
	for _, s := range priorShallow {
		shallowSet[s] = true
	}
	haveReachable := haveObjects(r, haves, shallowSet)
	included := make(map[string]bool) // commit oids included
	boundary := make(map[string]bool)
	if deepen == nil {
		for _, w := range wants {
			for c := range reachable(r, []string{w}) {
				if obj := r.objects[c]; obj != nil && obj.typ == objCommit {
					included[c] = true
				}
			}
		}
	} else {
		// BFS by parent distance.
		dist := make(map[string]int)
		queue := make([]string, 0, 8)
		for _, w := range wants {
			if _, ok := dist[w]; !ok {
				dist[w] = 0
				queue = append(queue, w)
			}
		}
		for head := 0; head < len(queue); head++ {
			oid := queue[head]
			d := dist[oid]
			if d >= *deepen {
				continue
			}
			obj := r.objects[oid]
			if obj == nil || obj.typ != objCommit {
				continue
			}
			included[oid] = true
			for _, p := range parseCommit(obj.data).parents {
				if _, ok := dist[p]; !ok {
					dist[p] = d + 1
					queue = append(queue, p)
				}
			}
		}
		for oid := range included {
			obj := r.objects[oid]
			if obj == nil || obj.typ != objCommit {
				continue
			}
			for _, p := range parseCommit(obj.data).parents {
				if !included[p] {
					boundary[oid] = true
					break
				}
			}
		}
	}
	var toSendIDs []string
	if deepen == nil {
		reachIncluded := reachable(r, keysOf(included))
		for oid := range reachIncluded {
			if !haveReachable[oid] {
				toSendIDs = append(toSendIDs, oid)
			}
		}
	} else {
		set := make(map[string]bool)
		for c := range included {
			set[c] = true
			obj := r.objects[c]
			if obj == nil || obj.typ != objCommit {
				continue
			}
			stack := []string{parseCommit(obj.data).tree}
			for len(stack) > 0 {
				t := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if set[t] {
					continue
				}
				tobj := r.objects[t]
				if tobj == nil {
					continue
				}
				set[t] = true
				if tobj.typ == objTree {
					for _, e := range parseTree(tobj.data) {
						stack = append(stack, e.oid)
					}
				}
			}
		}
		for oid := range set {
			if !haveReachable[oid] {
				toSendIDs = append(toSendIDs, oid)
			}
		}
	}
	// The wanted tips themselves always travel.
	for _, w := range wants {
		if _, ok := r.objects[w]; ok && !haveReachable[w] {
			found := false
			for _, id := range toSendIDs {
				if id == w {
					found = true
					break
				}
			}
			if !found {
				toSendIDs = append(toSendIDs, w)
			}
		}
	}
	// Order: commits, then trees, then blobs, then tags (stable).
	order := map[objType]int{objCommit: 0, objTree: 1, objBlob: 2, objTag: 3}
	sort.Slice(toSendIDs, func(i, j int) bool {
		ta := objBlob
		if o := r.objects[toSendIDs[i]]; o != nil {
			ta = o.typ
		}
		tb := objBlob
		if o := r.objects[toSendIDs[j]]; o != nil {
			tb = o.typ
		}
		if order[ta] != order[tb] {
			return order[ta] < order[tb]
		}
		return toSendIDs[i] < toSendIDs[j]
	})
	toSend := make([]packObject, 0, len(toSendIDs))
	for _, oid := range toSendIDs {
		if o := r.objects[oid]; o != nil {
			toSend = append(toSend, packObject{typ: o.typ, data: o.data, oid: oid})
		}
	}
	shallow := make([]string, 0, len(boundary))
	for s := range boundary {
		shallow = append(shallow, s)
	}
	sortStrings(shallow)
	var unshallow []string
	for _, s := range priorShallow {
		if included[s] && !boundary[s] {
			unshallow = append(unshallow, s)
		}
	}
	sortStrings(unshallow)
	var acks []string
	if len(haves) > 0 {
		wantReach := reachable(r, wants)
		for _, h := range haves {
			if wantReach[h] {
				acks = append(acks, h)
			}
		}
	}
	return fetchPlan{toSend: toSend, shallow: shallow, unshallow: unshallow, acks: acks}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// sidebandPack splits pack bytes into sideband band-1 pkt-lines.
func sidebandPack(pack []byte) [][]byte {
	var out [][]byte
	off := 0
	for off < len(pack) {
		end := off + 65515
		if end > len(pack) {
			end = len(pack)
		}
		chunk := make([]byte, 0, end-off+1)
		chunk = append(chunk, 1)
		chunk = append(chunk, pack[off:end]...)
		out = append(out, pkt(chunk))
		off = end
	}
	if len(out) == 0 {
		out = append(out, pkt([]byte{1}))
	}
	return out
}

// ------------------------------------------------------------ upload-pack v2

type v2FetchArgs struct {
	wants    []string
	haves    []string
	shallow  []string
	deepen   *int
	thinPack bool
}

func parseV2Fetch(lines []string) v2FetchArgs {
	var args v2FetchArgs
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "want "):
			args.wants = append(args.wants, strings.TrimSpace(t[5:]))
		case strings.HasPrefix(t, "have "):
			args.haves = append(args.haves, strings.TrimSpace(t[5:]))
		case strings.HasPrefix(t, "shallow "):
			args.shallow = append(args.shallow, strings.TrimSpace(t[8:]))
		case strings.HasPrefix(t, "deepen "):
			if n, err := strconv.Atoi(strings.TrimSpace(t[7:])); err == nil {
				args.deepen = &n
			}
		case t == "thin-pack":
			args.thinPack = true
		}
	}
	return args
}

func handleUploadPackV2Fetch(r *repo, lines []string) []byte {
	args := parseV2Fetch(lines)
	plan := planFetch(r, args.wants, args.haves, args.shallow, args.deepen)
	pack := writePack(plan.toSend)
	var parts [][]byte
	parts = append(parts, pktStr("acknowledgments\n"))
	if len(plan.acks) > 0 {
		for _, a := range plan.acks {
			parts = append(parts, pktStr("ACK "+a+"\n"))
		}
		parts = append(parts, pktStr("ready\n"))
	} else {
		parts = append(parts, pktStr("NAK\n"))
	}
	parts = append(parts, pktDelim())
	parts = append(parts, pktStr("shallow-info\n"))
	for _, s := range plan.shallow {
		parts = append(parts, pktStr("shallow "+s+"\n"))
	}
	for _, s := range plan.unshallow {
		parts = append(parts, pktStr("unshallow "+s+"\n"))
	}
	parts = append(parts, pktDelim())
	parts = append(parts, pktStr("wanted-refs\n"))
	for _, w := range args.wants {
		for _, rl := range refLines(r) {
			if rl.oid == w {
				parts = append(parts, pktStr(w+" "+rl.ref+"\n"))
			}
		}
	}
	parts = append(parts, pktDelim())
	parts = append(parts, pktStr("packfile\n"))
	parts = append(parts, sidebandPack(pack)...)
	parts = append(parts, pktFlush())
	return joinParts(parts)
}

func joinParts(parts [][]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func handleUploadPackV2LsRefs(r *repo, lines []string) []byte {
	peel := false
	symrefs := false
	var prefixes []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case t == "peel":
			peel = true
		case t == "symrefs":
			symrefs = true
		case t == "unborn":
		case strings.HasPrefix(t, "ref-prefix "):
			prefixes = append(prefixes, t[11:])
		}
	}
	var parts [][]byte
	head := headTarget(r)
	headOID, hasHead := r.refs[head]
	emitRef := func(oid, ref string) {
		line := oid + " " + ref
		if symrefs && ref == "HEAD" {
			line += " symref-target:" + head
		}
		if peel {
			if obj := r.objects[oid]; obj != nil && obj.typ == objTag {
				peeled, _ := peelTag(r, oid)
				line += " peeled:" + peeled
			}
		}
		parts = append(parts, pktStr(line+"\n"))
	}
	matches := func(ref string) bool {
		if len(prefixes) == 0 {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(ref, p) {
				return true
			}
		}
		return false
	}
	if len(prefixes) == 0 && hasHead {
		emitRef(headOID, "HEAD")
	}
	for _, rl := range refLines(r) {
		if !matches(rl.ref) {
			continue
		}
		emitRef(rl.oid, rl.ref)
	}
	parts = append(parts, pktFlush())
	return joinParts(parts)
}

// ------------------------------------------------------------ upload-pack v0

func handleUploadPackV0(r *repo, body []byte) []byte {
	items := parseAllPktLines(body)
	lines := pktText(items)
	var wants, haves, shallow []string
	var deepen *int
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "want "):
			wants = append(wants, strings.Fields(t[5:])[0])
		case strings.HasPrefix(t, "have "):
			haves = append(haves, strings.TrimSpace(t[5:]))
		case strings.HasPrefix(t, "shallow "):
			shallow = append(shallow, strings.TrimSpace(t[8:]))
		case strings.HasPrefix(t, "deepen "):
			if n, err := strconv.Atoi(strings.TrimSpace(t[7:])); err == nil {
				deepen = &n
			}
		}
	}
	plan := planFetch(r, wants, haves, shallow, deepen)
	pack := writePack(plan.toSend)
	var parts [][]byte
	if len(plan.acks) > 0 {
		for _, a := range plan.acks {
			parts = append(parts, pktStr("ACK "+a+"\n"))
		}
	}
	parts = append(parts, pktStr("NAK\n"))
	for _, s := range plan.shallow {
		parts = append(parts, pktStr("shallow "+s+"\n"))
	}
	for _, s := range plan.unshallow {
		parts = append(parts, pktStr("unshallow "+s+"\n"))
	}
	parts = append(parts, sidebandPack(pack)...)
	parts = append(parts, pktFlush())
	return joinParts(parts)
}

// ------------------------------------------------------------ receive-pack

type pushCommand struct {
	oldOID string
	newOID string
	ref    string
}

func parseReceiveCommands(body []byte) (commands []pushCommand, packStart int) {
	items, rest := parsePktLines(body)
	for _, item := range items {
		if item.kind != 'd' {
			continue
		}
		text := string(item.data)
		if nul := strings.IndexByte(text, 0); nul >= 0 {
			text = text[:nul]
		}
		parts := strings.Fields(strings.TrimSpace(text))
		if len(parts) >= 3 {
			commands = append(commands, pushCommand{oldOID: parts[0], newOID: parts[1], ref: parts[2]})
		}
	}
	packStart = len(body) - len(rest)
	return commands, packStart
}

// applyPush applies a push: unpacks the pack, then applies each ref command.
func applyPush(r *repo, body []byte) (lines []string, ok bool) {
	commands, packStart := parseReceiveCommands(body)
	needsPack := false
	for _, c := range commands {
		if c.newOID != zeroOID {
			needsPack = true
			break
		}
	}
	if needsPack {
		packBytes := body[packStart:]
		if len(packBytes) == 0 {
			return []string{"unpack error"}, false
		}
		objects, err := parsePack(packBytes, func(oid string) *gitObject {
			return r.objects[oid]
		})
		if err != nil {
			return []string{"unpack error"}, false
		}
		for oid, obj := range objects {
			if _, exists := r.objects[oid]; !exists {
				r.objects[oid] = &gitObject{typ: obj.typ, data: obj.data}
			}
		}
	}
	lines = []string{"unpack ok"}
	for _, cmd := range commands {
		current, exists := r.refs[cmd.ref]
		if cmd.newOID == zeroOID {
			// Delete.
			switch {
			case !exists:
				lines = append(lines, "ng "+cmd.ref+" ref-not-found")
			case cmd.oldOID != zeroOID && cmd.oldOID != current:
				lines = append(lines, "ng "+cmd.ref+" stale")
			default:
				delete(r.refs, cmd.ref)
				lines = append(lines, "ok "+cmd.ref)
			}
			continue
		}
		if cmd.oldOID == zeroOID {
			if exists {
				lines = append(lines, "ng "+cmd.ref+" already-exists")
				continue
			}
		} else {
			if !exists {
				lines = append(lines, "ng "+cmd.ref+" stale")
				continue
			}
			if current != cmd.oldOID {
				lines = append(lines, "ng "+cmd.ref+" stale")
				continue
			}
			if !isAncestor(r, cmd.oldOID, cmd.newOID) {
				lines = append(lines, "ng "+cmd.ref+" non-fast-forward")
				continue
			}
		}
		r.refs[cmd.ref] = cmd.newOID
		lines = append(lines, "ok "+cmd.ref)
	}
	return lines, true
}

// pushReportSideband frames each report line as its own pkt-line inside
// band-1 sideband data (the git CLI's report parser reads those pkt-line
// lengths; the harness accepts both framed and unframed text).
func pushReportSideband(lines []string) []byte {
	var framed []byte
	for _, l := range lines {
		framed = append(framed, pktStr(l+"\n")...)
	}
	parts := sidebandPack(framed)
	parts = append(parts, pktFlush())
	return joinParts(parts)
}

// ------------------------------------------------------------ HTTP dispatch

type gitResult struct {
	status      int
	body        []byte
	contentType string
}

func splitV2Sections(items []pktItem) (command string, lines []string) {
	for _, item := range items {
		if item.kind != 'd' {
			continue
		}
		text := strings.TrimSpace(string(item.data))
		if strings.HasPrefix(text, "command=") {
			command = strings.TrimSpace(text[8:])
		} else {
			lines = append(lines, text)
		}
	}
	return command, lines
}

// handleGit dispatches one smart-HTTP git request. 404 when the repo is unknown.
func handleGit(s *store, owner, repoName, sub, method string, query map[string]string, body []byte) gitResult {
	notFound := gitResult{status: 404, body: []byte("Not Found"), contentType: "text/plain"}
	if owner != viewerLogin {
		return notFound
	}
	r := s.getRepo(owner, repoName)
	if r == nil {
		return notFound
	}
	if method == "GET" && sub == "info/refs" {
		service := query["service"]
		if service == "git-upload-pack" {
			return gitResult{status: 200, body: advertisementV2(), contentType: "application/x-git-upload-pack-advertisement"}
		}
		if service == "git-receive-pack" {
			return gitResult{status: 200, body: advertisementV0(r, "git-receive-pack"), contentType: "application/x-git-receive-pack-advertisement"}
		}
		return notFound
	}
	if method == "POST" && sub == "git-upload-pack" {
		items := parseAllPktLines(body)
		var firstText string
		for _, it := range items {
			if it.kind == 'd' {
				firstText = strings.TrimSpace(string(it.data))
				break
			}
		}
		if strings.HasPrefix(firstText, "command=") {
			command, lines := splitV2Sections(items)
			switch command {
			case "ls-refs":
				return gitResult{status: 200, body: handleUploadPackV2LsRefs(r, lines), contentType: "application/x-git-upload-pack-result"}
			case "fetch":
				return gitResult{status: 200, body: handleUploadPackV2Fetch(r, lines), contentType: "application/x-git-upload-pack-result"}
			}
			return notFound
		}
		return gitResult{status: 200, body: handleUploadPackV0(r, body), contentType: "application/x-git-upload-pack-result"}
	}
	if method == "POST" && sub == "git-receive-pack" {
		lines, _ := applyPush(r, body)
		return gitResult{status: 200, body: pushReportSideband(lines), contentType: "application/x-git-receive-pack-result"}
	}
	return notFound
}
