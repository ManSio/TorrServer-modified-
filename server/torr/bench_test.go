package torr

import (
	"bytes"
	"io"
	"testing"
)

func BenchmarkBufferedStreamRead64K(b *testing.B) {
	data := make([]byte, 4<<20)
	r := newBufferedStreamReader(bytes.NewReader(data), 1<<20)
	p := make([]byte, 64<<10)
	b.SetBytes(int64(len(p)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Read(p); err != nil {
			if err == io.EOF {
				if _, err := r.Seek(0, io.SeekStart); err != nil {
					b.Fatal(err)
				}
				continue
			}
			b.Fatal(err)
		}
	}
}

func BenchmarkRawStreamRead64K(b *testing.B) {
	data := make([]byte, 4<<20)
	r := bytes.NewReader(data)
	p := make([]byte, 64<<10)
	b.SetBytes(int64(len(p)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Read(p); err != nil {
			if err == io.EOF {
				if _, err := r.Seek(0, io.SeekStart); err != nil {
					b.Fatal(err)
				}
				continue
			}
			b.Fatal(err)
		}
	}
}
