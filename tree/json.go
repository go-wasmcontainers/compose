package tree

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"unicode/utf8"
)

// ParseJSON reads a JSON document (RFC 8259) into a tree, keeping key
// order and positions, refusing duplicate keys and trailing content.
func ParseJSON(file string, data []byte, lim Limits) (*Node, error) {
	b := &budget{lim: lim.WithDefaults(), file: file}
	if err := b.size(data); err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, Errorf(Pos{File: file}, "file is not valid UTF-8")
	}
	j := &jconv{b: b, data: data, dec: json.NewDecoder(bytes.NewReader(data))}
	j.dec.UseNumber()
	for i, c := range data {
		if c == '\n' {
			j.lines = append(j.lines, i+1)
		}
	}
	tok, p, err := j.next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Errorf(Pos{File: file}, "empty file")
		}
		return nil, err
	}
	n, err := j.value(tok, p, 0)
	if err != nil {
		return nil, err
	}
	if _, p, err := j.next(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, Errorf(p, "unexpected content after the JSON document")
	}
	return n, nil
}

type jconv struct {
	b     *budget
	data  []byte
	dec   *json.Decoder
	lines []int // offsets where lines 2, 3, ... start
}

// at converts a byte offset to a position, skipping the whitespace and
// separators the decoder has not consumed yet.
func (j *jconv) at(off int) Pos {
	for off < len(j.data) {
		switch j.data[off] {
		case ' ', '\t', '\r', '\n', ',', ':':
			off++
			continue
		}
		break
	}
	line := sort.SearchInts(j.lines, off+1) + 1
	start := 0
	if line > 1 {
		start = j.lines[line-2]
	}
	return Pos{File: j.b.file, Line: line, Col: utf8.RuneCount(j.data[start:off]) + 1}
}

func (j *jconv) next() (json.Token, Pos, error) {
	p := j.at(int(j.dec.InputOffset()))
	tok, err := j.dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, p, io.EOF
		}
		var se *json.SyntaxError
		if errors.As(err, &se) {
			// Offset counts the offending byte: point at it.
			at := j.at(max(int(se.Offset)-1, 0))
			return nil, at, &Error{Pos: at, Msg: se.Error()}
		}
		return nil, p, &Error{Pos: p, Msg: err.Error()}
	}
	return tok, p, nil
}

func (j *jconv) value(tok json.Token, p Pos, depth int) (*Node, error) {
	if err := j.b.node(p, depth); err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case nil:
		return &Node{Kind: Null, Pos: p}, nil
	case bool:
		return &Node{Kind: Bool, Pos: p, Text: strconv.FormatBool(t)}, nil
	case string:
		if err := j.b.str(p, t); err != nil {
			return nil, err
		}
		return &Node{Kind: String, Pos: p, Text: t}, nil
	case json.Number:
		if v, err := t.Int64(); err == nil {
			return &Node{Kind: Int, Pos: p, Text: strconv.FormatInt(v, 10)}, nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, Errorf(p, "number %s out of range", t)
		}
		return Number(f, p), nil
	case json.Delim:
		switch t {
		case '[':
			out := &Node{Kind: Seq, Pos: p, Items: []*Node{}}
			for {
				tok, ip, err := j.next()
				if err != nil {
					return nil, j.eof(err, p)
				}
				if d, ok := tok.(json.Delim); ok && d == ']' {
					return out, nil
				}
				v, err := j.value(tok, ip, depth+1)
				if err != nil {
					return nil, err
				}
				out.Items = append(out.Items, v)
			}
		case '{':
			out := &Node{Kind: Map, Pos: p}
			for {
				tok, kp, err := j.next()
				if err != nil {
					return nil, j.eof(err, p)
				}
				if d, ok := tok.(json.Delim); ok && d == '}' {
					return out, nil
				}
				key := tok.(string) // the decoder only yields string keys
				if err := j.b.str(kp, key); err != nil {
					return nil, err
				}
				if i := out.Index(key); i >= 0 {
					return nil, Errorf(kp, "duplicate key %q (first at %s)", key, out.Entries[i].KeyPos)
				}
				tok, vp, err := j.next()
				if err != nil {
					return nil, j.eof(err, p)
				}
				v, err := j.value(tok, vp, depth+1)
				if err != nil {
					return nil, err
				}
				out.Entries = append(out.Entries, Entry{Key: key, KeyPos: kp, Value: v})
			}
		}
	}
	return nil, Errorf(p, "unexpected JSON token %v", tok)
}

func (j *jconv) eof(err error, open Pos) error {
	if errors.Is(err, io.EOF) {
		return Errorf(open, "unterminated JSON value")
	}
	return err
}
