package bucketcrypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	mk := make([]byte, 32)
	rand.Read(mk)
	kek, err := NewKEK(mk)
	if err != nil {
		t.Fatalf("NewKEK: %v", err)
	}
	return NewManager(kek, NewMemKeyStore())
}

func TestRoundTrip(t *testing.T) {
	m := newManager(t)
	if err := m.EnableBucket("tenant-a"); err != nil {
		t.Fatal(err)
	}
	plain := []byte("tenant A confidential records")
	enc, ok, err := m.Encrypt("tenant-a", plain)
	if err != nil || !ok {
		t.Fatalf("Encrypt ok=%v err=%v", ok, err)
	}
	if bytes.Contains(enc, plain) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := m.Decrypt("tenant-a", enc)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Decrypt got=%q err=%v", got, err)
	}
}

// The whole point of the feature: tenant B's key cannot read tenant A's object.
func TestPerBucketIsolation(t *testing.T) {
	m := newManager(t)
	m.EnableBucket("tenant-a")
	m.EnableBucket("tenant-b")

	encA, _, _ := m.Encrypt("tenant-a", []byte("A secret"))

	if _, err := m.Decrypt("tenant-b", encA); err == nil {
		t.Fatal("tenant B must NOT be able to decrypt tenant A's object")
	}
	// A can still read its own.
	if got, err := m.Decrypt("tenant-a", encA); err != nil || string(got) != "A secret" {
		t.Fatalf("owner decrypt failed: %v", err)
	}
}

func TestOptOutPassthrough(t *testing.T) {
	m := newManager(t) // no EnableBucket -> opted out
	plain := []byte("plaintext bucket")
	enc, ok, err := m.Encrypt("plain-tenant", plain)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("opt-out bucket should not be encrypted")
	}
	if !bytes.Equal(enc, plain) {
		t.Fatal("opt-out Encrypt must pass plaintext through unchanged")
	}
	// Decrypt of non-magic data is a pass-through too.
	if got, _ := m.Decrypt("plain-tenant", plain); !bytes.Equal(got, plain) {
		t.Fatal("opt-out Decrypt must pass through")
	}
}

func TestRotationKeepsOldObjectsReadable(t *testing.T) {
	m := newManager(t)
	m.EnableBucket("acme")
	old, _, _ := m.Encrypt("acme", []byte("written under v1"))

	if err := m.Rotate("acme"); err != nil {
		t.Fatal(err)
	}
	fresh, _, _ := m.Encrypt("acme", []byte("written under v2"))

	// Both decrypt: old carries v1 in its header, fresh carries v2.
	if got, err := m.Decrypt("acme", old); err != nil || string(got) != "written under v1" {
		t.Fatalf("v1 object unreadable after rotation: %v", err)
	}
	if got, err := m.Decrypt("acme", fresh); err != nil || string(got) != "written under v2" {
		t.Fatalf("v2 object unreadable: %v", err)
	}
	if v, _, _ := m.keys.Current("acme"); v != 2 {
		t.Fatalf("current version = %d, want 2", v)
	}
}

func TestCryptoShred(t *testing.T) {
	m := newManager(t)
	m.EnableBucket("departing-tenant")
	enc, _, _ := m.Encrypt("departing-tenant", []byte("must be unrecoverable after offboarding"))

	if got, err := m.Decrypt("departing-tenant", enc); err != nil || got == nil {
		t.Fatal("sanity: should decrypt before shred")
	}
	if err := m.ShredBucket("departing-tenant"); err != nil {
		t.Fatal(err)
	}
	// Key is gone → data is unrecoverable.
	if _, err := m.Decrypt("departing-tenant", enc); err == nil {
		t.Fatal("after crypto-shred the data MUST be unrecoverable")
	}
}

func TestWrongKEKCannotUnwrap(t *testing.T) {
	// Encrypt under one KEK, then try to read with a Manager built from a
	// different KEK over the same wrapped-key store.
	store := NewMemKeyStore()
	mk1 := make([]byte, 32)
	rand.Read(mk1)
	kek1, _ := NewKEK(mk1)
	m1 := NewManager(kek1, store)
	m1.EnableBucket("x")
	enc, _, _ := m1.Encrypt("x", []byte("secret"))

	mk2 := make([]byte, 32)
	rand.Read(mk2)
	kek2, _ := NewKEK(mk2)
	m2 := NewManager(kek2, store) // same store (wrapped DEKs), wrong KEK
	if _, err := m2.Decrypt("x", enc); err == nil {
		t.Fatal("a wrong master KEK must not unwrap the bucket DEK")
	}
}

func TestTamperedCiphertextRejected(t *testing.T) {
	m := newManager(t)
	m.EnableBucket("b")
	enc, _, _ := m.Encrypt("b", []byte("authentic"))
	enc[len(enc)-1] ^= 0xff // flip a tag byte
	if _, err := m.Decrypt("b", enc); err == nil {
		t.Fatal("GCM must reject tampered ciphertext")
	}
}
