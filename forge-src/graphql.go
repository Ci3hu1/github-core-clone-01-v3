// GraphQL v4 (/api/graphql): a small hand-rolled parser + executor for the
// exact documents gh and github-mcp-server send. No schema library, no
// dependencies: the documents are fixed, so the parser only needs selections,
// arguments (variables, inline enums, input objects, lists, null), aliases,
// fragments and __type introspection.
package main

import (
	"strconv"
	"strings"
)

// ------------------------------------------------------------ lexer

type tokKind int

const (
	tName tokKind = iota
	tInt
	tString
	tPunct
	tSpread
)

type tok struct {
	k tokKind
	v string
	n int
}

func lex(src string) []tok {
	var toks []tok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '.' && i+2 < len(src) && src[i+1] == '.' && src[i+2] == '.':
			toks = append(toks, tok{k: tSpread, v: "..."})
			i += 3
		case strings.IndexByte("{}():!=$[]@", c) >= 0:
			toks = append(toks, tok{k: tPunct, v: string(c)})
			i++
		case c == '"':
			j := i + 1
			var out strings.Builder
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' && j+1 < len(src) {
					e := src[j+1]
					switch e {
					case 'n':
						out.WriteByte('\n')
					case 't':
						out.WriteByte('\t')
					case 'r':
						out.WriteByte('\r')
					default:
						out.WriteByte(e)
					}
					j += 2
				} else {
					out.WriteByte(src[j])
					j++
				}
			}
			toks = append(toks, tok{k: tString, v: out.String()})
			i = j + 1
		case (c >= '0' && c <= '9') || (c == '-' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i
			if src[j] == '-' {
				j++
			}
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(src[i:j])
			toks = append(toks, tok{k: tInt, n: n})
			i = j
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_':
			j := i
			for j < len(src) && ((src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= 'a' && src[j] <= 'z') || (src[j] >= '0' && src[j] <= '9') || src[j] == '_') {
				j++
			}
			toks = append(toks, tok{k: tName, v: src[i:j]})
			i = j
		default:
			i++
		}
	}
	return toks
}

// ------------------------------------------------------------ AST

type fieldNode struct {
	alias    string
	name     string
	args     map[string]any
	children []*fieldNode
	fragment string // "... on Type" -> type name ("" when none)
	hasFrag  bool
}

type operation struct {
	kind   string // "query" | "mutation"
	name   string
	fields []*fieldNode
}

type gqlParser struct {
	toks []tok
	pos  int
	vars map[string]any
}

func (p *gqlParser) peek() *tok {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *gqlParser) next() *tok {
	if p.pos < len(p.toks) {
		t := &p.toks[p.pos]
		p.pos++
		return t
	}
	return nil
}

func (p *gqlParser) expectPunct(v string) bool {
	t := p.next()
	return t != nil && t.k == tPunct && t.v == v
}

func (p *gqlParser) parseValue() (any, bool) {
	t := p.next()
	if t == nil {
		return nil, false
	}
	switch t.k {
	case tString:
		return t.v, true
	case tInt:
		return float64(t.n), true
	case tPunct:
		switch t.v {
		case "$":
			name := p.next()
			if name == nil || name.k != tName {
				return nil, false
			}
			if v, ok := p.vars[name.v]; ok {
				return v, true
			}
			return nil, true
		case "[":
			var arr []any
			for {
				pk := p.peek()
				if pk == nil {
					return nil, false
				}
				if pk.k == tPunct && pk.v == "]" {
					break
				}
				v, ok := p.parseValue()
				if !ok {
					return nil, false
				}
				arr = append(arr, v)
			}
			if !p.expectPunct("]") {
				return nil, false
			}
			return arr, true
		case "{":
			obj := make(map[string]any)
			for {
				pk := p.peek()
				if pk == nil {
					return nil, false
				}
				if pk.k == tPunct && pk.v == "}" {
					break
				}
				key := p.next()
				if key == nil || key.k != tName {
					return nil, false
				}
				if !p.expectPunct(":") {
					return nil, false
				}
				v, ok := p.parseValue()
				if !ok {
					return nil, false
				}
				obj[key.v] = v
			}
			if !p.expectPunct("}") {
				return nil, false
			}
			return obj, true
		}
	case tName:
		switch t.v {
		case "true":
			return true, true
		case "false":
			return false, true
		case "null":
			return nil, true
		default:
			return t.v, true // enum value: keep as string
		}
	}
	return nil, false
}

func (p *gqlParser) parseArgs() (map[string]any, bool) {
	args := make(map[string]any)
	pk := p.peek()
	if pk == nil || pk.k != tPunct || pk.v != "(" {
		return args, true
	}
	p.next()
	for {
		pk := p.peek()
		if pk == nil {
			return nil, false
		}
		if pk.k == tPunct && pk.v == ")" {
			break
		}
		key := p.next()
		if key == nil || key.k != tName {
			return nil, false
		}
		if !p.expectPunct(":") {
			return nil, false
		}
		v, ok := p.parseValue()
		if !ok {
			return nil, false
		}
		args[key.v] = v
	}
	if !p.expectPunct(")") {
		return nil, false
	}
	return args, true
}

func (p *gqlParser) parseSelectionSet() ([]*fieldNode, bool) {
	if !p.expectPunct("{") {
		return nil, false
	}
	var fields []*fieldNode
	for {
		pk := p.peek()
		if pk == nil {
			return nil, false
		}
		if pk.k == tPunct && pk.v == "}" {
			break
		}
		if pk.k == tSpread {
			p.next()
			onTok := p.next()
			fragType := ""
			hasFrag := false
			if onTok != nil && onTok.k == tName && onTok.v == "on" {
				typeTok := p.next()
				if typeTok != nil && typeTok.k == tName {
					fragType = typeTok.v
					hasFrag = true
				}
			} else if onTok != nil && onTok.k == tName {
				// Named fragment spread; unsupported in the checked documents.
				fragType = onTok.v
				hasFrag = true
			}
			children, ok := p.parseSelectionSet()
			if !ok {
				return nil, false
			}
			for _, c := range children {
				c.fragment = fragType
				c.hasFrag = hasFrag
				fields = append(fields, c)
			}
			continue
		}
		nameTok := p.next()
		if nameTok == nil || nameTok.k != tName {
			return nil, false
		}
		alias := ""
		name := nameTok.v
		if pk2 := p.peek(); pk2 != nil && pk2.k == tPunct && pk2.v == ":" {
			p.next()
			alias = name
			real := p.next()
			if real == nil || real.k != tName {
				return nil, false
			}
			name = real.v
		}
		args, ok := p.parseArgs()
		if !ok {
			return nil, false
		}
		var children []*fieldNode
		if pk2 := p.peek(); pk2 != nil && pk2.k == tPunct && pk2.v == "{" {
			children, ok = p.parseSelectionSet()
			if !ok {
				return nil, false
			}
		}
		fields = append(fields, &fieldNode{alias: alias, name: name, args: args, children: children})
	}
	if !p.expectPunct("}") {
		return nil, false
	}
	return fields, true
}

func (p *gqlParser) parseOperation() (*operation, bool) {
	op := &operation{kind: "query"}
	t := p.peek()
	if t != nil && t.k == tName && (t.v == "query" || t.v == "mutation") {
		op.kind = t.v
		p.next()
		if n := p.peek(); n != nil && n.k == tName {
			op.name = n.v
			p.next()
		}
	}
	// Variable definitions ($x: Type!) — skip them, values come from vars.
	if pk := p.peek(); pk != nil && pk.k == tPunct && pk.v == "(" {
		depth := 0
		for {
			tok := p.next()
			if tok == nil {
				return nil, false
			}
			if tok.k == tPunct && tok.v == "(" {
				depth++
			}
			if tok.k == tPunct && tok.v == ")" {
				depth--
				if depth == 0 {
					break
				}
			}
		}
	}
	fields, ok := p.parseSelectionSet()
	if !ok {
		return nil, false
	}
	op.fields = fields
	return op, true
}

func parseDocument(query string, vars map[string]any) (*operation, bool) {
	p := &gqlParser{toks: lex(query), vars: vars}
	return p.parseOperation()
}

// ------------------------------------------------------------ introspection data

var typeFields = map[string][]string{
	"Issue":       {"id", "number", "title", "state", "body", "createdAt", "updatedAt", "closedAt", "author", "labels", "comments", "url"},
	"PullRequest": {"id", "number", "title", "state", "merged", "headRefName", "baseRefName", "mergeable", "files", "headRefOid", "baseRefOid", "body", "createdAt", "updatedAt"},
	"Repository":  {"id", "databaseId", "name", "nameWithOwner", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequests", "pullRequest", "description", "isPrivate", "url", "createdAt", "updatedAt"},
	"SearchType":  {},
	"User":        {"login", "id", "name", "email"},
	"IssueConnection":      {"nodes", "pageInfo", "totalCount"},
	"PullRequestConnection": {"nodes", "pageInfo", "totalCount"},
	"PageInfo":             {"hasNextPage", "endCursor", "hasPreviousPage", "startCursor"},
}

var searchTypeValues = []string{"ISSUE", "REPOSITORY", "USER", "DISCUSSION", "COMMIT", "CODE"}

// ------------------------------------------------------------ executor

func gqlErr(message string) map[string]any {
	return map[string]any{"message": message}
}

func argInt(args map[string]any, k string) (int, bool) {
	f, ok := args[k].(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func argStr(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

func issueGql(r *repo, is *issue) map[string]any {
	return map[string]any{
		"id":     globalID("Issue", intStr(r.dbID)+":"+intStr(is.number)),
		"number": is.number,
		"title":  is.title,
		"state":  strings.ToUpper(is.state),
		"body":   is.body,
	}
}

func prGql(r *repo, pr *pull) map[string]any {
	headSha := r.refs["refs/heads/"+pr.head]
	baseSha := r.refs["refs/heads/"+pr.base]
	state := strings.ToUpper(pr.state)
	if pr.merged {
		state = "MERGED"
	}
	mergeable := "MERGEABLE"
	if len(mergeConflicts(r, baseSha, headSha)) > 0 {
		mergeable = "CONFLICTING"
	}
	return map[string]any{
		"id":          globalID("PullRequest", intStr(r.dbID)+":"+intStr(pr.number)),
		"number":      pr.number,
		"state":       state,
		"merged":      pr.merged,
		"headRefName": pr.head,
		"baseRefName": pr.base,
		"mergeable":   mergeable,
		"title":       pr.title,
		"body":        pr.body,
	}
}

func repoGql(r *repo) map[string]any {
	var dbr any
	if _, ok := r.refs["refs/heads/"+r.defaultBranch]; ok {
		dbr = map[string]any{"name": r.defaultBranch}
	}
	return map[string]any{
		"__repo":             r,
		"id":                 globalID("Repository", intStr(r.dbID)),
		"databaseId":         r.dbID,
		"name":               r.name,
		"nameWithOwner":      r.owner + "/" + r.name,
		"owner":              map[string]any{"login": r.owner},
		"defaultBranchRef":   dbr,
		"viewerPermission":   "ADMIN",
		"hasIssuesEnabled":   true,
		"description":        r.description,
		"isPrivate":          r.isPrivate,
	}
}

func issuesConnection(r *repo, args map[string]any) map[string]any {
	var list []*issue
	for _, is := range r.issues {
		list = append(list, is)
	}
	if states, ok := args["states"].([]any); ok && len(states) > 0 {
		want := make(map[string]bool)
		for _, s := range states {
			if str, ok := s.(string); ok {
				want[strings.ToLower(str)] = true
			}
		}
		var filtered []*issue
		for _, is := range list {
			if want[is.state] {
				filtered = append(filtered, is)
			}
		}
		list = filtered
	}
	dir := "DESC"
	if ob, ok := args["orderBy"].(map[string]any); ok {
		if d, ok := ob["direction"].(string); ok {
			dir = strings.ToUpper(d)
		}
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			less := false
			if dir == "ASC" {
				less = list[j].seq < list[j-1].seq || (list[j].seq == list[j-1].seq && list[j].number < list[j-1].number)
			} else {
				less = list[j].seq > list[j-1].seq || (list[j].seq == list[j-1].seq && list[j].number > list[j-1].number)
			}
			if !less {
				break
			}
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	if after, ok := args["after"].(string); ok && after != "" {
		if decoded := b64decode(after); decoded != nil {
			text := string(decoded)
			if strings.HasPrefix(text, "cursor:") {
				if n, err := strconv.Atoi(text[7:]); err == nil {
					idx := -1
					for i, is := range list {
						if is.number == n {
							idx = i
							break
						}
					}
					if idx >= 0 {
						list = list[idx+1:]
					}
				}
			}
		}
	}
	first := 100
	if f, ok := argInt(args, "first"); ok {
		first = f
	}
	page := list
	if len(page) > first {
		page = page[:first]
	}
	hasNext := len(list) > first
	var endCursor any
	if len(page) > 0 {
		endCursor = b64encodeStr("cursor:" + intStr(page[len(page)-1].number))
	}
	return map[string]any{
		"nodes":      nodesOf(r, page),
		"pageInfo":   map[string]any{"hasNextPage": hasNext, "endCursor": endCursor},
		"totalCount": len(list),
	}
}

func nodesOf(r *repo, page []*issue) []any {
	out := make([]any, 0, len(page))
	for _, is := range page {
		out = append(out, issueGql(r, is))
	}
	return out
}

func prFiles(r *repo, pr *pull, first int) map[string]any {
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
	paths := make(map[string]bool)
	for p := range headFlat {
		paths[p] = true
	}
	for p := range baseFlat {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sortStrings(sorted)
	var nodes []any
	for _, p := range sorted {
		h, hasH := headFlat[p]
		b, hasB := baseFlat[p]
		if hasH && (!hasB || b.oid != h.oid) {
			nodes = append(nodes, map[string]any{"path": p})
		} else if !hasH && hasB {
			nodes = append(nodes, map[string]any{"path": p})
		}
	}
	if len(nodes) > first {
		nodes = nodes[:first]
	}
	return map[string]any{"nodes": nodes}
}

func resolveRepoFields(r *repo, repoObj map[string]any, fields []*fieldNode, errors *[]map[string]any) map[string]any {
	out := make(map[string]any)
	for _, f := range fields {
		if f.hasFrag && f.fragment != "Repository" {
			continue
		}
		key := f.alias
		if key == "" {
			key = f.name
		}
		switch f.name {
		case "id", "databaseId", "name", "nameWithOwner", "viewerPermission", "hasIssuesEnabled", "description", "isPrivate":
			out[key] = repoObj[f.name]
		case "owner":
			owner, _ := repoObj["owner"].(map[string]any)
			out[key] = pick(owner, f.children, owner, errors, "User")
		case "defaultBranchRef":
			dbr, _ := repoObj["defaultBranchRef"].(map[string]any)
			if dbr == nil {
				out[key] = nil
			} else {
				out[key] = pick(dbr, f.children, dbr, errors, "Ref")
			}
		case "issue":
			num, _ := argInt(f.args, "number")
			is := r.issues[num]
			if is == nil {
				out[key] = nil
			} else {
				g := issueGql(r, is)
				out[key] = pick(g, f.children, g, errors, "Issue")
			}
		case "issues":
			out[key] = pickConn(issuesConnection(r, f.args), f.children, errors)
		case "pullRequests":
			first := 100
			if n, ok := argInt(f.args, "first"); ok {
				first = n
			}
			var list []*pull
			for _, pr := range r.pulls {
				list = append(list, pr)
			}
			for i := 1; i < len(list); i++ {
				for j := i; j > 0 && list[j].seq > list[j-1].seq; j-- {
					list[j], list[j-1] = list[j-1], list[j]
				}
			}
			if len(list) > first {
				list = list[:first]
			}
			nodes := make([]any, 0, len(list))
			for _, pr := range list {
				nodes = append(nodes, prGql(r, pr))
			}
			out[key] = pickConn(map[string]any{"nodes": nodes, "totalCount": len(list)}, f.children, errors, "PullRequest")
		case "pullRequest":
			num, _ := argInt(f.args, "number")
			pr := r.pulls[num]
			if pr == nil {
				out[key] = nil
				break
			}
			base := prGql(r, pr)
			obj := make(map[string]any)
			for _, c := range f.children {
				if c.name == "files" {
					first := 100
					if n, ok := argInt(c.args, "first"); ok {
						first = n
					}
					ck := c.alias
					if ck == "" {
						ck = "files"
					}
					obj[ck] = prFiles(r, pr, first)
				} else {
					ck := c.alias
					if ck == "" {
						ck = c.name
					}
					obj[ck] = base[c.name]
				}
			}
			out[key] = obj
		default:
			*errors = append(*errors, gqlErr("Cannot query field \""+f.name+"\" on type \"Repository\"."))
			out[key] = nil
		}
	}
	return out
}

// pick returns only the requested subfields from a plain object value.
func pick(value map[string]any, children []*fieldNode, fallback map[string]any, errors *[]map[string]any, typeName string) map[string]any {
	if len(children) == 0 {
		return value
	}
	out := make(map[string]any)
	for _, c := range children {
		if c.hasFrag {
			continue
		}
		key := c.alias
		if key == "" {
			key = c.name
		}
		v, ok := value[c.name]
		if !ok {
			if errors != nil && typeName != "" {
				*errors = append(*errors, gqlErr("Cannot query field \""+c.name+"\" on type \""+typeName+"\"."))
			}
			out[key] = nil
			continue
		}
		if len(c.children) > 0 {
			if vm, ok := v.(map[string]any); ok && vm != nil {
				out[key] = pick(vm, c.children, vm, errors, typeName)
			} else {
				out[key] = v
			}
		} else {
			if v == nil {
				out[key] = nil
			} else {
				out[key] = v
			}
		}
	}
	return out
}

func pickConn(conn map[string]any, children []*fieldNode, errors *[]map[string]any, nodeType ...string) map[string]any {
	nt := "Issue"
	if len(nodeType) > 0 {
		nt = nodeType[0]
	}
	out := make(map[string]any)
	for _, c := range children {
		key := c.alias
		if key == "" {
			key = c.name
		}
		switch c.name {
		case "nodes":
			nodes, _ := conn["nodes"].([]any)
			mapped := make([]any, 0, len(nodes))
			for _, n := range nodes {
				if nm, ok := n.(map[string]any); ok {
					mapped = append(mapped, pick(nm, c.children, nm, errors, nt))
				} else {
					mapped = append(mapped, n)
				}
			}
			out[key] = mapped
		case "pageInfo":
			pi, _ := conn["pageInfo"].(map[string]any)
			out[key] = pick(pi, c.children, pi, errors, "PageInfo")
		default:
			out[key] = conn[c.name]
		}
	}
	return out
}

func resolveTypeFields(name string, fields []*fieldNode) map[string]any {
	out := make(map[string]any)
	if name == "SearchType" {
		for _, f := range fields {
			key := f.alias
			if key == "" {
				key = f.name
			}
			switch f.name {
			case "enumValues":
				vals := make([]any, 0, len(searchTypeValues))
				for _, v := range searchTypeValues {
					vals = append(vals, map[string]any{"name": v})
				}
				out[key] = vals
			case "name":
				out[key] = "SearchType"
			case "fields":
				out[key] = []any{}
			default:
				out[key] = nil
			}
		}
		return out
	}
	known, ok := typeFields[name]
	if !ok {
		for _, f := range fields {
			key := f.alias
			if key == "" {
				key = f.name
			}
			out[key] = nil
		}
		return out
	}
	for _, f := range fields {
		key := f.alias
		if key == "" {
			key = f.name
		}
		switch f.name {
		case "fields":
			vals := make([]any, 0, len(known))
			for _, n := range known {
				vals = append(vals, map[string]any{"name": n})
			}
			out[key] = vals
		case "enumValues":
			out[key] = []any{}
		case "name":
			out[key] = name
		default:
			out[key] = nil
		}
	}
	return out
}

func findRepoByID(s *store, dbID int) *repo {
	for _, r := range s.repos {
		if r.dbID == dbID {
			return r
		}
	}
	return nil
}

func executeOperation(s *store, op *operation) (data map[string]any, errors []map[string]any) {
	data = make(map[string]any)
	for _, f := range op.fields {
		key := f.alias
		if key == "" {
			key = f.name
		}
		if op.kind == "query" {
			switch f.name {
			case "viewer":
				data[key] = pick(map[string]any{"login": viewerLogin}, f.children, map[string]any{"login": viewerLogin}, &errors, "User")
			case "repository":
				owner := argStr(f.args, "owner")
				name := argStr(f.args, "name")
				var r *repo
				if owner == viewerLogin {
					r = s.getRepo(owner, name)
				}
				if r == nil {
					data[key] = nil
					continue
				}
				data[key] = resolveRepoFields(r, repoGql(r), f.children, &errors)
			case "node":
				id := argStr(f.args, "id")
				kind, rest, ok := decodeGlobalID(id)
				if !ok || kind != "Repository" {
					data[key] = nil
					continue
				}
				dbID, err := strconv.Atoi(rest)
				if err != nil {
					data[key] = nil
					continue
				}
				found := findRepoByID(s, dbID)
				if found == nil {
					data[key] = nil
					continue
				}
				obj := map[string]any{"id": id}
				for _, c := range f.children {
					if c.hasFrag && c.fragment != "Repository" {
						continue
					}
					ck := c.alias
					if ck == "" {
						ck = c.name
					}
					switch c.name {
					case "name":
						obj[ck] = found.name
					case "nameWithOwner":
						obj[ck] = found.owner + "/" + found.name
					case "id":
						obj[ck] = id
					default:
						obj[ck] = nil
					}
				}
				data[key] = obj
			case "__type":
				data[key] = resolveTypeFields(argStr(f.args, "name"), f.children)
			default:
				errors = append(errors, gqlErr("Cannot query field \""+f.name+"\" on type \"Query\"."))
				data[key] = nil
			}
		} else {
			// Mutations.
			switch f.name {
			case "createIssue":
				input, _ := f.args["input"].(map[string]any)
				kind, rest, ok := decodeGlobalID(getStr(input, "repositoryId"))
				var r *repo
				if ok && kind == "Repository" {
					if dbID, err := strconv.Atoi(rest); err == nil {
						r = findRepoByID(s, dbID)
					}
				}
				if r == nil {
					errors = append(errors, gqlErr("Could not resolve to a Repository with the global id."))
					data[key] = nil
					continue
				}
				r.counter++
				is := &issue{number: r.counter, title: getStr(input, "title"), body: getStr(input, "body"), state: "open", seq: s.nextSeq()}
				r.issues[is.number] = is
				g := issueGql(r, is)
				child := map[string]any{}
				if len(f.children) > 0 {
					child = pick(g, f.children[0].children, g, &errors, "Issue")
				}
				data[key] = map[string]any{"issue": child}
			case "closeIssue":
				input, _ := f.args["input"].(map[string]any)
				kind, rest, ok := decodeGlobalID(getStr(input, "issueId"))
				var foundR *repo
				var foundI *issue
				if ok && kind == "Issue" {
					parts := strings.SplitN(rest, ":", 2)
					if len(parts) == 2 {
						dbID, e1 := strconv.Atoi(parts[0])
						num, e2 := strconv.Atoi(parts[1])
						if e1 == nil && e2 == nil {
							for _, rr := range s.repos {
								if rr.dbID == dbID {
									if is := rr.issues[num]; is != nil {
										foundR, foundI = rr, is
									}
								}
							}
						}
					}
				}
				if foundI == nil {
					errors = append(errors, gqlErr("Could not resolve to an Issue with the global id."))
					data[key] = nil
					continue
				}
				foundI.state = "closed"
				g := issueGql(foundR, foundI)
				child := map[string]any{}
				if len(f.children) > 0 {
					child = pick(g, f.children[0].children, g, &errors, "Issue")
				}
				data[key] = map[string]any{"issue": child}
			case "createPullRequest":
				input, _ := f.args["input"].(map[string]any)
				kind, rest, ok := decodeGlobalID(getStr(input, "repositoryId"))
				var r *repo
				if ok && kind == "Repository" {
					if dbID, err := strconv.Atoi(rest); err == nil {
						r = findRepoByID(s, dbID)
					}
				}
				if r == nil {
					errors = append(errors, gqlErr("Could not resolve to a Repository with the global id."))
					data[key] = nil
					continue
				}
				head := getStr(input, "headRefName")
				base := getStr(input, "baseRefName")
				if base == "" {
					base = r.defaultBranch
				}
				if _, ok := r.refs["refs/heads/"+head]; !ok {
					errors = append(errors, gqlErr("Head or base ref does not exist."))
					data[key] = nil
					continue
				}
				if _, ok := r.refs["refs/heads/"+base]; !ok {
					errors = append(errors, gqlErr("Head or base ref does not exist."))
					data[key] = nil
					continue
				}
				r.counter++
				pr := &pull{number: r.counter, title: getStr(input, "title"), state: "open", head: head, base: base, seq: s.nextSeq()}
				r.pulls[pr.number] = pr
				g := prGql(r, pr)
				child := map[string]any{}
				if len(f.children) > 0 {
					child = pick(g, f.children[0].children, g, &errors, "PullRequest")
				}
				data[key] = map[string]any{"pullRequest": child}
			case "mergePullRequest":
				input, _ := f.args["input"].(map[string]any)
				kind, rest, ok := decodeGlobalID(getStr(input, "pullRequestId"))
				var foundR *repo
				var foundP *pull
				if ok && kind == "PullRequest" {
					parts := strings.SplitN(rest, ":", 2)
					if len(parts) == 2 {
						dbID, e1 := strconv.Atoi(parts[0])
						num, e2 := strconv.Atoi(parts[1])
						if e1 == nil && e2 == nil {
							for _, rr := range s.repos {
								if rr.dbID == dbID {
									if pr := rr.pulls[num]; pr != nil {
										foundR, foundP = rr, pr
									}
								}
							}
						}
					}
				}
				if foundP == nil {
					errors = append(errors, gqlErr("Could not resolve to a PullRequest with the global id."))
					data[key] = nil
					continue
				}
				if foundP.state != "open" {
					errors = append(errors, gqlErr("Pull request is not open."))
					data[key] = nil
					continue
				}
				headSha := foundR.refs["refs/heads/"+foundP.head]
				baseSha := foundR.refs["refs/heads/"+foundP.base]
				if len(mergeConflicts(foundR, baseSha, headSha)) > 0 {
					errors = append(errors, gqlErr("Merge conflict: cannot merge."))
					data[key] = nil
					continue
				}
				headCommit := foundR.objects[headSha]
				headTree := ""
				if headCommit != nil && headCommit.typ == objCommit {
					headTree = parseCommit(headCommit.data).tree
				}
				mergeSha := buildCommit(foundR, headTree, []string{baseSha}, "Merge pull request #"+intStr(foundP.number)+" from "+foundR.owner+"/"+foundP.head, fixedEpochSec)
				foundR.refs["refs/heads/"+foundP.base] = mergeSha
				foundP.state = "closed"
				foundP.merged = true
				foundP.mergedAt = fixedISO
				foundP.mergeSha = mergeSha
				g := prGql(foundR, foundP)
				child := map[string]any{}
				if len(f.children) > 0 {
					child = pick(g, f.children[0].children, g, &errors, "PullRequest")
				}
				data[key] = map[string]any{"pullRequest": child}
			case "addComment":
				input, _ := f.args["input"].(map[string]any)
				kind, rest, ok := decodeGlobalID(getStr(input, "subjectId"))
				var foundR *repo
				var foundI *issue
				if ok && kind == "Issue" {
					parts := strings.SplitN(rest, ":", 2)
					if len(parts) == 2 {
						dbID, e1 := strconv.Atoi(parts[0])
						num, e2 := strconv.Atoi(parts[1])
						if e1 == nil && e2 == nil {
							for _, rr := range s.repos {
								if rr.dbID == dbID {
									if is := rr.issues[num]; is != nil {
										foundR, foundI = rr, is
									}
								}
							}
						}
					}
				}
				if foundI == nil {
					errors = append(errors, gqlErr("Could not resolve to an Issue with the global id."))
					data[key] = nil
					continue
				}
				foundR.commentCounter++
				c := &comment{id: foundR.commentCounter, body: getStr(input, "body"), seq: s.nextSeq()}
				foundI.comments = append(foundI.comments, c)
				node := map[string]any{
					"id":   globalID("IssueComment", intStr(foundR.dbID)+":"+intStr(foundI.number)+":"+intStr(c.id)),
					"body": c.body,
				}
				var inner map[string]any
				if len(f.children) > 0 && len(f.children[0].children) > 0 {
					inner = pick(node, f.children[0].children[0].children, node, &errors, "IssueComment")
				} else {
					inner = node
				}
				data[key] = map[string]any{"commentEdge": map[string]any{"node": inner}}
			default:
				errors = append(errors, gqlErr("Cannot query field \""+f.name+"\" on type \"Mutation\"."))
				data[key] = nil
			}
		}
	}
	return data, errors
}

func handleGraphQL(s *store, body any) (int, []byte) {
	runOne := func(doc map[string]any) map[string]any {
		query, _ := doc["query"].(string)
		vars, _ := doc["variables"].(map[string]any)
		if vars == nil {
			vars = make(map[string]any)
		}
		op, ok := parseDocument(query, vars)
		if !ok {
			return map[string]any{"data": nil, "errors": []any{gqlErr("GraphQL parse error")}}
		}
		data, errs := executeOperation(s, op)
		out := map[string]any{"data": data}
		if len(errs) > 0 {
			errList := make([]any, 0, len(errs))
			for _, e := range errs {
				errList = append(errList, e)
			}
			out["errors"] = errList
		}
		return out
	}
	var w jw
	if arr, ok := body.([]any); ok {
		w.b = append(w.b, '[')
		for i, d := range arr {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			doc, _ := d.(map[string]any)
			w.writeAny(runOne(doc))
		}
		w.b = append(w.b, ']')
		return 200, w.bytes()
	}
	if m, ok := body.(map[string]any); ok {
		w.writeAny(runOne(m))
		return 200, w.bytes()
	}
	w.writeAny(map[string]any{"data": nil, "errors": []any{gqlErr("Request body must be a JSON object.")}})
	return 200, w.bytes()
}
