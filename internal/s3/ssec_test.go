package s3

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"
)

func ssecHeaders(key []byte) http.Header {
	h := http.Header{}
	h.Set(hdrSSECAlgo, "AES256")
	h.Set(hdrSSECKey, base64.StdEncoding.EncodeToString(key))
	sum := md5.Sum(key)
	h.Set(hdrSSECKeyMD5, base64.StdEncoding.EncodeToString(sum[:]))
	return h
}

func TestSSEC_RoundTripAndKeyMismatch(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	r := &http.Request{Header: ssecHeaders(key)}
	k, err := parseSSECHeaders(r)
	if err != nil || k == nil {
		t.Fatalf("parse: %v", err)
	}

	plain := []byte("operator must never see this")
	sealed, err := ssecSeal(k, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := ssecOpen(k, sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open: got=%q err=%v", got, err)
	}

	// A different key cannot decrypt.
	other := make([]byte, 32)
	rand.Read(other)
	r2 := &http.Request{Header: ssecHeaders(other)}
	k2, _ := parseSSECHeaders(r2)
	if _, err := ssecOpen(k2, sealed); err == nil {
		t.Fatal("a different customer key must not decrypt")
	}
}

func TestSSEC_HeaderValidation(t *testing.T) {
	// No headers → not SSE-C.
	if k, err := parseSSECHeaders(&http.Request{Header: http.Header{}}); k != nil || err != nil {
		t.Fatal("absent headers should yield (nil, nil)")
	}
	// Wrong key length.
	bad := http.Header{}
	bad.Set(hdrSSECAlgo, "AES256")
	bad.Set(hdrSSECKey, base64.StdEncoding.EncodeToString([]byte("short")))
	if _, err := parseSSECHeaders(&http.Request{Header: bad}); err == nil {
		t.Fatal("short key must be rejected")
	}
	// MD5 mismatch.
	key := make([]byte, 32)
	rand.Read(key)
	mm := ssecHeaders(key)
	mm.Set(hdrSSECKeyMD5, "AAAAAAAAAAAAAAAAAAAAAA==")
	if _, err := parseSSECHeaders(&http.Request{Header: mm}); err == nil {
		t.Fatal("key MD5 mismatch must be rejected")
	}
}
