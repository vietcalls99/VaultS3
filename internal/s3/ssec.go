package s3

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
)

// ssecReader is an in-memory ReadSeekCloser over decrypted SSE-C plaintext, so
// the GET handler's range/part logic operates on plaintext.
type ssecReader struct{ *bytes.Reader }

func (ssecReader) Close() error { return nil }

// SSE-C: server-side encryption with customer-provided keys. The client supplies
// a 32-byte key per request; the server encrypts/decrypts with it and stores only
// the key's MD5 (for verification) — never the key itself. This is the
// operator-blind option: lose the key and the data is unrecoverable.
//
// Headers (mirroring S3):
//
//	x-amz-server-side-encryption-customer-algorithm: AES256
//	x-amz-server-side-encryption-customer-key:        base64(32-byte key)
//	x-amz-server-side-encryption-customer-key-MD5:    base64(md5(key))

const (
	hdrSSECAlgo   = "X-Amz-Server-Side-Encryption-Customer-Algorithm"
	hdrSSECKey    = "X-Amz-Server-Side-Encryption-Customer-Key"
	hdrSSECKeyMD5 = "X-Amz-Server-Side-Encryption-Customer-Key-Md5"
)

type sseCustomerKey struct {
	key    []byte // 32 bytes
	keyMD5 string // base64(md5(key))
}

// parseSSECHeaders extracts and validates the SSE-C headers. Returns (nil, nil)
// when no SSE-C headers are present.
func parseSSECHeaders(r *http.Request) (*sseCustomerKey, error) {
	algo := r.Header.Get(hdrSSECAlgo)
	if algo == "" {
		return nil, nil
	}
	if algo != "AES256" {
		return nil, fmt.Errorf("unsupported SSE-C algorithm %q", algo)
	}
	key, err := base64.StdEncoding.DecodeString(r.Header.Get(hdrSSECKey))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("SSE-C key must be base64 of 32 bytes")
	}
	sum := md5.Sum(key)
	want := base64.StdEncoding.EncodeToString(sum[:])
	if got := r.Header.Get(hdrSSECKeyMD5); got != "" && got != want {
		return nil, fmt.Errorf("SSE-C key MD5 mismatch")
	}
	return &sseCustomerKey{key: key, keyMD5: want}, nil
}

func ssecGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ssecSeal encrypts plaintext with the customer key (nonce prepended).
func ssecSeal(k *sseCustomerKey, plaintext []byte) ([]byte, error) {
	gcm, err := ssecGCM(k.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, nil)...), nil
}

// ssecOpen decrypts data produced by ssecSeal with the customer key.
func ssecOpen(k *sseCustomerKey, data []byte) ([]byte, error) {
	gcm, err := ssecGCM(k.key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("SSE-C ciphertext too short")
	}
	return gcm.Open(nil, data[:ns], data[ns:], nil)
}
