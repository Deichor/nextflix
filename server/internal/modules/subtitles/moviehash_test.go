package subtitles

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// naiveHash is an independent byte-by-byte implementation of the OpenSubtitles hash.
func naiveHash(data []byte) string {
	var sum uint64 = uint64(len(data))
	add := func(chunk []byte) {
		for i := 0; i < len(chunk); i += 8 {
			var w uint64
			for j := 7; j >= 0; j-- {
				w = w<<8 | uint64(chunk[i+j])
			}
			sum += w
		}
	}
	add(data[:hashChunk])
	add(data[len(data)-hashChunk:])
	return fmt.Sprintf("%016x", sum)
}

func TestMovieHashKnownVectors(t *testing.T) {
	zeros := make([]byte, 2*hashChunk)
	if got, err := movieHashReader(bytes.NewReader(zeros), int64(len(zeros))); err != nil || got != "0000000000020000" {
		t.Fatalf("zeros: got %s, %v", got, err)
	}
	ones := bytes.Repeat([]byte{1}, 2*hashChunk)
	// 16384 words of 0x0101010101010101 plus the size 0x20000.
	if got, _ := movieHashReader(bytes.NewReader(ones), int64(len(ones))); got != "4040404040424000" {
		t.Fatalf("ones: got %s", got)
	}
	// Overflow wraps at 64 bits.
	ff := bytes.Repeat([]byte{0xff}, 2*hashChunk)
	if got, _ := movieHashReader(bytes.NewReader(ff), int64(len(ff))); got != naiveHash(ff) || got != "000000000001c000" {
		t.Fatalf("0xff: got %s want %s", got, naiveHash(ff))
	}
}

func TestMovieHashRandomFile(t *testing.T) {
	data := make([]byte, 1_234_567)
	rand.New(rand.NewSource(42)).Read(data)
	want := naiveHash(data)
	got, err := movieHashReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || got != want {
		t.Fatalf("got %s, %v; want %s", got, err, want)
	}
	// Sanity check against the word-sum definition.
	var sum uint64 = uint64(len(data))
	for i := 0; i < hashChunk; i += 8 {
		sum += binary.LittleEndian.Uint64(data[i:]) + binary.LittleEndian.Uint64(data[len(data)-hashChunk+i:])
	}
	if fmt.Sprintf("%016x", sum) != want {
		t.Fatal("definition mismatch")
	}
	if _, err := movieHashReader(bytes.NewReader(data[:1000]), 1000); err == nil {
		t.Fatal("tiny file must fail")
	}
}

func TestTorrServerFileHash(t *testing.T) {
	data := make([]byte, 3*hashChunk+123)
	rand.New(rand.NewSource(7)).Read(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream" || r.URL.Query().Get("link") != "abc" || r.URL.Query().Get("index") != "2" {
			http.NotFound(w, r)
			return
		}
		if _, ok := r.URL.Query()["play"]; !ok {
			http.Error(w, "no play", 400)
			return
		}
		http.ServeContent(w, r, "v.mkv", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()

	got, size, err := torrServerFileHash(context.Background(), srv.Client(), srv.URL, "abc", 2)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(data)) || got != naiveHash(data) {
		t.Fatalf("got %s/%d want %s/%d", got, size, naiveHash(data), len(data))
	}
}
