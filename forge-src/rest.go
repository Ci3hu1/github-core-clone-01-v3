// REST v3 (/api/v3): repositories, branches, commits, contents, issues,
// labels, comments, pull requests. Field-for-field per the kit's checks.
package main

import (
	"strings"
)

type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

func errHTTP(status int, message string) *httpError {
	return &httpError{status: status, message: message}
}

type restResult struct {
	status  int
	body    []byte
	headers map[string]string
}

func writeRepoView(w *jw, r *repo) {
	o := w.obj()
	o.n("id", r.dbID)
	o.s("node_id", globalID("Repository", intStr(r.dbID)))
	o.s("name", r.name)
	o.s("full_name", r.owner+"/"+r.name)
	o.obj("owner", func(o2 *jobj) {
		o2.s("login", r.owner)
		o2.n("id", viewerID)
		o2.s("type", "User")
	})
	o.b("private", r.isPrivate)
	o.s("description", r.description)
	o.s("default_branch", r.defaultBranch)
	o.s("html_url", "https://github.example/"+r.owner+"/"+r.name)
	o.s("url", "/api/v3/repos/"+r.owner+"/"+r.name)
	o.s("clone_url", "https://github.example/"+r.owner+"/"+r.name+".git")
	o.s("git_url", "git://github.example/"+r.owner+"/"+r.name+".git")
	o.s("ssh_url", "git@github.example:"+r.owner+"/"+r.name+".git")
	o.s("created_at", fixedISO)
	o.s("updated_at", fixedISO)
	o.s("pushed_at", fixedISO)
	o.n("forks_count", 0)
	o.n("stargazers_count", 0)
	o.n("watchers_count", 0)
	open := 0
	for _, is := range r.issues {
		if is.state == "open" {
			open++
		}
	}
	o.n("open_issues_count", open)
	o.b("has_issues", true)
	o.b("has_projects", true)
	o.b("has_wiki", true)
	o.b("archived", false)
	o.b("disabled", false)
	if r.isPrivate {
		o.s("visibility", "private")
	} else {
		o.s("visibility", "public")
	}
	o.null("license")
	o.end()
}

func writeIssueView(w *jw, r *repo, is *issue) {
	o := w.obj()
	o.n("id", r.dbID*1000000+is.number)
	o.s("node_id", globalID("Issue", intStr(r.dbID)+":"+intStr(is.number)))
	o.n("number", is.number)
	o.s("title", is.title)
	o.s("body", is.body)
	o.s("state", is.state)
	o.arr("labels", func(a *jarr) {
		for _, name := range is.labels {
			a.obj(func(lo *jobj) {
				lo.s("name", name)
				lo.s("color", "ededed")
				lo.s("description", "")
			})
		}
	})
	o.obj("user", func(o2 *jobj) {
		o2.s("login", viewerLogin)
		o2.n("id", viewerID)
	})
	o.arr("assignees", func(a *jarr) {})
	o.null("assignee")
	o.null("milestone")
	o.n("comments", len(is.comments))
	o.b("locked", false)
	o.s("created_at", fixedISO)
	o.s("updated_at", fixedISO)
	if is.state == "closed" {
		o.s("closed_at", fixedISO)
	} else {
		o.null("closed_at")
	}
	o.s("url", "/api/v3/repos/"+r.owner+"/"+r.name+"/issues/"+intStr(is.number))
	o.s("html_url", "https://github.example/"+r.owner+"/"+r.name+"/issues/"+intStr(is.number))
	o.end()
}

func writePullView(w *jw, r *repo, pr *pull) {
	headSha, ok := r.refs["refs/heads/"+pr.head]
	if !ok {
		headSha = zeroOID
	}
	baseSha, ok := r.refs["refs/heads/"+pr.base]
	if !ok {
		baseSha = zeroOID
	}
	o := w.obj()
	o.n("id", r.dbID*1000000+pr.number)
	o.s("node_id", globalID("PullRequest", intStr(r.dbID)+":"+intStr(pr.number)))
	o.n("number", pr.number)
	o.s("title", pr.title)
	o.s("body", pr.body)
	o.s("state", pr.state)
	o.b("merged", pr.merged)
	if pr.mergedAt == "" {
		o.null("merged_at")
	} else {
		o.s("merged_at", pr.mergedAt)
	}
	if pr.mergeSha == "" {
		o.null("merge_commit_sha")
	} else {
		o.s("merge_commit_sha", pr.mergeSha)
	}
	o.b("mergeable", pr.state == "open" && len(mergeConflicts(r, baseSha, headSha)) == 0)
	o.s("mergeable_state", "clean")
	o.b("draft", false)
	o.b("locked", false)
	o.obj("head", func(o2 *jobj) {
		o2.s("ref", pr.head)
		o2.s("sha", headSha)
		o2.s("label", r.owner+":"+pr.head)
	})
	o.obj("base", func(o2 *jobj) {
		o2.s("ref", pr.base)
		o2.s("sha", baseSha)
		o2.s("label", r.owner+":"+pr.base)
	})
	o.obj("user", func(o2 *jobj) {
		o2.s("login", viewerLogin)
		o2.n("id", viewerID)
	})
	o.arr("assignees", func(a *jarr) {})
	o.s("created_at", fixedISO)
	o.s("updated_at", fixedISO)
	if pr.state == "closed" {
		o.s("closed_at", fixedISO)
	} else {
		o.null("closed_at")
	}
	o.n("commits", 1)
	o.n("additions", 0)
	o.n("deletions", 0)
	o.n("changed_files", 0)
	o.s("url", "/api/v3/repos/"+r.owner+"/"+r.name+"/pulls/"+intStr(pr.number))
	o.s("html_url", "https://github.example/"+r.owner+"/"+r.name+"/pull/"+intStr(pr.number))
	o.s("diff_url", "https://github.example/"+r.owner+"/"+r.name+"/pull/"+intStr(pr.number)+".diff")
	o.s("patch_url", "https://github.example/"+r.owner+"/"+r.name+"/pull/"+intStr(pr.number)+".patch")
	o.end()
}

// pagination computes page/per_page and the Link header value.
func pagination(query map[string]string, rawQuery string, total int, host, path string) (page, perPage int, link string) {
	perPage = atoiDef(query["per_page"], 30)
	if perPage < 1 {
		perPage = 30
	}
	page = atoiDef(query["page"], 1)
	if page < 1 {
		page = 1
	}
	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	urlFor := func(p int) string {
		q := setQueryParam(rawQuery, "page", intStr(p))
		q = setQueryParam(q, "per_page", intStr(perPage))
		return "http://" + host + path + "?" + q
	}
	var links []string
	if page < pages {
		links = append(links, "<"+urlFor(page+1)+">; rel=\"next\"")
	}
	if page > 1 {
		links = append(links, "<"+urlFor(page-1)+">; rel=\"prev\"")
	}
	links = append(links, "<"+urlFor(1)+">; rel=\"first\"")
	links = append(links, "<"+urlFor(pages)+">; rel=\"last\"")
	return page, perPage, strings.Join(links, ", ")
}

func atoiDef(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n
}

// setQueryParam sets (or adds) one query parameter, preserving the others
// in their original order.
func setQueryParam(raw, key, value string) string {
	parts := strings.Split(raw, "&")
	found := false
	for i, p := range parts {
		if p == "" {
			continue
		}
		k := p
		if idx := strings.IndexByte(p, '='); idx >= 0 {
			k = p[:idx]
		}
		if k == key {
			parts[i] = key + "=" + value
			found = true
		}
	}
	if !found {
		parts = append(parts, key+"="+value)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "&")
}

func pageOf[T any](items []T, page, perPage int) []T {
	start := (page - 1) * perPage
	if start >= len(items) {
		return nil
	}
	end := start + perPage
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func needRepo(s *store, owner, name string) (*repo, *httpError) {
	if owner != viewerLogin {
		return nil, errHTTP(404, "Not Found")
	}
	r := s.getRepo(owner, name)
	if r == nil {
		return nil, errHTTP(404, "Not Found")
	}
	return r, nil
}

func resolveRef(r *repo, ref string) string {
	if ref == "" {
		if oid, ok := r.refs["refs/heads/"+r.defaultBranch]; ok {
			return oid
		}
		return ""
	}
	if oid, ok := r.refs["refs/heads/"+ref]; ok {
		return oid
	}
	if oid, ok := r.refs[ref]; ok {
		return oid
	}
	if isSha40(ref) {
		if _, ok := r.objects[ref]; ok {
			return ref
		}
	}
	return ""
}

// commitContents commits a new contents change on a branch.
func commitContents(r *repo, branch, path string, content []byte, message string, deleteFile bool) (commitSha string, blobSha string) {
	refName := "refs/heads/" + branch
	tip, hasTip := r.refs[refName]
	flat := make(map[string]flatEntry)
	if hasTip {
		if commit := r.objects[tip]; commit != nil && commit.typ == objCommit {
			flat = flattenTree(r, parseCommit(commit.data).tree, "")
		}
	}
	blobSha = ""
	if deleteFile {
		delete(flat, path)
	} else {
		blobSha = buildBlob(r, content)
		flat[path] = flatEntry{oid: blobSha, mode: "100644"}
	}
	tree := buildTreeFromFlat(r, flat)
	var parents []string
	if hasTip {
		parents = []string{tip}
	}
	commitSha = buildCommit(r, tree, parents, message, fixedEpochSec)
	r.refs[refName] = commitSha
	return commitSha, blobSha
}

func jsonResult(status int, fn func(w *jw)) restResult {
	var w jw
	fn(&w)
	return restResult{status: status, body: w.bytes()}
}

func jsonResultHeaders(status int, headers map[string]string, fn func(w *jw)) restResult {
	var w jw
	fn(&w)
	return restResult{status: status, body: w.bytes(), headers: headers}
}

// routeRest routes one REST request. body is the parsed JSON body (or nil).
func routeRest(s *store, method string, segments []string, query map[string]string, rawQuery string, body any, host, path string) (restResult, *httpError) {
	b, _ := body.(map[string]any)
	if b == nil {
		b = make(map[string]any)
	}
	seg := segments

	// GET /api/v3/ (root)
	if len(seg) == 0 && method == "GET" {
		return jsonResultHeaders(200, map[string]string{"X-Oauth-Scopes": "repo, read:org"}, func(w *jw) {
			o := w.obj()
			o.s("current_user_url", "/api/v3/user")
			o.s("rate_limit_url", "/api/v3/rate_limit")
			o.end()
		}), nil
	}
	// GET /api/v3/meta
	if len(seg) == 1 && seg[0] == "meta" && method == "GET" {
		return jsonResult(200, func(w *jw) {
			o := w.obj()
			o.s("installed_version", "1.0.0")
			o.b("verifiable_password_authentication", true)
			o.end()
		}), nil
	}
	// GET /api/v3/user
	if len(seg) == 1 && seg[0] == "user" && method == "GET" {
		return jsonResult(200, func(w *jw) {
			o := w.obj()
			o.s("login", viewerLogin)
			o.n("id", viewerID)
			o.s("type", "User")
			o.s("name", "ada iversen")
			o.s("email", viewerEmail)
			o.end()
		}), nil
	}
	// /api/v3/user/repos
	if len(seg) == 2 && seg[0] == "user" && seg[1] == "repos" {
		if method == "GET" {
			all := s.listRepos(viewerLogin)
			page, perPage, link := pagination(query, rawQuery, len(all), host, path)
			pageItems := pageOf(all, page, perPage)
			return jsonResultHeaders(200, map[string]string{"Link": link}, func(w *jw) {
				arr := w.arr()
				for _, r := range pageItems {
					arr.obj(func(o *jobj) { writeRepoViewInto(o, r) })
				}
				w.b = append(w.b, ']')
			}), nil
		}
		if method == "POST" {
			name := getStr(b, "name")
			if name == "" {
				return restResult{}, errHTTP(422, "Validation Failed: name is required")
			}
			if s.getRepo(viewerLogin, name) != nil {
				return restResult{}, errHTTP(422, "Repository already exists")
			}
			priv, hasPriv := getBool(b, "private")
			isPrivate := true
			if hasPriv {
				isPrivate = priv
			}
			r := s.createRepo(viewerLogin, name, getStr(b, "description"), isPrivate)
			return jsonResult(201, func(w *jw) { writeRepoView(w, r) }), nil
		}
	}
	// /api/v3/repos/{owner}/{repo}[...]
	if len(seg) >= 3 && seg[0] == "repos" {
		owner := seg[1]
		name := seg[2]
		rest := seg[3:]
		if len(rest) == 0 {
			if method == "GET" {
				r, herr := needRepo(s, owner, name)
				if herr != nil {
					return restResult{}, herr
				}
				return jsonResult(200, func(w *jw) { writeRepoView(w, r) }), nil
			}
			if method == "PATCH" {
				r, herr := needRepo(s, owner, name)
				if herr != nil {
					return restResult{}, herr
				}
				if d, ok := b["description"].(string); ok {
					r.description = d
				}
				return jsonResult(200, func(w *jw) { writeRepoView(w, r) }), nil
			}
			if method == "DELETE" {
				if _, herr := needRepo(s, owner, name); herr != nil {
					return restResult{}, herr
				}
				s.deleteRepo(owner, name)
				return restResult{status: 204}, nil
			}
			return restResult{}, errHTTP(404, "Not Found")
		}
		r, herr := needRepo(s, owner, name)
		if herr != nil {
			return restResult{}, herr
		}
		// branches
		if len(rest) == 1 && rest[0] == "branches" && method == "GET" {
			refs := make([]string, 0, len(r.refs))
			for ref := range r.refs {
				refs = append(refs, ref)
			}
			sortStrings(refs)
			return jsonResult(200, func(w *jw) {
				arr := w.arr()
				for _, ref := range refs {
					if !strings.HasPrefix(ref, "refs/heads/") {
						continue
					}
					oid := r.refs[ref]
					arr.obj(func(o *jobj) {
						o.s("name", ref[len("refs/heads/"):])
						o.obj("commit", func(c *jobj) { c.s("sha", oid) })
						o.b("protected", false)
					})
				}
				w.b = append(w.b, ']')
			}), nil
		}
		// git/refs
		if len(rest) == 2 && rest[0] == "git" && rest[1] == "refs" && method == "POST" {
			ref := getStr(b, "ref")
			sha := getStr(b, "sha")
			if !strings.HasPrefix(ref, "refs/") || !isSha40(sha) {
				return restResult{}, errHTTP(422, "Validation Failed: ref or sha is invalid")
			}
			if _, ok := r.objects[sha]; !ok {
				return restResult{}, errHTTP(422, "Validation Failed: ref or sha is invalid")
			}
			r.refs[ref] = sha
			return jsonResult(201, func(w *jw) {
				o := w.obj()
				o.s("ref", ref)
				o.obj("object", func(o2 *jobj) {
					o2.s("sha", sha)
					o2.s("type", "commit")
				})
				o.end()
			}), nil
		}
		// commits
		if len(rest) == 1 && rest[0] == "commits" && method == "GET" {
			tip := resolveRef(r, query["sha"])
			if tip == "" {
				return jsonResult(200, func(w *jw) { w.b = append(w.b, "[]"...) }), nil
			}
			list := history(r, tip, 10000)
			return jsonResult(200, func(w *jw) {
				arr := w.arr()
				for _, sha := range list {
					obj := r.objects[sha]
					msg := ""
					if obj != nil {
						msg = parseCommit(obj.data).message
					}
					arr.obj(func(o *jobj) {
						o.s("sha", sha)
						o.obj("commit", func(c *jobj) {
							c.s("message", msg)
							c.obj("author", func(a *jobj) {
								a.s("name", "ada iversen")
								a.s("email", viewerEmail)
							})
							c.obj("committer", func(a *jobj) {
								a.s("name", "ada iversen")
								a.s("email", viewerEmail)
							})
						})
						o.arr("parents", func(a *jarr) {})
					})
				}
				w.b = append(w.b, ']')
			}), nil
		}
		// contents
		if len(rest) >= 1 && rest[0] == "contents" {
			return routeContents(s, r, method, rest[1:], query, b)
		}
		// labels
		if len(rest) == 1 && rest[0] == "labels" && method == "POST" {
			name := getStr(b, "name")
			if name == "" {
				return restResult{}, errHTTP(422, "Validation Failed: name is required")
			}
			if _, ok := r.labels[name]; !ok {
				color := getStr(b, "color")
				if color == "" {
					color = "ededed"
				}
				r.labels[name] = &label{name: name, color: color}
			}
			lb := r.labels[name]
			return jsonResult(201, func(w *jw) {
				o := w.obj()
				o.s("name", lb.name)
				o.s("color", lb.color)
				o.s("description", "")
				o.end()
			}), nil
		}
		// issues
		if rest[0] == "issues" {
			return routeIssues(s, r, method, rest[1:], query, rawQuery, b, host, path)
		}
		// pulls
		if rest[0] == "pulls" {
			return routePulls(s, r, method, rest[1:], query, rawQuery, b, host, path)
		}
		return restResult{}, errHTTP(404, "Not Found")
	}
	return restResult{}, errHTTP(404, "Not Found")
}

// writeRepoViewInto writes a repo view into an already-open object builder.
func writeRepoViewInto(o *jobj, r *repo) {
	var w jw
	writeRepoView(&w, r)
	// Re-emit: the view was rendered into w; copy its bytes minus braces.
	b := w.bytes()
	o.w.b = append(o.w.b, b[1:len(b)-1]...)
}

func routeContents(s *store, r *repo, method string, rest []string, query map[string]string, b map[string]any) (restResult, *httpError) {
	filePath := strings.Join(rest, "/")
	if method == "PUT" {
		branch := r.defaultBranch
		if v, ok := b["branch"].(string); ok {
			branch = v
		}
		message := getStr(b, "message")
		if message == "" {
			message = "Update file"
		}
		content := b64decode(getStr(b, "content"))
		tip, hasTip := r.refs["refs/heads/"+branch]
		var currentSha string
		if hasTip {
			if commit := r.objects[tip]; commit != nil && commit.typ == objCommit {
				currentSha = flattenTree(r, parseCommit(commit.data).tree, "")[filePath].oid
			}
		}
		isUpdate := currentSha != ""
		if isUpdate {
			sha, ok := b["sha"].(string)
			if !ok || !strings.EqualFold(sha, currentSha) {
				return restResult{}, errHTTP(409, "Conflict: sha does not match the current file")
			}
		}
		commitSha, blobSha := commitContents(r, branch, filePath, content, message, false)
		base := filePath
		if idx := strings.LastIndexByte(filePath, '/'); idx >= 0 {
			base = filePath[idx+1:]
		}
		return jsonResult(map[bool]int{true: 200, false: 201}[isUpdate], func(w *jw) {
			o := w.obj()
			o.obj("content", func(c *jobj) {
				c.s("name", base)
				c.s("path", filePath)
				c.s("sha", blobSha)
				c.n("size", len(content))
				c.s("encoding", "base64")
				c.s("type", "file")
			})
			o.obj("commit", func(c *jobj) { c.s("sha", commitSha) })
			o.end()
		}), nil
	}
	if method == "DELETE" {
		tip, hasTip := r.refs["refs/heads/"+r.defaultBranch]
		var currentSha string
		if hasTip {
			if commit := r.objects[tip]; commit != nil && commit.typ == objCommit {
				currentSha = flattenTree(r, parseCommit(commit.data).tree, "")[filePath].oid
			}
		}
		if currentSha == "" {
			return restResult{}, errHTTP(404, "Not Found")
		}
		msg := getStr(b, "message")
		if msg == "" {
			msg = "Delete file"
		}
		commitSha, _ := commitContents(r, r.defaultBranch, filePath, nil, msg, true)
		return jsonResult(200, func(w *jw) {
			o := w.obj()
			o.obj("commit", func(c *jobj) { c.s("sha", commitSha) })
			o.end()
		}), nil
	}
	if method == "GET" {
		tip := resolveRef(r, query["ref"])
		if tip == "" {
			return restResult{}, errHTTP(404, "Not Found")
		}
		commit := r.objects[tip]
		if commit == nil || commit.typ != objCommit {
			return restResult{}, errHTTP(404, "Not Found")
		}
		flat := flattenTree(r, parseCommit(commit.data).tree, "")
		if filePath == "" {
			// Root listing.
			type dirEnt struct {
				name, path, typ, sha string
			}
			seen := make(map[string]dirEnt)
			var order []string
			for p, v := range flat {
				top := p
				if idx := strings.IndexByte(p, '/'); idx >= 0 {
					top = p[:idx]
					if _, ok := seen[top]; !ok {
						seen[top] = dirEnt{name: top, path: top, typ: "dir", sha: v.oid}
						order = append(order, top)
					}
				} else {
					if _, ok := seen[top]; !ok {
						order = append(order, top)
					}
					seen[top] = dirEnt{name: top, path: top, typ: "file", sha: v.oid}
				}
			}
			return jsonResult(200, func(w *jw) {
				arr := w.arr()
				for _, name := range order {
					e := seen[name]
					arr.obj(func(o *jobj) {
						o.s("name", e.name)
						o.s("path", e.path)
						o.s("type", e.typ)
						o.s("sha", e.sha)
					})
				}
				w.b = append(w.b, ']')
			}), nil
		}
		if file, ok := flat[filePath]; ok {
			blob := r.objects[file.oid]
			var bytes []byte
			if blob != nil {
				bytes = blob.data
			}
			base := filePath
			if idx := strings.LastIndexByte(filePath, '/'); idx >= 0 {
				base = filePath[idx+1:]
			}
			return jsonResult(200, func(w *jw) {
				o := w.obj()
				o.s("type", "file")
				o.s("encoding", "base64")
				o.s("content", b64encode(bytes))
				o.s("sha", file.oid)
				o.n("size", len(bytes))
				o.s("name", base)
				o.s("path", filePath)
				o.end()
			}), nil
		}
		// Directory?
		prefix := filePath + "/"
		type dirEnt struct {
			typ, name, path, sha string
		}
		seen := make(map[string]bool)
		var entries []dirEnt
		for p, v := range flat {
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			restPath := p[len(prefix):]
			first := restPath
			if idx := strings.IndexByte(restPath, '/'); idx >= 0 {
				first = restPath[:idx]
			}
			if seen[first] {
				continue
			}
			seen[first] = true
			typ := "file"
			if strings.Contains(restPath, "/") {
				typ = "dir"
			}
			entries = append(entries, dirEnt{typ: typ, name: first, path: filePath + "/" + first, sha: v.oid})
		}
		if len(entries) == 0 {
			return restResult{}, errHTTP(404, "Not Found")
		}
		for i := 1; i < len(entries); i++ {
			for j := i; j > 0 && entries[j].name < entries[j-1].name; j-- {
				entries[j], entries[j-1] = entries[j-1], entries[j]
			}
		}
		return jsonResult(200, func(w *jw) {
			arr := w.arr()
			for _, e := range entries {
				arr.obj(func(o *jobj) {
					o.s("type", e.typ)
					o.s("name", e.name)
					o.s("path", e.path)
					o.s("sha", e.sha)
				})
			}
			w.b = append(w.b, ']')
		}), nil
	}
	return restResult{}, errHTTP(404, "Not Found")
}

func routeIssues(s *store, r *repo, method string, rest []string, query map[string]string, rawQuery string, b map[string]any, host, path string) (restResult, *httpError) {
	if len(rest) == 0 {
		if method == "POST" {
			var labels []string
			if arr, ok := b["labels"].([]any); ok {
				for _, v := range arr {
					if str, ok := v.(string); ok {
						labels = append(labels, str)
					}
				}
			}
			for _, l := range labels {
				if _, ok := r.labels[l]; !ok {
					return restResult{}, errHTTP(422, "Validation Failed: label \""+l+"\" does not exist")
				}
			}
			r.counter++
			is := &issue{number: r.counter, title: getStr(b, "title"), body: getStr(b, "body"), state: "open", labels: labels, seq: s.nextSeq()}
			r.issues[is.number] = is
			return jsonResult(201, func(w *jw) { writeIssueView(w, r, is) }), nil
		}
		if method == "GET" {
			state := query["state"]
			if state == "" {
				state = "open"
			}
			var list []*issue
			for _, is := range r.issues {
				list = append(list, is)
			}
			if state != "all" {
				filtered := list[:0]
				for _, is := range list {
					if is.state == state {
						filtered = append(filtered, is)
					}
				}
				list = filtered
			}
			direction := query["direction"]
			if direction == "" {
				direction = "desc"
			}
			sortParam := query["sort"]
			if sortParam == "" {
				sortParam = "created"
			}
			if sortParam == "created" {
				for i := 1; i < len(list); i++ {
					for j := i; j > 0; j-- {
						less := false
						if direction == "asc" {
							less = list[j].seq < list[j-1].seq
						} else {
							less = list[j].seq > list[j-1].seq
						}
						if !less {
							break
						}
						list[j], list[j-1] = list[j-1], list[j]
					}
				}
			} else {
				// TS keeps Map insertion order here, which is creation
				// order (seq ascending).
				for i := 1; i < len(list); i++ {
					for j := i; j > 0 && list[j].seq < list[j-1].seq; j-- {
						list[j], list[j-1] = list[j-1], list[j]
					}
				}
			}
			page, perPage, link := pagination(query, rawQuery, len(list), host, path)
			pageItems := pageOf(list, page, perPage)
			return jsonResultHeaders(200, map[string]string{"Link": link}, func(w *jw) {
				arr := w.arr()
				for _, is := range pageItems {
					arr.obj(func(o *jobj) { writeIssueViewInto(o, r, is) })
				}
				w.b = append(w.b, ']')
			}), nil
		}
	}
	num := atoiDef(rest[0], -1)
	if len(rest) == 1 && num >= 0 {
		is, ok := r.issues[num]
		if !ok {
			return restResult{}, errHTTP(404, "Not Found")
		}
		if method == "GET" {
			return jsonResult(200, func(w *jw) { writeIssueView(w, r, is) }), nil
		}
		if method == "PATCH" {
			if st, ok := b["state"].(string); ok && (st == "closed" || st == "open") {
				is.state = st
			}
			if t, ok := b["title"].(string); ok {
				is.title = t
			}
			if bd, ok := b["body"].(string); ok {
				is.body = bd
			}
			return jsonResult(200, func(w *jw) { writeIssueView(w, r, is) }), nil
		}
		return restResult{}, errHTTP(404, "Not Found")
	}
	if len(rest) == 2 && rest[1] == "comments" && num >= 0 {
		is, ok := r.issues[num]
		if !ok {
			return restResult{}, errHTTP(404, "Not Found")
		}
		if method == "POST" {
			r.commentCounter++
			c := &comment{id: r.commentCounter, body: getStr(b, "body"), seq: s.nextSeq()}
			is.comments = append(is.comments, c)
			return jsonResult(201, func(w *jw) {
				o := w.obj()
				o.n("id", c.id)
				o.s("body", c.body)
				o.obj("user", func(u *jobj) { u.s("login", viewerLogin) })
				o.end()
			}), nil
		}
		if method == "GET" {
			return jsonResult(200, func(w *jw) {
				arr := w.arr()
				for _, c := range is.comments {
					arr.obj(func(o *jobj) {
						o.n("id", c.id)
						o.s("body", c.body)
						o.obj("user", func(u *jobj) { u.s("login", viewerLogin) })
					})
				}
				w.b = append(w.b, ']')
			}), nil
		}
		return restResult{}, errHTTP(404, "Not Found")
	}
	return restResult{}, errHTTP(404, "Not Found")
}

func writeIssueViewInto(o *jobj, r *repo, is *issue) {
	var w jw
	writeIssueView(&w, r, is)
	b := w.bytes()
	o.w.b = append(o.w.b, b[1:len(b)-1]...)
}

func writePullViewInto(o *jobj, r *repo, pr *pull) {
	var w jw
	writePullView(&w, r, pr)
	b := w.bytes()
	o.w.b = append(o.w.b, b[1:len(b)-1]...)
}

func routePulls(s *store, r *repo, method string, rest []string, query map[string]string, rawQuery string, b map[string]any, host, path string) (restResult, *httpError) {
	if len(rest) == 0 {
		if method == "POST" {
			head := getStr(b, "head")
			base := getStr(b, "base")
			if base == "" {
				base = r.defaultBranch
			}
			if _, ok := r.refs["refs/heads/"+head]; !ok {
				return restResult{}, errHTTP(422, "Validation Failed: head or base branch does not exist")
			}
			if _, ok := r.refs["refs/heads/"+base]; !ok {
				return restResult{}, errHTTP(422, "Validation Failed: head or base branch does not exist")
			}
			r.counter++
			pr := &pull{number: r.counter, title: getStr(b, "title"), body: getStr(b, "body"), state: "open", head: head, base: base, seq: s.nextSeq()}
			r.pulls[pr.number] = pr
			return jsonResult(201, func(w *jw) { writePullView(w, r, pr) }), nil
		}
		if method == "GET" {
			state := query["state"]
			if state == "" {
				state = "open"
			}
			var list []*pull
			for _, pr := range r.pulls {
				list = append(list, pr)
			}
			if state != "all" {
				filtered := list[:0]
				for _, pr := range list {
					if pr.state == state {
						filtered = append(filtered, pr)
					}
				}
				list = filtered
			}
			for i := 1; i < len(list); i++ {
				for j := i; j > 0 && list[j].seq > list[j-1].seq; j-- {
					list[j], list[j-1] = list[j-1], list[j]
				}
			}
			page, perPage, link := pagination(query, rawQuery, len(list), host, path)
			pageItems := pageOf(list, page, perPage)
			return jsonResultHeaders(200, map[string]string{"Link": link}, func(w *jw) {
				arr := w.arr()
				for _, pr := range pageItems {
					arr.obj(func(o *jobj) { writePullViewInto(o, r, pr) })
				}
				w.b = append(w.b, ']')
			}), nil
		}
	}
	num := atoiDef(rest[0], -1)
	if len(rest) == 1 && num >= 0 {
		pr, ok := r.pulls[num]
		if !ok {
			return restResult{}, errHTTP(404, "Not Found")
		}
		if method == "GET" {
			return jsonResult(200, func(w *jw) { writePullView(w, r, pr) }), nil
		}
		return restResult{}, errHTTP(404, "Not Found")
	}
	if len(rest) == 2 && rest[1] == "files" && num >= 0 && method == "GET" {
		pr, ok := r.pulls[num]
		if !ok {
			return restResult{}, errHTTP(404, "Not Found")
		}
		headSha := r.refs["refs/heads/"+pr.head]
		baseSha := r.refs["refs/heads/"+pr.base]
		var headFlat, baseFlat map[string]flatEntry
		if c := r.objects[headSha]; c != nil && c.typ == objCommit {
			headFlat = flattenTree(r, parseCommit(c.data).tree, "")
		} else {
			headFlat = make(map[string]flatEntry)
		}
		if c := r.objects[baseSha]; c != nil && c.typ == objCommit {
			baseFlat = flattenTree(r, parseCommit(c.data).tree, "")
		} else {
			baseFlat = make(map[string]flatEntry)
		}
		files := diffFlat(baseFlat, headFlat)
		return jsonResult(200, func(w *jw) {
			arr := w.arr()
			for _, d := range files {
				additions, deletions := lineDiff(r, d.oldOID, d.newOID)
				status := d.status
				arr.obj(func(o *jobj) {
					o.s("filename", d.path)
					o.s("status", status)
					o.n("additions", additions)
					o.n("deletions", deletions)
					o.n("changes", additions+deletions)
					if d.newOID != "" {
						o.s("sha", d.newOID)
					} else {
						o.s("sha", d.oldOID)
					}
				})
			}
			w.b = append(w.b, ']')
		}), nil
	}
	if len(rest) == 2 && rest[1] == "merge" && num >= 0 && method == "PUT" {
		pr, ok := r.pulls[num]
		if !ok {
			return restResult{}, errHTTP(404, "Not Found")
		}
		if pr.state != "open" {
			return restResult{}, errHTTP(405, "Pull request is not open")
		}
		headSha := r.refs["refs/heads/"+pr.head]
		baseSha := r.refs["refs/heads/"+pr.base]
		if len(mergeConflicts(r, baseSha, headSha)) > 0 {
			return restResult{}, errHTTP(405, "Merge conflict: cannot merge")
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
		return jsonResult(200, func(w *jw) {
			o := w.obj()
			o.b("merged", true)
			o.s("sha", mergeSha)
			o.s("message", "Pull request successfully merged")
			o.end()
		}), nil
	}
	return restResult{}, errHTTP(404, "Not Found")
}
