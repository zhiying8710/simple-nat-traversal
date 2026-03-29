package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

const (
	passwordHashIterations = 600_000
	networkKeyIterations   = 210_000
	keyLength              = 32
)

func RandomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func RandomID(prefix string) (string, error) {
	raw, err := RandomBytes(10)
	if err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func RandomToken() (string, error) {
	raw, err := RandomBytes(24)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func HashPassword(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, keyLength)
}

func VerifyPassword(password string, salt, expected []byte) (bool, error) {
	got, err := HashPassword(password, salt)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, expected) == 1, nil
}

func DeriveNetworkKey(password string) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, []byte("simple-nat-traversal/single-network"), networkKeyIterations, keyLength)
}

func ComputePunchMAC(networkKey []byte, fromID string, nonce, public []byte) []byte {
	mac := hmac.New(sha256.New, networkKey)
	mac.Write([]byte("punch_hello"))
	mac.Write([]byte(fromID))
	mac.Write(nonce)
	mac.Write(public)
	return mac.Sum(nil)
}

func VerifyPunchMAC(networkKey []byte, fromID string, nonce, public, expected []byte) bool {
	got := ComputePunchMAC(networkKey, fromID, nonce, public)
	return hmac.Equal(got, expected)
}

func NewEphemeralKey() (*ecdh.PrivateKey, []byte, []byte, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	pub := priv.PublicKey().Bytes()
	nonce, err := RandomBytes(16)
	if err != nil {
		return nil, nil, nil, err
	}
	return priv, pub, nonce, nil
}

func ParsePeerPublicKey(raw []byte) (*ecdh.PublicKey, error) {
	return ecdh.X25519().NewPublicKey(raw)
}

func NewIdentityKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return pub, priv, nil
}

func ParseIdentityPublicKey(raw []byte) (ed25519.PublicKey, error) {
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid identity public key size: %d", len(raw))
	}
	return ed25519.PublicKey(slices.Clone(raw)), nil
}

func EncodeIdentityPrivate(private ed25519.PrivateKey) (string, error) {
	if len(private) != ed25519.PrivateKeySize {
		return "", errors.New("invalid identity private key")
	}
	return base64.RawURLEncoding.EncodeToString(private.Seed()), nil
}

func ParseIdentityPrivate(encoded string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, nil, err
	}

	switch len(raw) {
	case ed25519.SeedSize:
		private := ed25519.NewKeyFromSeed(raw)
		public := private.Public().(ed25519.PublicKey)
		return public, private, nil
	case ed25519.PrivateKeySize:
		private := ed25519.PrivateKey(slices.Clone(raw))
		public := private.Public().(ed25519.PublicKey)
		return public, private, nil
	default:
		return nil, nil, fmt.Errorf("invalid identity private key size: %d", len(raw))
	}
}

func SignPunchHello(private ed25519.PrivateKey, fromID string, nonce, public []byte) ([]byte, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid identity private key")
	}
	message := punchHelloSignatureInput(fromID, nonce, public)
	return ed25519.Sign(private, message), nil
}

func VerifyPunchHelloSignature(publicKey ed25519.PublicKey, fromID string, nonce, public, signature []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	message := punchHelloSignatureInput(fromID, nonce, public)
	return ed25519.Verify(publicKey, message, signature)
}

func DeriveSessionKey(networkKey []byte, localID, remoteID string, localNonce, remoteNonce, localPub, remotePub []byte, sharedSecret []byte) ([]byte, error) {
	if len(sharedSecret) == 0 {
		return nil, errors.New("shared secret is empty")
	}
	type side struct {
		id    string
		nonce []byte
		pub   []byte
	}
	parts := []side{
		{id: localID, nonce: localNonce, pub: localPub},
		{id: remoteID, nonce: remoteNonce, pub: remotePub},
	}
	slices.SortFunc(parts, func(a, b side) int {
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		default:
			return 0
		}
	})
	info := fmt.Sprintf("snt-session:%s:%x:%x:%x:%x", parts[0].id, parts[0].nonce, parts[1].nonce, parts[0].pub, parts[1].pub)
	return hkdf.Key(sha256.New, sharedSecret, networkKey, info, 32)
}

func EncryptPacket(sessionKey []byte, seq uint64, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonceFromSeq(seq), plaintext, nil), nil
}

func DecryptPacket(sessionKey []byte, seq uint64, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(sessionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonceFromSeq(seq), ciphertext, nil)
}

func nonceFromSeq(seq uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}

func punchHelloSignatureInput(fromID string, nonce, public []byte) []byte {
	h := sha256.New()
	h.Write([]byte("snt-punch-hello-signature-v1"))
	writeLengthPrefixed(h, []byte(fromID))
	writeLengthPrefixed(h, nonce)
	writeLengthPrefixed(h, public)
	return h.Sum(nil)
}

func writeLengthPrefixed(h io.Writer, value []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(value)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write(value)
}
