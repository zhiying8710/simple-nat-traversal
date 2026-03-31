use std::net::SocketAddr;

use anyhow::{Context, Result};
use clap::Parser;
use minipunch_server::config::ServerConfig;
use minipunch_server::db::Database;
use minipunch_server::routes::build_router;
use tokio::net::TcpListener;
use tracing::info;
use tracing_subscriber::EnvFilter;

#[derive(Debug, Parser)]
struct Cli {
    #[arg(long, default_value = "127.0.0.1:9443")]
    listen_addr: String,
    #[arg(long, default_value = "minipunch.db")]
    database: std::path::PathBuf,
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let cli = Cli::parse();
    let config = ServerConfig {
        listen_addr: cli.listen_addr,
        database_path: cli.database,
    };
    run(config).await
}

async fn run(config: ServerConfig) -> Result<()> {
    let db = Database::open(&config.database_path)
        .await
        .with_context(|| format!("failed to open {}", config.database_path.display()))?;
    let app = build_router(db);
    let listen_addr: SocketAddr = config
        .listen_addr
        .parse()
        .with_context(|| format!("invalid listen address {}", config.listen_addr))?;
    let listener = TcpListener::bind(listen_addr)
        .await
        .with_context(|| format!("failed to bind {}", config.listen_addr))?;
    info!("minipunch-server listening on {}", config.listen_addr);
    axum::serve(listener, app)
        .await
        .context("server exited unexpectedly")
}
