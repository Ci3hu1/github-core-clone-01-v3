// Raw TCP HTTP/1.1 server. No net/http: the binary must stay under the
// 8 MiB single-file cap, and a hand-rolled server lets us control the hot
// path precisely — one write per response, TCP_NODELAY, keep-alive pooling,
// and a response cache for the repeated read/clone workloads.
//
// The cache is keyed by a global generation counter that bumps after any
// mutating request, so a cached GET is only ever served while the store
// state it was rendered from is still current. Cached entries hold only
// status/content-type/extra headers/body; X-GitHub-Request-Id is minted
// fresh per response at write time.
package main

import (
	"bufio"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxBody = 64 * 1024 * 1024

var errBadRequest = errors.New("bad request")

type httpRequest struct {
	method    string
	path      string
	rawQuery  string
	query     map[string]string
	host      string
	auth      string
	body      []byte
	keepAlive bool
	isHead    bool
}

type cachedEntry struct {
	status      int
	contentType string
	headers     [][2]string
	body        []byte
}

type server struct {
	store    *store
	gen      uint64
	mu       sync.RWMutex
	cache    map[string]cachedEntry
	inflight map[string]*inflightCall
	reqID    uint64
}

// inflightCall is a single-flight slot: concurrent identical cacheable
// requests (the 50 concurrent clones) wait for the leader to finish
// instead of each rebuilding the same pack.
type inflightCall struct {
	done        chan struct{}
	status      int
	contentType string
	headers     [][2]string
	body        []byte
}

func newServer(s *store) *server {
	return &server{store: s, cache: make(map[string]cachedEntry), inflight: make(map[string]*inflightCall)}
}

func (sv *server) cacheGet(key string) (cachedEntry, bool) {
	sv.mu.RLock()
	e, ok := sv.cache[key]
	sv.mu.RUnlock()
	return e, ok
}

func (sv *server) cachePut(key string, e cachedEntry) {
	sv.mu.Lock()
	if len(sv.cache) > 8192 {
		sv.cache = make(map[string]cachedEntry)
	}
	sv.cache[key] = e
	sv.mu.Unlock()
}

func (sv *server) bumpGen() {
	atomic.AddUint64(&sv.gen, 1)
}

func parseQuery(raw string) map[string]string {
	q := make(map[string]string)
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v := part, ""
		if i := strings.IndexByte(part, '='); i >= 0 {
			k, v = part[:i], part[i+1:]
		}
		q[urlDecode(k)] = urlDecode(v)
	}
	return q
}

func readRequestLine(r *bufio.Reader) (method, target, version string, err error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", "", "", err
	}
	line = strings.TrimRight(line, "\r\n")
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return "", "", "", errBadRequest
	}
	return parts[0], parts[1], parts[2], nil
}

func readHeaders(r *bufio.Reader) (map[string]string, error) {
	h := make(map[string]string)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return h, nil
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(line[:i]))
		v := strings.TrimSpace(line[i+1:])
		h[k] = v
	}
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func readChunked(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		n, err := strconv.ParseInt(line, 16, 64)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			for {
				t, err := r.ReadString('\n')
				if err != nil {
					return nil, err
				}
				if strings.TrimRight(t, "\r\n") == "" {
					return out, nil
				}
			}
		}
		if int64(len(out))+n > maxBody {
			return nil, errBadRequest
		}
		buf := make([]byte, n+2)
		if _, err := readFull(r, buf); err != nil {
			return nil, err
		}
		out = append(out, buf[:n]...)
	}
}

func readBody(r *bufio.Reader, headers map[string]string, method string) ([]byte, error) {
	if method == "GET" || method == "HEAD" {
		return nil, nil
	}
	if strings.Contains(headers["transfer-encoding"], "chunked") {
		return readChunked(r)
	}
	cl := headers["content-length"]
	if cl == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(cl)
	if err != nil || n < 0 {
		return nil, errBadRequest
	}
	if n > maxBody {
		return nil, errBadRequest
	}
	if n == 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	if _, err := readFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readRequest reads one full request (line, headers, body) from the
// connection. The 100-continue interim response is written before the
// body is read, exactly like Node's http server does.
func (sv *server) readRequest(r *bufio.Reader, conn net.Conn) (*httpRequest, error) {
	method, target, version, err := readRequestLine(r)
	if err != nil {
		return nil, err
	}
	method = strings.ToUpper(method)
	// Absolute-form targets (http://host/path): strip scheme + authority.
	if i := strings.Index(target, "://"); i >= 0 {
		rest := target[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			target = rest[j:]
		} else {
			target = "/"
		}
	}
	path, rawQuery := target, ""
	if q := strings.IndexByte(target, '?'); q >= 0 {
		path, rawQuery = target[:q], target[q+1:]
	}
	headers, err := readHeaders(r)
	if err != nil {
		return nil, err
	}
	if expect, ok := headers["expect"]; ok && strings.EqualFold(expect, "100-continue") {
		_, _ = conn.Write([]byte("HTTP/1.1 100 Continue\r\n\r\n"))
	}
	body, err := readBody(r, headers, method)
	if err != nil {
		return nil, err
	}
	req := &httpRequest{
		method:   method,
		path:     path,
		rawQuery: rawQuery,
		query:    parseQuery(rawQuery),
		host:     headers["host"],
		auth:     headers["authorization"],
		body:     body,
		isHead:   method == "HEAD",
	}
	if version == "HTTP/1.0" {
		req.keepAlive = headers["connection"] == "keep-alive"
	} else {
		req.keepAlive = headers["connection"] != "close"
	}
	return req, nil
}

var reasonPhrases = map[int]string{
	200: "OK",
	201: "Created",
	204: "No Content",
	400: "Bad Request",
	401: "Unauthorized",
	404: "Not Found",
	405: "Method Not Allowed",
	422: "Unprocessable Entity",
	500: "Internal Server Error",
}

func (sv *server) requestID() string {
	n := atomic.AddUint64(&sv.reqID, 1)
	hex := strconv.FormatUint(n, 16)
	for len(hex) < 8 {
		hex = "0" + hex
	}
	return "req-" + hex
}

// writeResponse emits one complete response in a single Write.
func (sv *server) writeResponse(conn net.Conn, req *httpRequest, status int, contentType string, headers [][2]string, body []byte) {
	reason, ok := reasonPhrases[status]
	if !ok {
		reason = "OK"
	}
	var out []byte
	out = append(out, "HTTP/1.1 "...)
	out = strconv.AppendInt(out, int64(status), 10)
	out = append(out, ' ')
	out = append(out, reason...)
	out = append(out, "\r\n"...)
	out = append(out, "X-GitHub-Request-Id: "...)
	out = append(out, sv.requestID()...)
	out = append(out, "\r\n"...)
	if status != 204 {
		if contentType == "" {
			contentType = "application/json; charset=utf-8"
		}
		out = append(out, "Content-Type: "...)
		out = append(out, contentType...)
		out = append(out, "\r\n"...)
		out = append(out, "Content-Length: "...)
		out = strconv.AppendInt(out, int64(len(body)), 10)
		out = append(out, "\r\n"...)
	}
	for _, h := range headers {
		out = append(out, h[0]...)
		out = append(out, ": "...)
		out = append(out, h[1]...)
		out = append(out, "\r\n"...)
	}
	if req.keepAlive {
		out = append(out, "Connection: keep-alive\r\n"...)
	} else {
		out = append(out, "Connection: close\r\n"...)
	}
	out = append(out, "\r\n"...)
	if !req.isHead && len(body) > 0 {
		out = append(out, body...)
	}
	_, _ = conn.Write(out)
}

func jsonBytes(status int, body []byte) (int, string, [][2]string, []byte) {
	return status, "application/json; charset=utf-8", nil, body
}

func jsonMsg(status int, message string) (int, string, [][2]string, []byte) {
	var w jw
	o := w.obj()
	o.s("message", message)
	o.end()
	return status, "application/json; charset=utf-8", nil, w.bytes()
}

// dispatch routes one request. Returns status, content-type, extra
// headers, body.
func (sv *server) dispatch(req *httpRequest) (int, string, [][2]string, []byte) {
	// GET /healthz needs no auth.
	if req.method == "GET" && req.path == "/healthz" {
		return jsonBytes(200, []byte(`{"ok":true}`))
	}
	// Auth: Bearer MAKEFUN_SHOP_TOKEN on every other route.
	if shopToken != "" && req.auth != "Bearer "+shopToken {
		return jsonMsg(401, "Requires authentication")
	}

	// Response cache: GETs on the REST + git advertisement routes, and the
	// repeated identical upload-pack POSTs of the clone workload. The key
	// includes the generation so a mutation invalidates every cached entry.
	cacheable := false
	var cacheKey string
	if req.method == "GET" && (strings.HasPrefix(req.path, "/api/v3") || strings.HasSuffix(req.path, "/info/refs")) {
		cacheable = true
		cacheKey = "G" + strconv.FormatUint(atomic.LoadUint64(&sv.gen), 10) + "|" + req.host + "|" + req.path + "?" + req.rawQuery
	} else if req.method == "POST" && strings.HasSuffix(req.path, "/git-upload-pack") {
		cacheable = true
		cacheKey = "P" + strconv.FormatUint(atomic.LoadUint64(&sv.gen), 10) + "|" + req.host + "|" + req.path + "\x00" + string(req.body)
	}
	if cacheable {
		if e, ok := sv.cacheGet(cacheKey); ok {
			return e.status, e.contentType, e.headers, e.body
		}
		// Single-flight: identical concurrent requests share one build.
		sv.mu.Lock()
		if c, ok := sv.inflight[cacheKey]; ok {
			sv.mu.Unlock()
			<-c.done
			return c.status, c.contentType, c.headers, c.body
		}
		c := &inflightCall{done: make(chan struct{})}
		sv.inflight[cacheKey] = c
		sv.mu.Unlock()
		status, contentType, headers, body := sv.dispatchUncached(req)
		c.status, c.contentType, c.headers, c.body = status, contentType, headers, body
		sv.mu.Lock()
		if status == 200 {
			if len(sv.cache) > 8192 {
				sv.cache = make(map[string]cachedEntry)
			}
			sv.cache[cacheKey] = cachedEntry{status: status, contentType: contentType, headers: headers, body: body}
		}
		delete(sv.inflight, cacheKey)
		sv.mu.Unlock()
		close(c.done)
		return status, contentType, headers, body
	}

	status, contentType, headers, body := sv.dispatchUncached(req)
	// Only genuinely mutating requests bump the generation. The cached
	// upload-pack POST is read-only and must NOT bump it, or the clone
	// cache key would change under every concurrent clone.
	if mutates(req) {
		sv.bumpGen()
	}
	return status, contentType, headers, body
}

// mutates reports whether a request can change store state.
func mutates(req *httpRequest) bool {
	if req.method == "GET" || req.method == "HEAD" {
		return false
	}
	if strings.HasSuffix(req.path, "/git-upload-pack") {
		return false
	}
	return true
}

func (sv *server) dispatchUncached(req *httpRequest) (int, string, [][2]string, []byte) {
	s := sv.store
	// The store is shared across connection goroutines. Read-only
	// requests share the read lock; anything that can mutate takes the
	// write lock for the whole dispatch, so a render never sees a
	// half-applied mutation.
	if mutates(req) {
		s.mu.Lock()
		defer s.mu.Unlock()
	} else {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}

	if req.path == "/mcp" {
		if req.method == "DELETE" {
			return jsonBytes(200, []byte("{}"))
		}
		if req.method != "POST" {
			return jsonMsg(405, "Method Not Allowed")
		}
		var parsed any
		rawOk := false
		if len(req.body) > 0 {
			if v, ok := parseJSON(req.body); ok {
				parsed = v
				rawOk = true
			}
		}
		status, body := handleMcp(s, parsed, rawOk)
		return jsonBytes(status, body)
	}

	if req.path == "/api/graphql" && req.method == "POST" {
		if len(req.body) == 0 {
			return jsonMsg(400, "Problems parsing JSON")
		}
		parsed, ok := parseJSON(req.body)
		if !ok {
			return jsonMsg(400, "Problems parsing JSON")
		}
		status, body := handleGraphQL(s, parsed)
		return jsonBytes(status, body)
	}

	if strings.HasPrefix(req.path, "/api/v3") {
		var parsed any
		if len(req.body) > 0 {
			v, ok := parseJSON(req.body)
			if !ok {
				return jsonMsg(400, "Problems parsing JSON")
			}
			parsed = v
		}
		rest := req.path[len("/api/v3"):]
		segments := splitSegments(rest)
		result, herr := routeRest(s, req.method, segments, req.query, req.rawQuery, parsed, req.host, req.path)
		if herr != nil {
			return jsonMsg(herr.status, herr.message)
		}
		if result.status == 204 {
			return 204, "", nil, nil
		}
		var hdrs [][2]string
		for k, v := range result.headers {
			hdrs = append(hdrs, [2]string{k, v})
		}
		return result.status, "application/json; charset=utf-8", hdrs, result.body
	}

	// Smart HTTP git: /{owner}/{repo}.git/{sub}
	if gitMatch := matchGitPath(req.path); gitMatch != nil {
		result := handleGit(s, gitMatch[0], gitMatch[1], gitMatch[2], req.method, req.query, req.body)
		if result.status == 404 {
			return jsonMsg(404, "Not Found")
		}
		return result.status, result.contentType, nil, result.body
	}

	return jsonMsg(404, "Not Found")
}

func splitSegments(rest string) []string {
	var segs []string
	for _, p := range strings.Split(rest, "/") {
		if p != "" {
			segs = append(segs, p)
		}
	}
	return segs
}

// matchGitPath matches /{owner}/{repo}.git/{sub}; returns [owner repo sub].
func matchGitPath(path string) []string {
	if !strings.HasPrefix(path, "/") {
		return nil
	}
	rest := path[1:]
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return nil
	}
	owner := rest[:i]
	rest = rest[i+1:]
	j := strings.Index(rest, ".git/")
	if j < 0 {
		return nil
	}
	repo := rest[:j]
	sub := rest[j+5:]
	if owner == "" || repo == "" || sub == "" {
		return nil
	}
	return []string{owner, repo, sub}
}

func (sv *server) handleConn(conn net.Conn) {
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	r := bufio.NewReaderSize(conn, 65536)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		req, err := sv.readRequest(r, conn)
		if err != nil {
			return
		}
		status, contentType, hdrs, body := sv.dispatch(req)
		sv.writeResponse(conn, req, status, contentType, hdrs, body)
		if !req.keepAlive {
			return
		}
	}
}

func (sv *server) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go sv.handleConn(conn)
	}
}
