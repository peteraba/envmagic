package internal

import (
	"bytes"
	"strings"
	"testing"
)

func TestCrypto(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	plain := []byte("a secret value that must not appear in stored bytes")
	ad := AD("dev", "TOKEN")
	enc, err := Encrypt(key, plain, ad)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(key, enc, ad)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: got %q, err=%v", got, err)
	}
	if len(enc) != 12+len(plain)+16 {
		t.Fatalf("stored length: got %d, want nonce + plaintext + tag", len(enc))
	}
	if bytes.Contains(enc, plain) {
		t.Fatal("stored bytes contain plaintext")
	}
	second, err := Encrypt(key, plain, ad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(enc, second) {
		t.Fatal("encrypting twice produced identical ciphertext")
	}

	for _, tc := range []struct {
		name string
		key  []byte
		data []byte
		ad   []byte
	}{
		{"different namespace", key, enc, AD("prd", "TOKEN")},
		{"different name", key, enc, AD("dev", "OTHER")},
		{"nil AD", key, enc, nil},
		{"short input", key, enc[:11], ad},
		{"missing tag", key, enc[:12], ad},
		{"wrong key", bytes.Repeat([]byte{2}, 32), enc, ad},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Decrypt(tc.key, tc.data, tc.ad); err == nil || len(got) != 0 {
				t.Fatalf("got %q, err=%v; want no plaintext and an error", got, err)
			}
		})
	}
}

func TestADUnambiguous(t *testing.T) {
	for _, pair := range [][4]string{
		{"a", "BC", "aB", "C"},
		{"", "ABC", "A", "BC"},
		{"a\x00", "B", "a", "\x00B"},
		{strings.Repeat("a", 256), "B", "", strings.Repeat("a", 256) + "B"},
	} {
		if bytes.Equal(AD(pair[0], pair[1]), AD(pair[2], pair[3])) {
			t.Errorf("AD(%q, %q) equals AD(%q, %q)", pair[0], pair[1], pair[2], pair[3])
		}
	}
}
