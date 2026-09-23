// Package posjson parses JSON while retaining source positions so diagnostics
// and single-source mutations can point at the exact fragment that mattered.
package posjson

import (
	"fmt"
)

type Pos struct {
	Offset int `json:"offset"`
	Line   int `json:"line"`
	Col    int `json:"col"`
}

type Kind string

const (
	Object Kind = "object"
	Array  Kind = "array"
	String Kind = "string"
	Number Kind = "number"
	Bool   Kind = "bool"
	Null   Kind = "null"
)

type Member struct {
	Key    string `json:"key"`
	KeyPos Pos    `json:"keyPos"`
	Value  *Node  `json:"value"`
}

type Node struct {
	Kind    Kind      `json:"kind"`
	Pos     Pos       `json:"pos"`
	End     Pos       `json:"end"`
	TokEnd  int       `json:"-"`
	Raw     string    `json:"raw"`
	Str     string    `json:"str,omitempty"`
	Members []Member  `json:"members,omitempty"`
	Items   []*Node   `json:"items,omitempty"`
}

func (n *Node) Get(key string) *Node {
	if n == nil || n.Kind != Object {
		return nil
	}
	for i := range n.Members {
		if n.Members[i].Key == key {
			return n.Members[i].Value
		}
	}
	return nil
}

// At follows a path of object keys / array indices.
func (n *Node) At(path ...any) *Node {
	cur := n
	for _, p := range path {
		switch v := p.(type) {
		case string:
			cur = cur.Get(v)
		case int:
			if cur == nil || cur.Kind != Array || v < 0 || v >= len(cur.Items) {
				return nil
			}
			cur = cur.Items[v]
		}
		if cur == nil {
			return nil
		}
	}
	return cur
}

func (n *Node) Text() string {
	if n == nil {
		return ""
	}
	return n.Raw
}

type ParseError struct {
	Pos     Pos
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("json: %s at line %d col %d", e.Message, e.Pos.Line, e.Pos.Col)
}

type parser struct {
	src []byte
	i   int
}

func Parse(src []byte) (*Node, error) {
	p := &parser{src: src}
	p.skipWS()
	if p.i >= len(p.src) {
		return nil, &ParseError{Pos: p.pos(), Message: "empty document"}
	}
	n, err := p.value()
	if err != nil {
		return nil, err
	}
	p.skipWS()
	if p.i != len(p.src) {
		return nil, &ParseError{Pos: p.pos(), Message: "trailing characters after value"}
	}
	return n, nil
}

func (p *parser) pos() Pos {
	line, col := 1, 1
	for j := 0; j < p.i && j < len(p.src); j++ {
		if p.src[j] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return Pos{Offset: p.i, Line: line, Col: col}
}

func (p *parser) skipWS() {
	for p.i < len(p.src) {
		switch p.src[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) value() (*Node, error) {
	p.skipWS()
	if p.i >= len(p.src) {
		return nil, &ParseError{Pos: p.pos(), Message: "unexpected end of input"}
	}
	start := p.pos()
	off := p.i
	switch p.src[p.i] {
	case '{':
		return p.object(start, off)
	case '[':
		return p.array(start, off)
	case '"':
		s, err := p.string()
		if err != nil {
			return nil, err
		}
		return &Node{Kind: String, Pos: start, End: p.pos(), TokEnd: p.i, Raw: string(p.src[off:p.i]), Str: s}, nil
	case 't', 'f':
		return p.boolean(start, off)
	case 'n':
		return p.null(start, off)
	default:
		return p.number(start, off)
	}
}

func (p *parser) object(start Pos, off int) (*Node, error) {
	p.i++
	n := &Node{Kind: Object, Pos: start}
	p.skipWS()
	if p.i < len(p.src) && p.src[p.i] == '}' {
		p.i++
		n.End = p.pos()
		n.TokEnd = p.i
		n.Raw = string(p.src[off:p.i])
		return n, nil
	}
	for {
		p.skipWS()
		if p.i >= len(p.src) || p.src[p.i] != '"' {
			return nil, &ParseError{Pos: p.pos(), Message: "expected string key"}
		}
		kp := p.pos()
		key, err := p.string()
		if err != nil {
			return nil, err
		}
		p.skipWS()
		if p.i >= len(p.src) || p.src[p.i] != ':' {
			return nil, &ParseError{Pos: p.pos(), Message: "expected ':' after key"}
		}
		p.i++
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.Members = append(n.Members, Member{Key: key, KeyPos: kp, Value: v})
		p.skipWS()
		if p.i >= len(p.src) {
			return nil, &ParseError{Pos: p.pos(), Message: "unterminated object"}
		}
		switch p.src[p.i] {
		case ',':
			p.i++
			continue
		case '}':
			p.i++
			n.End = p.pos()
			n.TokEnd = p.i
			n.Raw = string(p.src[off:p.i])
			return n, nil
		default:
			return nil, &ParseError{Pos: p.pos(), Message: "expected ',' or '}'"}
		}
	}
}

func (p *parser) array(start Pos, off int) (*Node, error) {
	p.i++
	n := &Node{Kind: Array, Pos: start}
	p.skipWS()
	if p.i < len(p.src) && p.src[p.i] == ']' {
		p.i++
		n.End = p.pos()
		n.TokEnd = p.i
		n.Raw = string(p.src[off:p.i])
		return n, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, v)
		p.skipWS()
		if p.i >= len(p.src) {
			return nil, &ParseError{Pos: p.pos(), Message: "unterminated array"}
		}
		switch p.src[p.i] {
		case ',':
			p.i++
			p.skipWS()
			if p.i < len(p.src) && p.src[p.i] == ']' {
				return nil, &ParseError{Pos: p.pos(), Message: "trailing comma in array"}
			}
			continue
		case ']':
			p.i++
			n.End = p.pos()
			n.TokEnd = p.i
			n.Raw = string(p.src[off:p.i])
			return n, nil
		default:
			return nil, &ParseError{Pos: p.pos(), Message: "expected ',' or ']'"}
		}
	}
}

func (p *parser) string() (string, error) {
	start := p.i
	p.i++
	var b []byte
	for p.i < len(p.src) {
		c := p.src[p.i]
		if c == '"' {
			p.i++
			if b == nil {
				return string(p.src[start+1 : p.i-1]), nil
			}
			return string(b), nil
		}
		if c == '\\' {
			p.i++
			if p.i >= len(p.src) {
				break
			}
			e := p.src[p.i]
			switch e {
			case '"', '\\', '/':
				b = append(b, e)
			case 'b':
				b = append(b, '\b')
			case 'f':
				b = append(b, '\f')
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			case 'u':
				if p.i+4 >= len(p.src) {
					return "", &ParseError{Pos: p.pos(), Message: "bad unicode escape"}
				}
				var r rune
				for k := 0; k < 4; k++ {
					p.i++
					h := p.src[p.i]
					r <<= 4
					switch {
					case h >= '0' && h <= '9':
						r += rune(h - '0')
					case h >= 'a' && h <= 'f':
						r += rune(h-'a') + 10
					case h >= 'A' && h <= 'F':
						r += rune(h-'A') + 10
					default:
						return "", &ParseError{Pos: p.pos(), Message: "bad hex digit"}
					}
				}
				var buf [4]byte
				m := utf8Encode(buf[:], r)
				b = append(b, buf[:m]...)
			default:
				return "", &ParseError{Pos: p.pos(), Message: "bad escape"}
			}
			p.i++
			continue
		}
		if c < 0x20 {
			return "", &ParseError{Pos: p.pos(), Message: "unescaped control character"}
		}
		if b != nil {
			b = append(b, c)
		}
		p.i++
	}
	return "", &ParseError{Pos: p.pos(), Message: "unterminated string"}
}

func utf8Encode(b []byte, r rune) int {
	switch {
	case r < 0x80:
		b[0] = byte(r)
		return 1
	case r < 0x800:
		b[0] = 0xC0 | byte(r>>6)
		b[1] = 0x80 | byte(r&0x3F)
		return 2
	default:
		b[0] = 0xE0 | byte(r>>12)
		b[1] = 0x80 | byte((r>>6)&0x3F)
		b[2] = 0x80 | byte(r&0x3F)
		return 3
	}
}

func (p *parser) boolean(start Pos, off int) (*Node, error) {
	if p.match("true") {
		return &Node{Kind: Bool, Pos: start, End: p.pos(), TokEnd: p.i, Raw: "true", Str: "true"}, nil
	}
	if p.match("false") {
		return &Node{Kind: Bool, Pos: start, End: p.pos(), TokEnd: p.i, Raw: "false", Str: "false"}, nil
	}
	return nil, &ParseError{Pos: p.pos(), Message: "invalid literal"}
}

func (p *parser) null(start Pos, off int) (*Node, error) {
	if p.match("null") {
		return &Node{Kind: Null, Pos: start, End: p.pos(), TokEnd: p.i, Raw: "null"}, nil
	}
	return nil, &ParseError{Pos: p.pos(), Message: "invalid literal"}
}

func (p *parser) match(lit string) bool {
	if p.i+len(lit) <= len(p.src) && string(p.src[p.i:p.i+len(lit)]) == lit {
		p.i += len(lit)
		return true
	}
	return false
}

func (p *parser) number(start Pos, off int) (*Node, error) {
	j := p.i
	if p.src[j] == '-' {
		j++
	}
	digit := false
	for j < len(p.src) && p.src[j] >= '0' && p.src[j] <= '9' {
		j++
		digit = true
	}
	if j < len(p.src) && p.src[j] == '.' {
		j++
		for j < len(p.src) && p.src[j] >= '0' && p.src[j] <= '9' {
			j++
			digit = true
		}
	}
	if j < len(p.src) && (p.src[j] == 'e' || p.src[j] == 'E') {
		j++
		if j < len(p.src) && (p.src[j] == '+' || p.src[j] == '-') {
			j++
		}
		for j < len(p.src) && p.src[j] >= '0' && p.src[j] <= '9' {
			j++
		}
	}
	if !digit {
		return nil, &ParseError{Pos: p.pos(), Message: "invalid number"}
	}
	p.i = j
	return &Node{Kind: Number, Pos: start, End: p.pos(), TokEnd: p.i, Raw: string(p.src[off:j]), Str: string(p.src[off:j])}, nil
}

// ReplaceToken substitutes the source text between two byte offsets.
func ReplaceToken(src []byte, startOff, endOff int, replacement string) []byte {
	out := make([]byte, 0, len(src)+len(replacement))
	out = append(out, src[:startOff]...)
	out = append(out, replacement...)
	out = append(out, src[endOff:]...)
	return out
}
