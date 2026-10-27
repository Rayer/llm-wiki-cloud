package byok

import (
	"bytes"
	"strings"
	"testing"
)

func TestCredentialBoxEncryptsWithContextAndNeverEchoesPlaintext(t *testing.T) {
	box, err := NewCredentialBox(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	const key = "provider-secret-marker"
	ciphertext, err := box.Encrypt([]byte(key), []byte("user:profile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, key) || ciphertext == key {
		t.Fatalf("ciphertext exposed the plaintext: %q", ciphertext)
	}
	plaintext, err := box.Decrypt(ciphertext, []byte("user:profile"))
	if err != nil || string(plaintext) != key {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
	clear(plaintext)
	if _, err := box.Decrypt(ciphertext, []byte("other:profile")); err == nil {
		t.Fatal("ciphertext decrypted under a different owner/profile context")
	}
}
