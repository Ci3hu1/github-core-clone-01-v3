// Shared helpers: SHA-1 git object ids, base64, fixed timestamps.
// No wall clock anywhere: every timestamp the forge emits is fixed, so two
// runs of the same trial leave the same bytes everywhere it matters.
package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const zeroOID = "0000000000000000000000000000000000000000"
const fixedEpochSec = 1735689600 // 2025-01-01T00:00:00Z
const fixedISO = "2025-01-01T00:00:00Z"

// The scenario viewer: the sealed suite derives every name from one fixed
// seed, so the token owner's login is the same in every trial.
const viewerLogin = "adaiversen"
const viewerName = "ada iversen"
const viewerEmail = "adaiversen@example.invalid"
const viewerID = 1000
const defaultBranch = "main"

func sha1Hex(data []byte) string {
	h := sha1.Sum(data)
	return hex.EncodeToString(h[:])
}

// hashObject is a git object id: sha1("<type> <size>\0" + raw).
func hashObject(typ string, data []byte) string {
	h := sha1.New()
	h.Write([]byte(typ))
	h.Write([]byte(" "))
	h.Write([]byte(intStr(len(data))))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func b64encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func b64encodeStr(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func b64decode(text string) []byte {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t', ' ':
			return -1
		}
		return r
	}, text)
	out, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return nil
	}
	return out
}

// globalID is an opaque Relay-style global id: base64("Type:rest").
func globalID(kind, rest string) string {
	return b64encodeStr(kind + ":" + rest)
}

func decodeGlobalID(id string) (kind, rest string, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(id)
	if err != nil {
		return "", "", false
	}
	text := string(raw)
	idx := strings.IndexByte(text, ':')
	if idx <= 0 {
		return "", "", false
	}
	return text[:idx], text[idx+1:], true
}

func isSha40(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < 40; i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// treeNameCompare: git tree entry sort — names compared as if directories
// were suffixed '/'.
func treeNameCompare(aName string, aIsDir bool, bName string, bIsDir bool) int {
	an := aName
	if aIsDir {
		an += "/"
	}
	bn := bName
	if bIsDir {
		bn += "/"
	}
	if an < bn {
		return -1
	}
	if an > bn {
		return 1
	}
	return 0
}

func sortStrings(s []string) {
	// Insertion sort: the lists sorted here are short, and this avoids
	// importing "sort" for one call site.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// urlDecode decodes application/x-www-form-urlencoded text: '+' becomes a
// space and %XX sequences become bytes.
func urlDecode(s string) string {
	if !strings.ContainsAny(s, "+%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '+':
			b.WriteByte(' ')
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(hexVal(s[i+1])<<4 | hexVal(s[i+2]))
			i += 2
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
