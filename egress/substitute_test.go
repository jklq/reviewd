package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type dripReader struct {
	b []byte
	n int
}

func (r *dripReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	k := r.n
	if k > len(r.b) {
		k = len(r.b)
	}
	if k > len(p) {
		k = len(p)
	}
	copy(p, r.b[:k])
	r.b = r.b[k:]
	return k, nil
}

func TestSubstituteStringAndBytes(t *testing.T) {
	pairs := []pair{{old: "SENTINEL", new: "REAL"}, {old: "aa", new: "b"}}
	if got := substituteString("x SENTINEL y SENTINEL", pairs[:1]); got != "x REAL y REAL" {
		t.Fatal(got)
	}
	if got := string(substituteBytes([]byte("aaaa"), pairs[1:])); got != "bb" {
		t.Fatal(got)
	}
	if got := substituteString("untouched", pairs); got != "untouched" {
		t.Fatal(got)
	}
	if got := substituteString("SENTINEL", []pair{{old: "", new: "x"}}); got != "SENTINEL" {
		t.Fatal(got)
	}
}

func TestStreamReplacer(t *testing.T) {
	input := "ab SENTINEL cd SENTINELe SENTINEL"
	want := "ab REAL cd REALe REAL"
	for _, drip := range []int{1, 2, 3, 7, 64} {
		r := substituteStream(&dripReader{b: []byte(input), n: drip}, []pair{{old: "SENTINEL", new: "REAL"}})
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != want {
			t.Fatalf("drip %d: %q", drip, out)
		}
	}
	chained := substituteStream(strings.NewReader("S1 and S2"), []pair{{old: "S1", new: "R1"}, {old: "S2", new: "R2"}})
	out, err := io.ReadAll(chained)
	if err != nil || string(out) != "R1 and R2" {
		t.Fatalf("chained: %q %v", out, err)
	}
	empty := substituteStream(strings.NewReader("abc"), nil)
	out, err = io.ReadAll(empty)
	if err != nil || string(out) != "abc" {
		t.Fatalf("empty pairs: %q %v", out, err)
	}
	overlap := substituteStream(strings.NewReader("aaaa"), []pair{{old: "aa", new: "b"}})
	out, err = io.ReadAll(overlap)
	if err != nil || string(out) != "bb" {
		t.Fatalf("overlap: %q %v", out, err)
	}
	tail := substituteStream(&dripReader{b: []byte("xxSENTINE"), n: 2}, []pair{{old: "SENTINEL", new: "REAL"}})
	out, err = io.ReadAll(tail)
	if err != nil || string(out) != "xxSENTINE" {
		t.Fatalf("partial tail: %q %v", out, err)
	}
	if !bytes.Equal(substituteBytes(nil, []pair{{old: "a", new: "b"}}), nil) {
		t.Fatal("nil body changed")
	}
}
