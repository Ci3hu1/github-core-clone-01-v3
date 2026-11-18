// MCP (/mcp): JSON-RPC 2.0 over Streamable HTTP. Serves server/discover
// (2026-07-28) and the legacy handshake (initialize, ping, tools/list,
// tools/call). Six tools acting on the same store as REST.
package main

import (
	"strconv"
	"strings"
)

var mcpSupportedVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

func normToolName(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

var mcpToolNames = []string{"create_repo", "get_repo", "create_issue", "list_issues", "create_pull_request", "merge_pull_request"}

func findToolName(name string) (string, bool) {
	n := normToolName(name)
	for _, t := range mcpToolNames {
		if normToolName(t) == n {
			return t, true
		}
	}
	for _, t := range mcpToolNames {
		nt := normToolName(t)
		if strings.HasSuffix(n, nt) && len(n) > len(nt) {
			return t, true
		}
	}
	return "", false
}

func mcpToolRequired(name string) []string {
	switch name {
	case "create_repo":
		return []string{"name"}
	case "get_repo":
		return []string{"owner", "repo"}
	case "create_issue":
		return []string{"owner", "repo", "title"}
	case "list_issues":
		return []string{"owner", "repo"}
	case "create_pull_request":
		return []string{"owner", "repo", "title", "head", "base"}
	case "merge_pull_request":
		return []string{"owner", "repo", "pull_number"}
	}
	return nil
}

func argString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return ""
	}
	return ""
}

func argNum(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		n, _ := strconv.ParseFloat(t, 64)
		return n
	case bool:
		if t {
			return 1
		}
		return 0
	}
	return 0
}

// writeToolsList emits the six tool definitions in the fixed order the
// TS version uses.
func writeToolsList(w *jw) {
	arr := w.arr()
	tool := func(name, desc string, props [][3]string, required []string) {
		arr.obj(func(o *jobj) {
			o.s("name", name)
			o.s("description", desc)
			o.obj("inputSchema", func(s *jobj) {
				s.s("type", "object")
				s.obj("properties", func(p *jobj) {
					for _, pr := range props {
						p.obj(pr[0], func(f *jobj) {
							f.s("type", pr[1])
							f.s("description", pr[2])
						})
					}
				})
				s.arr("required", func(a *jarr) {
					for _, r := range required {
						a.s(r)
					}
				})
			})
		})
	}
	tool("create_repo", "Create a new repository owned by the viewer.", [][3]string{
		{"owner", "string", "Repository owner login"},
		{"name", "string", "Repository name"},
		{"description", "string", "Repository description"},
		{"private", "boolean", "Whether the repository is private"},
	}, []string{"name"})
	tool("get_repo", "Get a repository by owner and name.", [][3]string{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
	}, []string{"owner", "repo"})
	tool("create_issue", "Create an issue in a repository.", [][3]string{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"title", "string", "Issue title"},
		{"body", "string", "Issue body"},
	}, []string{"owner", "repo", "title"})
	tool("list_issues", "List issues in a repository.", [][3]string{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"state", "string", "Issue state filter: open, closed, or all"},
	}, []string{"owner", "repo"})
	tool("create_pull_request", "Create a pull request in a repository.", [][3]string{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"title", "string", "Pull request title"},
		{"body", "string", "Pull request body"},
		{"head", "string", "Head branch name"},
		{"base", "string", "Base branch name"},
	}, []string{"owner", "repo", "title", "head", "base"})
	tool("merge_pull_request", "Merge a pull request with a squash merge.", [][3]string{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"pull_number", "integer", "Pull request number"},
	}, []string{"owner", "repo", "pull_number"})
	arr.end()
}

func mcpResultText(out []byte) []byte {
	var w jw
	o := w.obj()
	o.arr("content", func(a *jarr) {
		a.obj(func(c *jobj) {
			c.s("type", "text")
			c.s("text", string(out))
		})
	})
	o.raw("structuredContent", out)
	o.end()
	return w.bytes()
}

func mcpToolError(text string) []byte {
	var w jw
	o := w.obj()
	o.arr("content", func(a *jarr) {
		a.obj(func(c *jobj) {
			c.s("type", "text")
			c.s("text", text)
		})
	})
	o.b("isError", true)
	o.end()
	return w.bytes()
}

func mcpNeedRepo(s *store, owner, name string) *repo {
	if owner != viewerLogin {
		return nil
	}
	return s.getRepo(owner, name)
}

// mcpCallTool executes one tool. Returns the result object bytes, or an
// RPC error (code, message) when the tool itself is unknown.
func mcpCallTool(s *store, name string, args map[string]any) ([]byte, int, string) {
	tool, ok := findToolName(name)
	if !ok {
		return nil, -32601, "Method not found: " + name
	}
	for _, req := range mcpToolRequired(tool) {
		v, present := args[req]
		if !present || v == nil || argString(v) == "" {
			return mcpToolError("missing required argument: " + req), 0, ""
		}
	}
	owner := viewerLogin
	if v, ok := args["owner"]; ok {
		owner = argString(v)
	}
	switch tool {
	case "create_repo":
		repoName := argString(args["name"])
		r := s.getRepo(viewerLogin, repoName)
		if r == nil {
			priv := true
			if b, ok := args["private"].(bool); ok && !b {
				priv = false
			}
			r = s.createRepo(viewerLogin, repoName, argString(args["description"]), priv)
		}
		var w jw
		writeRepoView(&w, r)
		return mcpResultText(w.bytes()), 0, ""
	case "get_repo":
		r := mcpNeedRepo(s, owner, argString(args["repo"]))
		if r == nil {
			return mcpToolError("Repository not found: " + owner + "/" + argString(args["repo"])), 0, ""
		}
		var w jw
		writeRepoView(&w, r)
		return mcpResultText(w.bytes()), 0, ""
	case "create_issue":
		r := mcpNeedRepo(s, owner, argString(args["repo"]))
		if r == nil {
			return mcpToolError("Repository not found: " + owner + "/" + argString(args["repo"])), 0, ""
		}
		r.counter++
		is := &issue{number: r.counter, title: argString(args["title"]), body: argString(args["body"]), state: "open", seq: s.nextSeq()}
		r.issues[is.number] = is
		var w jw
		writeIssueView(&w, r, is)
		return mcpResultText(w.bytes()), 0, ""
	case "list_issues":
		r := mcpNeedRepo(s, owner, argString(args["repo"]))
		if r == nil {
			return mcpToolError("Repository not found: " + owner + "/" + argString(args["repo"])), 0, ""
		}
		state := argString(args["state"])
		if state == "" {
			state = "open"
		}
		var list []*issue
		for _, is := range r.issues {
			if state == "all" || is.state == state {
				list = append(list, is)
			}
		}
		for i := 1; i < len(list); i++ {
			for j := i; j > 0 && list[j].seq > list[j-1].seq; j-- {
				list[j], list[j-1] = list[j-1], list[j]
			}
		}
		var w jw
		o := w.obj()
		o.arr("issues", func(a *jarr) {
			for _, is := range list {
				a.obj(func(lo *jobj) { writeIssueViewInto(lo, r, is) })
			}
		})
		o.end()
		return mcpResultText(w.bytes()), 0, ""
	case "create_pull_request":
		r := mcpNeedRepo(s, owner, argString(args["repo"]))
		if r == nil {
			return mcpToolError("Repository not found: " + owner + "/" + argString(args["repo"])), 0, ""
		}
		head := argString(args["head"])
		base := argString(args["base"])
		if base == "" {
			base = r.defaultBranch
		}
		if _, ok := r.refs["refs/heads/"+head]; !ok {
			return mcpToolError("Head or base branch does not exist"), 0, ""
		}
		if _, ok := r.refs["refs/heads/"+base]; !ok {
			return mcpToolError("Head or base branch does not exist"), 0, ""
		}
		r.counter++
		pr := &pull{number: r.counter, title: argString(args["title"]), body: argString(args["body"]), state: "open", head: head, base: base, seq: s.nextSeq()}
		r.pulls[pr.number] = pr
		var w jw
		writePullView(&w, r, pr)
		return mcpResultText(w.bytes()), 0, ""
	case "merge_pull_request":
		r := mcpNeedRepo(s, owner, argString(args["repo"]))
		if r == nil {
			return mcpToolError("Repository not found: " + owner + "/" + argString(args["repo"])), 0, ""
		}
		num := int(argNum(args["pull_number"]))
		pr := r.pulls[num]
		if pr == nil {
			return mcpToolError("Pull request not found: " + intStr(num)), 0, ""
		}
		if pr.state != "open" {
			return mcpToolError("Pull request is not open"), 0, ""
		}
		headSha := r.refs["refs/heads/"+pr.head]
		baseSha := r.refs["refs/heads/"+pr.base]
		if len(mergeConflicts(r, baseSha, headSha)) > 0 {
			return mcpToolError("Merge conflict: cannot merge"), 0, ""
		}
		headCommit := r.objects[headSha]
		headTree := ""
		if headCommit != nil && headCommit.typ == objCommit {
			headTree = parseCommit(headCommit.data).tree
		}
		mergeSha := buildCommit(r, headTree, []string{baseSha}, "Merge pull request #"+intStr(pr.number)+" from "+r.owner+"/"+pr.head, fixedEpochSec)
		r.refs["refs/heads/"+pr.base] = mergeSha
		pr.state = "closed"
		pr.merged = true
		pr.mergedAt = fixedISO
		pr.mergeSha = mergeSha
		var w jw
		writePullView(&w, r, pr)
		return mcpResultText(w.bytes()), 0, ""
	}
	return mcpToolError("Unknown tool: " + name), 0, ""
}

func mcpRpcResult(id any, result []byte) []byte {
	var w jw
	o := w.obj()
	o.s("jsonrpc", "2.0")
	o.key("id")
	w.writeAny(id)
	o.raw("result", result)
	o.end()
	return w.bytes()
}

func mcpRpcError(id any, code int, message string) []byte {
	var w jw
	o := w.obj()
	o.s("jsonrpc", "2.0")
	o.key("id")
	w.writeAny(id)
	o.obj("error", func(e *jobj) {
		e.n("code", code)
		e.s("message", message)
	})
	o.end()
	return w.bytes()
}

func mcpServerInfo(o *jobj) {
	o.s("name", "forge")
	o.s("version", "1.0.0")
}

// handleMcp handles one POST /mcp body (already parsed JSON, or nil when
// unparsable). Returns status + body bytes.
func handleMcp(s *store, body any, rawOk bool) (int, []byte) {
	if !rawOk || body == nil {
		return 400, mcpRpcError(nil, -32700, "Parse error")
	}
	msg, ok := body.(map[string]any)
	if !ok {
		return 400, mcpRpcError(nil, -32700, "Parse error")
	}
	id, hasID := msg["id"]
	if !hasID || id == nil {
		// Notification: no response body.
		return 200, []byte("{}")
	}
	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		params = make(map[string]any)
	}
	switch method {
	case "server/discover":
		var res jw
		o := res.obj()
		o.arr("supportedVersions", func(a *jarr) {
			for _, v := range mcpSupportedVersions {
				a.s(v)
			}
		})
		o.obj("serverInfo", func(si *jobj) { mcpServerInfo(si) })
		o.obj("capabilities", func(c *jobj) {
			c.obj("tools", func(*jobj) {})
		})
		o.obj("_meta", func(m *jobj) {
			m.obj("io.modelcontextprotocol/serverInfo", func(si *jobj) { mcpServerInfo(si) })
		})
		o.end()
		return 200, mcpRpcResult(id, res.bytes())
	case "initialize":
		offered, _ := params["protocolVersion"].(string)
		if offered == "" {
			offered = "2025-11-25"
		}
		negotiated := "2025-11-25"
		for _, v := range mcpSupportedVersions {
			if v == offered {
				negotiated = offered
				break
			}
		}
		var res jw
		o := res.obj()
		o.s("protocolVersion", negotiated)
		o.obj("capabilities", func(c *jobj) {
			c.obj("tools", func(*jobj) {})
		})
		o.obj("serverInfo", func(si *jobj) { mcpServerInfo(si) })
		o.end()
		return 200, mcpRpcResult(id, res.bytes())
	case "ping":
		return 200, mcpRpcResult(id, []byte("{}"))
	case "tools/list":
		var res jw
		o := res.obj()
		o.key("tools")
		writeToolsList(&res)
		o.end()
		return 200, mcpRpcResult(id, res.bytes())
	case "tools/call":
		name, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = make(map[string]any)
		}
		result, code, errmsg := mcpCallTool(s, name, args)
		if code != 0 {
			return 200, mcpRpcError(id, code, errmsg)
		}
		return 200, mcpRpcResult(id, result)
	default:
		return 200, mcpRpcError(id, -32601, "Method not found: "+method)
	}
}
