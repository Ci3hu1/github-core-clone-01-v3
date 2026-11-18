// github-core-clone-01 — process entrypoint (port of the TS main.ts).
//
// The trial harness (env) owns the lifecycle: this process only has to come
// up, announce the port it bound, and stay alive until the env declares the
// trial finished. Nothing here is scored directly — the suite grades the
// forge — but if this process exits early or never registers, the trial
// scores 0, so every failure path below is deliberate:
//
//  1. MAKEFUN_ENV_URL must name the env origin. Without it there is no one
//     to register with, so fail fast with exit code 1.
//  2. Bind an ephemeral port (listen on 127.0.0.1:0); the kernel chooses.
//     Port 8080 belongs to the env and is never bound.
//  3. POST {"port": N} to $MAKEFUN_ENV_URL/register. A 200 means
//     registered; a 409 means some other process registered first, which is
//     fine. A 400/405 is a contract violation and ends the process; anything
//     else (network error, 5xx, timeout) is retried, because the env may
//     still be starting.
//  4. Poll GET $MAKEFUN_ENV_URL/done until the body reads {"done": true}.
//  5. Close the listener and exit 0.
package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// shopToken is the Bearer token the suite sends; empty means no auth
// (local dev). Set from MAKEFUN_SHOP_TOKEN.
var shopToken string

const requestBudget = 30 * time.Second
const backoffStart = 200 * time.Millisecond
const backoffCap = 1500 * time.Millisecond
const backoffMaxShift = 7

func pause(ms time.Duration) { time.Sleep(ms) }

func backoffMs(attempt int) time.Duration {
	shift := attempt
	if shift > backoffMaxShift {
		shift = backoffMaxShift
	}
	delay := backoffStart * time.Duration(1<<uint(shift))
	if delay > backoffCap {
		return backoffCap
	}
	return delay
}

type simpleError struct{ s string }

func (e *simpleError) Error() string { return e.s }

// envRequest performs one minimal HTTP/1.1 request against the env origin
// and returns the status + body. No net/http: the binary must stay small.
func envRequest(method, url, body string) (int, []byte, error) {
	rest := url
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	host := rest
	path := "/"
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, path = rest[:i], rest[i:]
	}
	conn, err := net.DialTimeout("tcp", host, requestBudget)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(requestBudget))
	var req strings.Builder
	req.WriteString(method)
	req.WriteString(" ")
	req.WriteString(path)
	req.WriteString(" HTTP/1.1\r\nHost: ")
	req.WriteString(host)
	req.WriteString("\r\nConnection: close\r\n")
	if body != "" {
		req.WriteString("Content-Type: application/json\r\nContent-Length: ")
		req.WriteString(strconv.Itoa(len(body)))
		req.WriteString("\r\n")
	}
	req.WriteString("\r\n")
	if body != "" {
		req.WriteString(body)
	}
	if _, err := conn.Write([]byte(req.String())); err != nil {
		return 0, nil, err
	}
	// Read the whole response (the env closes the connection).
	var raw []byte
	buf := make([]byte, 8192)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	// Split headers/body.
	sep := strings.Index(string(raw), "\r\n\r\n")
	if sep < 0 {
		return 0, nil, errBadRequest
	}
	head, respBody := string(raw[:sep]), raw[sep+4:]
	// Status line: HTTP/1.1 200 OK
	firstLine := head
	if i := strings.IndexByte(head, '\r'); i >= 0 {
		firstLine = head[:i]
	}
	parts := strings.SplitN(firstLine, " ", 3)
	if len(parts) < 2 {
		return 0, nil, errBadRequest
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, nil, err
	}
	// The env may use chunked encoding; de-chunk if so.
	if strings.Contains(strings.ToLower(head), "transfer-encoding: chunked") {
		respBody = dechunk(respBody)
	}
	return status, respBody, nil
}

func dechunk(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		i := strings.Index(string(b), "\r\n")
		if i < 0 {
			break
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b[:i])), 16, 64)
		if err != nil || n == 0 {
			break
		}
		b = b[i+2:]
		if int64(len(b)) < n {
			break
		}
		out = append(out, b[:n]...)
		b = b[n:]
		if len(b) >= 2 {
			b = b[2:]
		}
	}
	return out
}

func announcePort(envOrigin string, port int) error {
	body := `{"port":` + strconv.Itoa(port) + `}`
	attempt := 0
	for {
		status, respBody, err := envRequest("POST", envOrigin+"/register", body)
		if err == nil {
			if status == 200 || status == 409 {
				return nil
			}
			if status == 400 || status == 405 {
				return &simpleError{"register refused: HTTP " + strconv.Itoa(status) + ": " + string(respBody)}
			}
		}
		pause(backoffMs(attempt))
		attempt++
	}
}

func waitUntilDone(envOrigin string) {
	attempt := 0
	for {
		status, respBody, err := envRequest("GET", envOrigin+"/done", "")
		if err == nil && status == 200 {
			if v, ok := parseJSON(respBody); ok {
				if m, ok := v.(map[string]any); ok {
					if done, ok := m["done"].(bool); ok && done {
						return
					}
				}
			}
		}
		pause(backoffMs(attempt))
		attempt++
	}
}

func main() {
	shopToken = os.Getenv("MAKEFUN_SHOP_TOKEN")
	envOrigin := strings.TrimRight(os.Getenv("MAKEFUN_ENV_URL"), "/")
	if envOrigin == "" {
		os.Stderr.WriteString("[entry] MAKEFUN_ENV_URL is not set: no env to register with\n")
		os.Exit(1)
	}

	store := newStore()
	sv := newServer(store)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Stderr.WriteString("[entry] fatal: " + err.Error() + "\n")
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	os.Stdout.WriteString("[entry] forge listening on port " + strconv.Itoa(port) + "\n")

	go sv.serve(ln)

	if err := announcePort(envOrigin, port); err != nil {
		os.Stderr.WriteString("[entry] fatal: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[entry] port announced; waiting for the env to finish the trial\n")

	waitUntilDone(envOrigin)
	_ = ln.Close()
	os.Stdout.WriteString("[entry] trial done, shutting down\n")
	os.Exit(0)
}
