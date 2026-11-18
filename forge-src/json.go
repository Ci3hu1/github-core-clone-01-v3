// JSON: a small generic parser for request bodies and a fast,
// allocation-light writer for responses. No encoding/json anywhere:
// the writer emits fields in a fixed order with a fast path for strings
// that need no escaping, which is the common case on the hot paths.
package main

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ---------------------------------------------------------------- parser

type jparser struct {
	b   []byte
	pos int
}

func parseJSON(b []byte) (any, bool) {
	if len(b) == 0 {
		return nil, false
	}
	p := &jparser{b: b}
	p.ws()
	v, ok := p.value()
	if !ok {
		return nil, false
	}
	p.ws()
	return v, p.pos == len(p.b)
}

func (p *jparser) ws() {
	for p.pos < len(p.b) {
		switch p.b[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jparser) value() (any, bool) {
	if p.pos >= len(p.b) {
		return nil, false
	}
	switch c := p.b[p.pos]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		s, ok := p.str()
		return s, ok
	case c == 't':
		return p.lit("true", true)
	case c == 'f':
		return p.lit("false", false)
	case c == 'n':
		return p.lit("null", nil)
	default:
		return p.number()
	}
}

func (p *jparser) lit(word string, v any) (any, bool) {
	if p.pos+len(word) > len(p.b) || string(p.b[p.pos:p.pos+len(word)]) != word {
		return nil, false
	}
	p.pos += len(word)
	return v, true
}

func (p *jparser) number() (any, bool) {
	start := p.pos
	if p.pos < len(p.b) && p.b[p.pos] == '-' {
		p.pos++
	}
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			p.pos++
		} else {
			break
		}
	}
	if p.pos == start {
		return nil, false
	}
	f, err := strconv.ParseFloat(string(p.b[start:p.pos]), 64)
	if err != nil {
		return nil, false
	}
	return f, true
}

func (p *jparser) str() (string, bool) {
	// p.b[p.pos] == '"'
	p.pos++
	start := p.pos
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		if c == '"' {
			s := string(p.b[start:p.pos])
			p.pos++
			return s, true
		}
		if c == '\\' {
			return p.strSlow(start)
		}
		if c < 0x20 {
			return "", false
		}
		p.pos++
	}
	return "", false
}

func (p *jparser) strSlow(start int) (string, bool) {
	// Re-scan from start, handling escapes. start is the first byte after '"'.
	out := make([]byte, 0, 32)
	out = append(out, p.b[start:p.pos]...)
	for p.pos < len(p.b) {
		c := p.b[p.pos]
		if c == '"' {
			p.pos++
			return string(out), true
		}
		if c == '\\' {
			p.pos++
			if p.pos >= len(p.b) {
				return "", false
			}
			e := p.b[p.pos]
			p.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 't':
				out = append(out, '\t')
			case 'r':
				out = append(out, '\r')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '/':
				out = append(out, '/')
			case '\\':
				out = append(out, '\\')
			case '"':
				out = append(out, '"')
			case 'u':
				if p.pos+4 > len(p.b) {
					return "", false
				}
				r1, ok := p.hex4()
				if !ok {
					return "", false
				}
				if utf16.IsSurrogate(r1) {
					if p.pos+6 <= len(p.b) && p.b[p.pos] == '\\' && p.b[p.pos+1] == 'u' {
						p.pos += 2
						r2, ok := p.hex4()
						if !ok {
							return "", false
						}
						out = utf8.AppendRune(out, utf16.DecodeRune(r1, r2))
					} else {
						out = utf8.AppendRune(out, utf8.RuneError)
					}
				} else {
					out = utf8.AppendRune(out, r1)
				}
			default:
				return "", false
			}
			continue
		}
		if c < 0x20 {
			return "", false
		}
		out = append(out, c)
		p.pos++
	}
	return "", false
}

func (p *jparser) hex4() (rune, bool) {
	if p.pos+4 > len(p.b) {
		return 0, false
	}
	var v int
	for i := 0; i < 4; i++ {
		c := p.b[p.pos+i]
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
		v = v*16 + d
	}
	p.pos += 4
	return rune(v), true
}

func (p *jparser) object() (any, bool) {
	// p.b[p.pos] == '{'
	p.pos++
	out := make(map[string]any)
	p.ws()
	if p.pos < len(p.b) && p.b[p.pos] == '}' {
		p.pos++
		return out, true
	}
	for {
		p.ws()
		if p.pos >= len(p.b) || p.b[p.pos] != '"' {
			return nil, false
		}
		k, ok := p.str()
		if !ok {
			return nil, false
		}
		p.ws()
		if p.pos >= len(p.b) || p.b[p.pos] != ':' {
			return nil, false
		}
		p.pos++
		p.ws()
		v, ok := p.value()
		if !ok {
			return nil, false
		}
		out[k] = v
		p.ws()
		if p.pos >= len(p.b) {
			return nil, false
		}
		switch p.b[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return out, true
		default:
			return nil, false
		}
	}
}

func (p *jparser) array() (any, bool) {
	p.pos++
	out := make([]any, 0, 4)
	p.ws()
	if p.pos < len(p.b) && p.b[p.pos] == ']' {
		p.pos++
		return out, true
	}
	for {
		p.ws()
		v, ok := p.value()
		if !ok {
			return nil, false
		}
		out = append(out, v)
		p.ws()
		if p.pos >= len(p.b) {
			return nil, false
		}
		switch p.b[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, true
		default:
			return nil, false
		}
	}
}

// Typed accessors over parsed values.

func asObj(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asArr(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

func asStr(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func getStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func getFloat(m map[string]any, k string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	f, ok := m[k].(float64)
	return f, ok
}

func getInt(m map[string]any, k string) (int, bool) {
	f, ok := getFloat(m, k)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func getBool(m map[string]any, k string) (bool, bool) {
	if m == nil {
		return false, false
	}
	b, ok := m[k].(bool)
	return b, ok
}

// ---------------------------------------------------------------- writer

type jw struct {
	b []byte
}

func (w *jw) reset() { w.b = w.b[:0] }

func (w *jw) bytes() []byte { return w.b }

func (w *jw) raw(s string) { w.b = append(w.b, s...) }

func (w *jw) byte(c byte) { w.b = append(w.b, c) }

// str appends s as a quoted JSON string, with a fast path when nothing
// needs escaping (the common case: ids, logins, titles from the scenario).
func (w *jw) str(s string) {
	w.b = append(w.b, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '"' || c == '\\' {
			w.b = append(w.b, s[start:i]...)
			switch c {
			case '"':
				w.b = append(w.b, '\\', '"')
			case '\\':
				w.b = append(w.b, '\\', '\\')
			case '\n':
				w.b = append(w.b, '\\', 'n')
			case '\t':
				w.b = append(w.b, '\\', 't')
			case '\r':
				w.b = append(w.b, '\\', 'r')
			case '\b':
				w.b = append(w.b, '\\', 'b')
			case '\f':
				w.b = append(w.b, '\\', 'f')
			default:
				w.b = append(w.b, '\\', 'u', '0', '0')
				w.b = append(w.b, hexDigit(c>>4), hexDigit(c&0xf))
			}
			start = i + 1
		}
	}
	w.b = append(w.b, s[start:]...)
	w.b = append(w.b, '"')
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + n - 10
}

func (w *jw) num(n int) {
	w.b = strconv.AppendInt(w.b, int64(n), 10)
}

func (w *jw) boolean(v bool) {
	if v {
		w.b = append(w.b, "true"...)
	} else {
		w.b = append(w.b, "false"...)
	}
}

func (w *jw) null() { w.b = append(w.b, "null"...) }
// obj is a small ordered-object builder: fields are written in the order
// the caller adds them, comma handling is automatic.
type jobj struct {
	w     *jw
	first bool
}

func (w *jw) obj() *jobj {
	w.b = append(w.b, '{')
	return &jobj{w: w, first: true}
}

func (o *jobj) sep() {
	if o.first {
		o.first = false
	} else {
		o.w.b = append(o.w.b, ',')
	}
}

func (o *jobj) key(k string) {
	o.sep()
	o.w.str(k)
	o.w.b = append(o.w.b, ':')
}

func (o *jobj) s(k, v string) {
	o.key(k)
	o.w.str(v)
}

func (o *jobj) n(k string, v int) {
	o.key(k)
	o.w.num(v)
}

func (o *jobj) b(k string, v bool) {
	o.key(k)
	o.w.boolean(v)
}

func (o *jobj) null(k string) {
	o.key(k)
	o.w.null()
}

func (o *jobj) raw(k string, v []byte) {
	o.key(k)
	o.w.b = append(o.w.b, v...)
}

func (o *jobj) obj(k string, fn func(o *jobj)) {
	o.key(k)
	sub := o.w.obj()
	fn(sub)
	o.w.b = append(o.w.b, '}')
}

func (o *jobj) arr(k string, fn func(a *jarr)) {
	o.key(k)
	arr := o.w.arr()
	fn(arr)
	o.w.b = append(o.w.b, ']')
}

func (o *jobj) end() {
	o.w.b = append(o.w.b, '}')
}

type jarr struct {
	w     *jw
	first bool
}

func (w *jw) arr() *jarr {
	w.b = append(w.b, '[')
	return &jarr{w: w, first: true}
}

func (a *jarr) sep() {
	if a.first {
		a.first = false
	} else {
		a.w.b = append(a.w.b, ',')
	}
}

func (a *jarr) s(v string) {
	a.sep()
	a.w.str(v)
}

func (a *jarr) n(v int) {
	a.sep()
	a.w.num(v)
}

func (a *jarr) boolean(v bool) {
	a.sep()
	a.w.boolean(v)
}

func (a *jarr) null() {
	a.sep()
	a.w.null()
}

func (a *jarr) raw(v []byte) {
	a.sep()
	a.w.b = append(a.w.b, v...)
}

func (a *jarr) obj(fn func(o *jobj)) {
	a.sep()
	sub := a.w.obj()
	fn(sub)
	a.w.b = append(a.w.b, '}')
}

func (a *jarr) end() {
	a.w.b = append(a.w.b, ']')
}

// writeAny serializes a parsed generic value (used for echoing structured
// content and for values built as map[string]any in a few places).
func (w *jw) writeAny(v any) {
	switch t := v.(type) {
	case nil:
		w.null()
	case bool:
		w.boolean(t)
	case float64:
		if t == float64(int64(t)) {
			w.num(int(t))
		} else {
			w.b = strconv.AppendFloat(w.b, t, 'g', -1, 64)
		}
	case int:
		w.num(t)
	case string:
		w.str(t)
	case []any:
		w.b = append(w.b, '[')
		for i, e := range t {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			w.writeAny(e)
		}
		w.b = append(w.b, ']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			if strings.HasPrefix(k, "__") {
				continue
			}
			keys = append(keys, k)
		}
		sortStrings(keys)
		w.b = append(w.b, '{')
		for i, k := range keys {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			w.str(k)
			w.b = append(w.b, ':')
			w.writeAny(t[k])
		}
		w.b = append(w.b, '}')
	default:
		w.null()
	}
}

