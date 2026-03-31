use std::path::PathBuf;

#[derive(Debug, Clone)]
pub struct ServerConfig {
    pub listen_addr: String,
    pub database_path: PathBuf,
}

impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            listen_addr: "127.0.0.1:9443".to_string(),
            database_path: PathBuf::from("minipunch.db"),
        }
    }
}
