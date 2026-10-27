package byok

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// CredentialBox encrypts managed provider credentials at rest. The root key
// is supplied by protected runtime configuration and is never derived from a
// session, project, or provider credential.
type CredentialBox struct{ aead cipher.AEAD }

func NewCredentialBox(key []byte) (*CredentialBox, error) {
	if len(key) != 32 {
		return nil, errors.New("credential encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("invalid credential encryption key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("credential encryption is unavailable")
	}
	return &CredentialBox{aead: aead}, nil
}

func (b *CredentialBox) Encrypt(plaintext, associatedData []byte) (string, error) {
	if b == nil || b.aead == nil || len(plaintext) == 0 {
		return "", errors.New("credential is unavailable")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", errors.New("credential encryption failed")
	}
	ciphertext := b.aead.Seal(nonce, nonce, plaintext, associatedData)
	return base64.RawStdEncoding.EncodeToString(ciphertext), nil
}

func (b *CredentialBox) Decrypt(encoded string, associatedData []byte) ([]byte, error) {
	if b == nil || b.aead == nil || encoded == "" {
		return nil, errors.New("credential is unavailable")
	}
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(data) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, errors.New("credential ciphertext is invalid")
	}
	nonce, ciphertext := data[:b.aead.NonceSize()], data[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return nil, errors.New("credential ciphertext is invalid")
	}
	return plaintext, nil
}
