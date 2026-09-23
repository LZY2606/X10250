// Package parse imports capture documents while preserving the original
// representation of every quantity. Normalisation happens only in the
// derived layer; values that cannot be converted stay in place and carry a
// positioned diagnostic.
package parse

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// node is one JSON value with its byte span and JSON Pointer.
type node struct {
	kind    json.Token
	pointer string
	start   int
	end     int
	obj     map[string]*node
	arr     []*node
	leaf    json.Token
}

func (n *node) field(name string) *node {
	if n == nil || n.obj == nil {
		return nil
	}
	return n.obj[name]
}

func skipSpace(doc []byte, i int) int {
	for i < len(doc) {
		switch doc[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// parseTree decodes doc into a positioned node tree.
func parseTree(doc []byte) (*node, error) {
	d := json.NewDecoder(bytes.NewReader(doc))
	d.UseNumber()
	var parse func(pointer string, prevEnd int) (*node, error)
	parse = func(pointer string, prevEnd int) (*node, error) {
		start := skipSpace(doc, prevEnd)
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		end := int(d.InputOffset())
		n := &node{pointer: pointer, start: start, end: end, leaf: tok}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				n.obj = map[string]*node{}
				for d.More() {
					kt, err := d.Token()
					if err != nil {
						return nil, err
					}
					key := kt.(string)
					keyEnd := int(d.InputOffset())
					child, err := parse(pointer+"/"+escapePointer(key), keyEnd)
					if err != nil {
						return nil, err
					}
					n.obj[key] = child
					end = int(d.InputOffset())
					n.end = end
				}
				closeTok, err := d.Token()
				if err != nil {
					return nil, err
				}
				_ = closeTok
				n.end = int(d.InputOffset())
			case '[':
				for d.More() {
					child, err := parse(fmt.Sprintf("%s/%d", pointer, len(n.arr)), end)
					if err != nil {
						return nil, err
					}
					n.arr = append(n.arr, child)
					end = int(d.InputOffset())
					n.end = end
				}
				if _, err := d.Token(); err != nil {
					return nil, err
				}
				n.end = int(d.InputOffset())
			}
		}
		return n, nil
	}
	root, err := parse("", 0)
	if err != nil {
		return nil, err
	}
	// trailing tokens -> corrupt document
	if d.More() {
		return nil, fmt.Errorf("%w: trailing data after root value", ErrCorrupt)
	}
	return root, nil
}

func escapePointer(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '~':
			out = append(out, '~', '0')
		case '/':
			out = append(out, '~', '1')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}

// lineCol converts a byte offset into 1-based line/column for diagnostics.
func lineCol(doc []byte, offset int) (int, int) {
	line, col := 1, 1
	for i := 0; i < offset && i < len(doc); i++ {
		if doc[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}
