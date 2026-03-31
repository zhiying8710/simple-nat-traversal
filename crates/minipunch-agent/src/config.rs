use std::fs;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, anyhow};
use serde::{Deserialize, Serialize};

use minipunch_core::unix_timestamp_now;

pub const DEFAULT_DIRECT_CANDIDATE_TYPE: &str = "local";
pub const DEFAULT_DIRECT_WAIT_SECONDS: u64 = 5;
pub const DEFAULT_FORWARD_TRANSPORT: &str = "relay";

fn default_direct_candidate_type() -> String {
    DEFAULT_DIRECT_CANDIDATE_TYPE.to_string()
}

fn default_direct_wait_seconds() -> u64 {
    DEFAULT_DIRECT_WAIT_SECONDS
}

fn default_forward_transport() -> String {
    DEFAULT_FORWARD_TRANSPORT.to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PublishedServiceConfig {
    pub name: String,
    pub target_host: String,
    pub target_port: u16,
    #[serde(default)]
    pub allowed_device_ids: Vec<String>,
    #[serde(default)]
    pub direct_enabled: bool,
    #[serde(default)]
    pub direct_udp_bind_addr: String,
    #[serde(default = "default_direct_candidate_type")]
    pub direct_candidate_type: String,
    #[serde(default = "default_direct_wait_seconds")]
    pub direct_wait_seconds: u64,
}

impl Default for PublishedServiceConfig {
    fn default() -> Self {
        Self {
            name: String::new(),
            target_host: String::new(),
            target_port: 0,
            allowed_device_ids: Vec::new(),
            direct_enabled: false,
            direct_udp_bind_addr: String::new(),
            direct_candidate_type: default_direct_candidate_type(),
            direct_wait_seconds: default_direct_wait_seconds(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LocalForwardConfig {
    pub name: String,
    pub target_device_id: String,
    pub service_name: String,
    pub local_bind_addr: String,
    pub enabled: bool,
    #[serde(default = "default_forward_transport")]
    pub transport_mode: String,
    #[serde(default)]
    pub direct_udp_bind_addr: String,
    #[serde(default = "default_direct_candidate_type")]
    pub direct_candidate_type: String,
    #[serde(default = "default_direct_wait_seconds")]
    pub direct_wait_seconds: u64,
}

impl Default for LocalForwardConfig {
    fn default() -> Self {
        Self {
            name: String::new(),
            target_device_id: String::new(),
            service_name: String::new(),
            local_bind_addr: "127.0.0.1:10022".to_string(),
            enabled: true,
            transport_mode: default_forward_transport(),
            direct_udp_bind_addr: String::new(),
            direct_candidate_type: default_direct_candidate_type(),
            direct_wait_seconds: default_direct_wait_seconds(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct AgentConfig {
    pub server_url: String,
    pub device_name: String,
    pub device_id: Option<String>,
    pub private_key_base64: Option<String>,
    pub relay_private_key_base64: Option<String>,
    pub session_token: Option<String>,
    pub session_expires_at: Option<i64>,
    #[serde(default)]
    pub published_services: Vec<PublishedServiceConfig>,
    #[serde(default)]
    pub forward_rules: Vec<LocalForwardConfig>,
}

impl AgentConfig {
    pub fn load(path: &Path) -> Result<Self> {
        let raw = fs::read_to_string(path)
            .with_context(|| format!("failed to read config {}", path.display()))?;
        toml::from_str(&raw).with_context(|| format!("failed to parse config {}", path.display()))
    }

    pub fn load_or_default(path: &Path) -> Result<Self> {
        if path.exists() {
            Self::load(path)
        } else {
            Ok(Self::default())
        }
    }

    pub fn save(&self, path: &Path) -> Result<()> {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).with_context(|| {
                format!("failed to create config directory {}", parent.display())
            })?;
        }
        let encoded = toml::to_string_pretty(self).context("failed to encode config")?;
        fs::write(path, encoded)
            .with_context(|| format!("failed to write config {}", path.display()))
    }

    pub fn default_path() -> Result<PathBuf> {
        let config_dir =
            dirs::config_dir().ok_or_else(|| anyhow!("unable to determine config directory"))?;
        Ok(config_dir.join("minipunch").join("agent.toml"))
    }

    pub fn has_valid_session(&self) -> bool {
        match (&self.session_token, self.session_expires_at) {
            (Some(_), Some(expires_at)) => expires_at > unix_timestamp_now() + 60,
            _ => false,
        }
    }
}
