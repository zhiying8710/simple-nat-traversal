use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use chacha20poly1305::aead::{Aead, Payload};
use chacha20poly1305::{ChaCha20Poly1305, KeyInit, Nonce};
use ed25519_dalek::{Signature, Signer, SigningKey, Verifier, VerifyingKey};
use rand::RngCore;
use rand::rngs::OsRng;
use sha2::{Digest, Sha256};
use thiserror::Error;
use x25519_dalek::{PublicKey as X25519PublicKey, StaticSecret};

#[derive(Debug, Error)]
pub enum CryptoError {
    #[error("invalid base64 payload")]
    InvalidBase64(#[from] base64::DecodeError),
    #[error("invalid signing key material")]
    InvalidSigningKey,
    #[error("invalid relay key material")]
    InvalidRelayKey,
    #[error("invalid verifying key material")]
    InvalidVerifyingKey,
    #[error("invalid signature")]
    InvalidSignature,
    #[error("relay encryption failed")]
    EncryptFailed,
    #[error("relay decryption failed")]
    DecryptFailed,
}

#[derive(Clone)]
pub struct DeviceIdentity {
    signing_key: SigningKey,
}

impl DeviceIdentity {
    pub fn generate() -> Self {
        let mut rng = OsRng;
        Self {
            signing_key: SigningKey::generate(&mut rng),
        }
    }

    pub fn from_private_key_base64(private_key: &str) -> Result<Self, CryptoError> {
        let bytes = URL_SAFE_NO_PAD.decode(private_key)?;
        let key_bytes: [u8; 32] = bytes
            .as_slice()
            .try_into()
            .map_err(|_| CryptoError::InvalidSigningKey)?;
        Ok(Self {
            signing_key: SigningKey::from_bytes(&key_bytes),
        })
    }

    pub fn private_key_base64(&self) -> String {
        URL_SAFE_NO_PAD.encode(self.signing_key.to_bytes())
    }

    pub fn public_key_base64(&self) -> String {
        URL_SAFE_NO_PAD.encode(self.signing_key.verifying_key().to_bytes())
    }

    pub fn sign_base64(&self, message: &str) -> String {
        let signature = self.signing_key.sign(message.as_bytes());
        URL_SAFE_NO_PAD.encode(signature.to_bytes())
    }

    pub fn device_id(&self) -> String {
        device_id_from_public_key(&self.public_key_base64())
    }
}

pub fn verify_signature_base64(
    public_key: &str,
    message: &str,
    signature: &str,
) -> Result<(), CryptoError> {
    let public_key_bytes = URL_SAFE_NO_PAD.decode(public_key)?;
    let verifying_key = VerifyingKey::from_bytes(
        &public_key_bytes
            .as_slice()
            .try_into()
            .map_err(|_| CryptoError::InvalidVerifyingKey)?,
    )
    .map_err(|_| CryptoError::InvalidVerifyingKey)?;

    let signature_bytes = URL_SAFE_NO_PAD.decode(signature)?;
    let signature = Signature::from_slice(signature_bytes.as_slice())
        .map_err(|_| CryptoError::InvalidSignature)?;

    verifying_key
        .verify(message.as_bytes(), &signature)
        .map_err(|_| CryptoError::InvalidSignature)
}

pub fn device_id_from_public_key(public_key: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(public_key.as_bytes());
    let digest = hasher.finalize();
    let digest_b64 = URL_SAFE_NO_PAD.encode(digest);
    format!("dev_{}", &digest_b64[..20])
}

pub fn hash_secret(secret: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(secret.as_bytes());
    URL_SAFE_NO_PAD.encode(hasher.finalize())
}

pub fn generate_token(prefix: &str) -> String {
    let mut rng = OsRng;
    let mut bytes = [0u8; 24];
    rng.fill_bytes(&mut bytes);
    format!("{prefix}_{}", URL_SAFE_NO_PAD.encode(bytes))
}

pub fn registration_message(device_id: &str, device_name: &str, os: &str, nonce: &str) -> String {
    format!("minipunch:register:{device_id}:{device_name}:{os}:{nonce}")
}

pub fn relay_key_binding_message(device_id: &str, relay_public_key: &str) -> String {
    format!("minipunch:relay-key:{device_id}:{relay_public_key}")
}

pub fn relay_channel_open_message(
    channel_id: &str,
    service_id: &str,
    source_device_id: &str,
    source_ephemeral_public_key: &str,
) -> String {
    format!(
        "minipunch:channel-open:{channel_id}:{service_id}:{source_device_id}:{source_ephemeral_public_key}"
    )
}

pub fn direct_probe_hello_message(
    rendezvous_id: &str,
    sender_device_id: &str,
    hello_nonce: &str,
) -> String {
    format!("minipunch:direct-probe:hello:{rendezvous_id}:{sender_device_id}:{hello_nonce}")
}

pub fn direct_probe_ack_message(
    rendezvous_id: &str,
    sender_device_id: &str,
    hello_nonce: &str,
    ack_nonce: &str,
) -> String {
    format!(
        "minipunch:direct-probe:ack:{rendezvous_id}:{sender_device_id}:{hello_nonce}:{ack_nonce}"
    )
}

pub fn service_id(owner_device_id: &str, service_name: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(owner_device_id.as_bytes());
    hasher.update(b":");
    hasher.update(service_name.as_bytes());
    let digest = hasher.finalize();
    let digest_b64 = URL_SAFE_NO_PAD.encode(digest);
    format!("svc_{}", &digest_b64[..20])
}

#[derive(Clone)]
pub struct RelayKeypair {
    secret_bytes: [u8; 32],
}

impl RelayKeypair {
    pub fn generate() -> Self {
        let mut secret_bytes = [0u8; 32];
        let mut rng = OsRng;
        rng.fill_bytes(&mut secret_bytes);
        Self { secret_bytes }
    }

    pub fn from_private_key_base64(private_key: &str) -> Result<Self, CryptoError> {
        let bytes = URL_SAFE_NO_PAD.decode(private_key)?;
        let secret_bytes: [u8; 32] = bytes
            .as_slice()
            .try_into()
            .map_err(|_| CryptoError::InvalidRelayKey)?;
        Ok(Self { secret_bytes })
    }

    pub fn private_key_base64(&self) -> String {
        URL_SAFE_NO_PAD.encode(self.secret_bytes)
    }

    pub fn public_key_base64(&self) -> String {
        let secret = StaticSecret::from(self.secret_bytes);
        let public = X25519PublicKey::from(&secret);
        URL_SAFE_NO_PAD.encode(public.as_bytes())
    }

    pub fn shared_secret_with_public_key(
        &self,
        peer_public_key: &str,
    ) -> Result<[u8; 32], CryptoError> {
        let peer_public_key_bytes = URL_SAFE_NO_PAD.decode(peer_public_key)?;
        let peer_public_key_bytes: [u8; 32] = peer_public_key_bytes
            .as_slice()
            .try_into()
            .map_err(|_| CryptoError::InvalidRelayKey)?;
        let peer_public = X25519PublicKey::from(peer_public_key_bytes);
        let secret = StaticSecret::from(self.secret_bytes);
        Ok(secret.diffie_hellman(&peer_public).to_bytes())
    }
}

#[derive(Debug, Clone, Copy)]
pub enum SecureChannelRole {
    Initiator,
    Responder,
}

pub struct SecureSender {
    cipher: ChaCha20Poly1305,
    aad: Vec<u8>,
    nonce_prefix: [u8; 4],
    counter: u64,
}

impl SecureSender {
    pub fn encrypt_to_base64(&mut self, plaintext: &[u8]) -> Result<String, CryptoError> {
        let nonce = Nonce::from(build_nonce(self.nonce_prefix, self.counter));
        self.counter = self.counter.saturating_add(1);
        let ciphertext = self
            .cipher
            .encrypt(
                &nonce,
                Payload {
                    msg: plaintext,
                    aad: &self.aad,
                },
            )
            .map_err(|_| CryptoError::EncryptFailed)?;
        Ok(URL_SAFE_NO_PAD.encode(ciphertext))
    }
}

pub struct SecureReceiver {
    cipher: ChaCha20Poly1305,
    aad: Vec<u8>,
    nonce_prefix: [u8; 4],
    counter: u64,
}

impl SecureReceiver {
    pub fn decrypt_from_base64(&mut self, ciphertext_base64: &str) -> Result<Vec<u8>, CryptoError> {
        let ciphertext = URL_SAFE_NO_PAD.decode(ciphertext_base64)?;
        let nonce = Nonce::from(build_nonce(self.nonce_prefix, self.counter));
        self.counter = self.counter.saturating_add(1);
        self.cipher
            .decrypt(
                &nonce,
                Payload {
                    msg: ciphertext.as_ref(),
                    aad: &self.aad,
                },
            )
            .map_err(|_| CryptoError::DecryptFailed)
    }
}

pub fn secure_channel_pair(
    shared_secret: [u8; 32],
    channel_id: &str,
    role: SecureChannelRole,
) -> (SecureSender, SecureReceiver) {
    let mut hasher = Sha256::new();
    hasher.update(shared_secret);
    hasher.update(b"minipunch:relay-data:v1:");
    hasher.update(channel_id.as_bytes());
    let key_material = hasher.finalize();
    let cipher = ChaCha20Poly1305::new_from_slice(key_material.as_slice())
        .expect("sha256 output is always 32 bytes");

    let aad = channel_id.as_bytes().to_vec();
    let (send_prefix, recv_prefix) = match role {
        SecureChannelRole::Initiator => ([0, 0, 0, 1], [0, 0, 0, 2]),
        SecureChannelRole::Responder => ([0, 0, 0, 2], [0, 0, 0, 1]),
    };

    (
        SecureSender {
            cipher: cipher.clone(),
            aad: aad.clone(),
            nonce_prefix: send_prefix,
            counter: 0,
        },
        SecureReceiver {
            cipher,
            aad,
            nonce_prefix: recv_prefix,
            counter: 0,
        },
    )
}

fn build_nonce(prefix: [u8; 4], counter: u64) -> [u8; 12] {
    let mut nonce = [0u8; 12];
    nonce[..4].copy_from_slice(&prefix);
    nonce[4..].copy_from_slice(&counter.to_be_bytes());
    nonce
}
