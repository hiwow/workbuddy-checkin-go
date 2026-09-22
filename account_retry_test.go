package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func makeEnvelope(t *testing.T, key []byte, keyID, plaintext string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	aad := buildAAD(keyID, 1, "field")
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), aad)
	ct := sealed[:len(sealed)-gcm.Overhead()]
	tag := sealed[len(sealed)-gcm.Overhead():]
	env := map[string]any{
		"suite":      1,
		"keyId":      keyID,
		"nonce":      base64.StdEncoding.EncodeToString(nonce),
		"authTag":    base64.StdEncoding.EncodeToString(tag),
		"ciphertext": base64.StdEncoding.EncodeToString(ct),
	}
	raw, _ := json.Marshal(env)
	return base64.StdEncoding.EncodeToString(raw)
}

func encryptedDoc(t *testing.T, env string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"account": map[string]any{"uid": "u1"},
		"auth": map[string]any{
			"accessToken": map[string]any{"$wbEncrypted": 1, "envelope": env},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDecryptFileRetriesWithFreshKey(t *testing.T) {
	keyID := "0123456789abcdef"
	correct := make([]byte, 32)
	wrong := make([]byte, 32)
	for i := range correct {
		correct[i] = byte(i + 7)
		wrong[i] = byte(i + 99)
	}
	doc := encryptedDoc(t, makeEnvelope(t, correct, keyID, "hello-secret"))

	var forced int
	extract := func(force bool) ([]byte, error) {
		if force {
			forced++
			return correct, nil
		}
		return wrong, nil
	}
	raw, err := decryptFile(doc, extract)
	if err != nil {
		t.Fatalf("重试应成功，实际报错: %v", err)
	}
	if forced != 1 {
		t.Fatalf("应强制重新取密钥 1 次，实际 %d 次", forced)
	}
	auth := raw["auth"].(map[string]any)
	if auth["accessToken"] != "hello-secret" {
		t.Fatalf("解密值不符: %#v", auth["accessToken"])
	}
}

func TestDecryptFileFailsWhenRetryAlsoFails(t *testing.T) {
	keyID := "0123456789abcdef"
	correct := make([]byte, 32)
	for i := range correct {
		correct[i] = byte(i + 7)
	}
	doc := encryptedDoc(t, makeEnvelope(t, correct, keyID, "secret"))

	extract := func(force bool) ([]byte, error) { return make([]byte, 32), nil }
	if _, err := decryptFile(doc, extract); err == nil {
		t.Fatal("重取密钥仍失败时应返回错误")
	}
}
