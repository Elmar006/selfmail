package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

type Vault struct{ aead cipher.AEAD }

func NewVault(encoded string) (*Vault, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("MASTER_KEY must be a base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	return &Vault{aead}, err
}
func (v *Vault) Seal(data []byte, context string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, data, []byte(context)), nil
}
func (v *Vault) Open(data []byte, context string) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("invalid encrypted secret")
	}
	return v.aead.Open(nil, data[:n], data[n:], []byte(context))
}
func Digest(token string) string { s := sha256.Sum256([]byte(token)); return hex.EncodeToString(s[:]) }
func Sign(secret []byte, timestamp string, payload []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(timestamp + "."))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
