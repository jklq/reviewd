package main

import (
	"bytes"
	"io"
	"strings"
)

type pair struct {
	old, new string
}

func substituteString(s string, pairs []pair) string {
	for _, p := range pairs {
		if p.old == "" {
			continue
		}
		s = strings.ReplaceAll(s, p.old, p.new)
	}
	return s
}

func substituteBytes(b []byte, pairs []pair) []byte {
	for _, p := range pairs {
		if p.old == "" {
			continue
		}
		b = bytes.ReplaceAll(b, []byte(p.old), []byte(p.new))
	}
	return b
}

func substituteStream(r io.Reader, pairs []pair) io.Reader {
	for _, p := range pairs {
		if p.old == "" {
			continue
		}
		r = &streamReplacer{src: r, old: []byte(p.old), new: []byte(p.new)}
	}
	return r
}

type streamReplacer struct {
	src     io.Reader
	old     []byte
	new     []byte
	pending []byte
	eof     bool
}

func (s *streamReplacer) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for !s.eof && len(s.pending) < len(s.old)+len(p) {
		chunk := make([]byte, 32<<10)
		n, err := s.src.Read(chunk)
		if n > 0 {
			s.pending = append(s.pending, chunk[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				s.eof = true
				break
			}
			return 0, err
		}
	}
	var out []byte
	if i := bytes.Index(s.pending, s.old); i >= 0 {
		out = append(append(out, s.pending[:i]...), s.new...)
		s.pending = s.pending[i+len(s.old):]
	} else if s.eof {
		out = s.pending
		s.pending = nil
	} else {
		keep := len(s.old) - 1
		if len(s.pending) <= keep {
			return 0, nil
		}
		out = s.pending[:len(s.pending)-keep]
		s.pending = append([]byte(nil), s.pending[len(s.pending)-keep:]...)
	}
	n := copy(p, out)
	if n < len(out) {
		s.pending = append(append([]byte(nil), out[n:]...), s.pending...)
	}
	if n == 0 && s.eof && len(s.pending) == 0 {
		return 0, io.EOF
	}
	if n == 0 {
		return 0, nil
	}
	return n, nil
}
