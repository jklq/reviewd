package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// block builds a single 512-byte tar header with a valid checksum.
func block(name string, typeflag byte, size int64) []byte {
	h := make([]byte, 512)
	copy(h[0:100], name)
	copy(h[100:108], "0000644\x00")
	copy(h[108:116], "0000000\x00")
	copy(h[116:124], "0000000\x00")
	copy(h[124:136], fmt.Sprintf("%011o\x00", size))
	copy(h[136:148], "00000000000\x00")
	copy(h[257:263], "ustar\x00")
	copy(h[263:265], "00")
	h[156] = typeflag
	for i := 148; i < 156; i++ {
		h[i] = ' '
	}
	sum := 0
	for _, b := range h {
		sum += int(b)
	}
	copy(h[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return h
}

// Git archive prefixes the stream with a PAX global header, which Go's tar
// reader surfaces as an entry. Extraction must ignore it rather than treat
// "pax_global_header" as the archive root.
func TestExtractSkipsPAXGlobalHeader(t *testing.T) {
	gzipped := archive(t, []*tar.Header{{Name: "repo/file", Typeflag: tar.TypeReg, Size: 1, Mode: 0644}})
	gz, err := gzip.NewReader(bytes.NewReader(gzipped))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	record := []byte("20 comment=deadbeef\n")
	var raw bytes.Buffer
	raw.Write(block("pax_global_header", tar.TypeXGlobalHeader, int64(len(record))))
	raw.Write(record)
	if pad := (512 - len(record)%512) % 512; pad > 0 {
		raw.Write(make([]byte, pad))
	}
	raw.Write(entries)

	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err = zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	if err = Extract(bytes.NewReader(out.Bytes()), d); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(d, "file")); err != nil {
		t.Fatal(err)
	}
}
