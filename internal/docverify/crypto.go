package docverify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
)

// EncryptedPayload représente un document chiffré en hybride envoyé par le client.
type EncryptedPayload struct {
	EncryptedKey []byte `json:"encrypted_key"` // clé AES chiffrée par RSA-OAEP
	IV           []byte `json:"iv"`            // nonce 12 octets pour AES-GCM
	Ciphertext   []byte `json:"ciphertext"`    // document chiffré en AES-GCM
}

// --- Helpers de clés ---

// ParseRSAPrivateKey lit une clé privée RSA au format PEM (PKCS8 ou PKCS1).
func ParseRSAPrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("crypto: aucun bloc PEM trouvé")
	}

	// PKCS8 d'abord, fallback PKCS1.
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("crypto: la clé PKCS8 n'est pas RSA")
		}
		return rsaKey, nil
	}

	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// ParseRSAPublicKey lit une clé publique RSA au format PEM (PKIX).
func ParseRSAPublicKey(pemData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("crypto: aucun bloc PEM trouvé")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("crypto: la clé n'est pas RSA")
	}
	return rsaPub, nil
}

// --- Chiffrement (côté client, ou pour les tests) ---

// Encrypt chiffre `plaintext` en hybride RSA+AES-GCM avec la clé publique du destinataire.
// Retourne un EncryptedPayload prêt à envoyer.
func Encrypt(pub *rsa.PublicKey, plaintext []byte) (*EncryptedPayload, error) {
	// 1. Génère une clé AES-256 éphémère.
	aesKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
		return nil, fmt.Errorf("crypto: génération clé AES: %w", err)
	}

	// 2. Chiffre la clé AES avec RSA-OAEP (SHA-256).
	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, aesKey, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: chiffrement RSA: %w", err)
	}

	// 3. Chiffre le document en AES-256-GCM.
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher AES: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: GCM: %w", err)
	}

	iv := make([]byte, gcm.NonceSize()) // 12 octets
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("crypto: génération IV: %w", err)
	}

	ciphertext := gcm.Seal(nil, iv, plaintext, nil)

	return &EncryptedPayload{
		EncryptedKey: encryptedKey,
		IV:           iv,
		Ciphertext:   ciphertext,
	}, nil
}

// --- Déchiffrement (côté serveur) ---

// Decrypt déchiffre un payload hybride RSA+AES-GCM. Le serveur récupère la clé AES
// éphémère via sa clé privée RSA, puis déchiffre le document.
func Decrypt(priv *rsa.PrivateKey, payload *EncryptedPayload) ([]byte, error) {
	// 1. Récupère la clé AES via RSA-OAEP.
	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, payload.EncryptedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: déchiffrement RSA: %w", err)
	}

	// 2. Déchiffre le document en AES-256-GCM.
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher AES: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, payload.IV, payload.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: déchiffrement AES-GCM (altéré ou mauvaise clé): %w", err)
	}

	return plaintext, nil
}
