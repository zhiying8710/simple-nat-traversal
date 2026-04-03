use std::path::PathBuf;
use std::time::Duration;

use anyhow::Result;
use clap::{Parser, Subcommand};
use minipunch_agent::AgentRuntime;
use minipunch_agent::config::{
    AgentConfig, DEFAULT_DIRECT_CANDIDATE_TYPE, DEFAULT_DIRECT_WAIT_SECONDS,
    DEFAULT_FORWARD_TRANSPORT, PublishedServiceConfig,
};
use minipunch_core::DirectConnectionCandidate;
use tracing_subscriber::EnvFilter;

#[derive(Debug, Parser)]
struct Cli {
    #[arg(long)]
    config: Option<PathBuf>,
    #[command(subcommand)]
    command: Command,
}

#[derive(Debug, Subcommand)]
enum Command {
    Init {
        #[arg(long)]
        server_url: String,
        #[arg(long)]
        join_token: String,
        #[arg(long)]
        device_name: Option<String>,
    },
    Heartbeat,
    Publish {
        #[arg(long)]
        name: String,
        #[arg(long, default_value = "127.0.0.1")]
        target_host: String,
        #[arg(long)]
        target_port: u16,
        #[arg(long = "allow")]
        allow: Vec<String>,
        #[arg(long, default_value_t = false)]
        enable_direct: bool,
        #[arg(long, default_value = "")]
        udp_bind: String,
        #[arg(long, default_value = DEFAULT_DIRECT_CANDIDATE_TYPE)]
        candidate_type: String,
        #[arg(long, default_value_t = DEFAULT_DIRECT_WAIT_SECONDS)]
        direct_wait_seconds: u64,
    },
    DeletePublish {
        #[arg(long)]
        name: String,
    },
    AddForward {
        #[arg(long)]
        name: String,
        #[arg(long)]
        target_device: String,
        #[arg(long)]
        service: String,
        #[arg(long)]
        local_bind: String,
        #[arg(long, default_value_t = true)]
        enabled: bool,
        #[arg(long, default_value = DEFAULT_FORWARD_TRANSPORT)]
        transport: String,
        #[arg(long, default_value = "")]
        udp_bind: String,
        #[arg(long, default_value = DEFAULT_DIRECT_CANDIDATE_TYPE)]
        candidate_type: String,
        #[arg(long, default_value_t = DEFAULT_DIRECT_WAIT_SECONDS)]
        direct_wait_seconds: u64,
    },
    DeleteForward {
        #[arg(long)]
        name: String,
    },
    Run,
    RelayServe,
    Forward {
        #[arg(long)]
        target_device: String,
        #[arg(long)]
        service: String,
        #[arg(long, default_value = "127.0.0.1:10022")]
        local_bind: String,
    },
    RendezvousStart {
        #[arg(long)]
        target_device: String,
        #[arg(long)]
        service: String,
        #[arg(long = "candidate")]
        candidate: Vec<String>,
    },
    RendezvousPending,
    RendezvousGet {
        #[arg(long)]
        rendezvous_id: String,
    },
    RendezvousAnnounce {
        #[arg(long)]
        rendezvous_id: String,
        #[arg(long = "candidate")]
        candidate: Vec<String>,
    },
    DirectProbe {
        #[arg(long)]
        rendezvous_id: String,
        #[arg(long)]
        bind: String,
        #[arg(long, default_value_t = 8)]
        wait_seconds: u64,
    },
    DirectConnect {
        #[arg(long)]
        target_device: String,
        #[arg(long)]
        service: String,
        #[arg(long)]
        bind: String,
        #[arg(long, default_value = "local")]
        candidate_type: String,
        #[arg(long, default_value_t = 8)]
        wait_seconds: u64,
    },
    DirectServe {
        #[arg(long)]
        service: String,
        #[arg(long)]
        bind: String,
        #[arg(long, default_value = "local")]
        candidate_type: String,
        #[arg(long, default_value_t = 8)]
        wait_seconds: u64,
    },
    DirectTcpForward {
        #[arg(long)]
        target_device: String,
        #[arg(long)]
        service: String,
        #[arg(long)]
        local_bind: String,
        #[arg(long)]
        udp_bind: String,
        #[arg(long, default_value = "local")]
        candidate_type: String,
        #[arg(long, default_value_t = 8)]
        wait_seconds: u64,
    },
    DirectTcpServe {
        #[arg(long)]
        service: String,
        #[arg(long)]
        udp_bind: String,
        #[arg(long, default_value = "local")]
        candidate_type: String,
        #[arg(long, default_value_t = 8)]
        wait_seconds: u64,
    },
    AutoForward {
        #[arg(long)]
        target_device: String,
        #[arg(long)]
        service: String,
        #[arg(long)]
        local_bind: String,
        #[arg(long)]
        udp_bind: String,
        #[arg(long, default_value = "local")]
        candidate_type: String,
        #[arg(long, default_value_t = 8)]
        direct_wait_seconds: u64,
    },
    AutoServe {
        #[arg(long)]
        service: String,
        #[arg(long)]
        udp_bind: String,
        #[arg(long, default_value = "local")]
        candidate_type: String,
        #[arg(long, default_value_t = 8)]
        direct_wait_seconds: u64,
    },
    Network,
    Status,
}

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let cli = Cli::parse();
    let config_path = match cli.config {
        Some(path) => path,
        None => AgentConfig::default_path()?,
    };

    match cli.command {
        Command::Init {
            server_url,
            join_token,
            device_name,
        } => {
            let device_name = device_name.unwrap_or_else(default_device_name);
            let runtime =
                AgentRuntime::init(&config_path, &server_url, &join_token, &device_name).await?;
            println!("{}", serde_json::to_string_pretty(runtime.config())?);
        }
        Command::Heartbeat => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime.heartbeat().await?;
            println!("heartbeat ok");
        }
        Command::Publish {
            name,
            target_host,
            target_port,
            allow,
            enable_direct,
            udp_bind,
            candidate_type,
            direct_wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let service = runtime
                .publish_service(PublishedServiceConfig {
                    name,
                    target_host,
                    target_port,
                    allowed_device_ids: allow,
                    direct_enabled: enable_direct,
                    direct_udp_bind_addr: udp_bind,
                    direct_candidate_type: candidate_type,
                    direct_wait_seconds,
                })
                .await?;
            println!("{}", serde_json::to_string_pretty(&service)?);
        }
        Command::DeletePublish { name } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let service = runtime.delete_published_service(&name).await?;
            println!("{}", serde_json::to_string_pretty(&service)?);
        }
        Command::Network => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let snapshot = runtime.network_snapshot().await?;
            println!("{}", serde_json::to_string_pretty(&snapshot)?);
        }
        Command::AddForward {
            name,
            target_device,
            service,
            local_bind,
            enabled,
            transport,
            udp_bind,
            candidate_type,
            direct_wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime.upsert_forward_rule(
                name,
                target_device,
                service,
                local_bind,
                enabled,
                transport,
                udp_bind,
                candidate_type,
                direct_wait_seconds,
            )?;
            println!("{}", serde_json::to_string_pretty(runtime.config())?);
        }
        Command::DeleteForward { name } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime.delete_forward_rule(&name)?;
            println!("{}", serde_json::to_string_pretty(runtime.config())?);
        }
        Command::Run => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime.run().await?;
        }
        Command::RelayServe => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime.relay_serve().await?;
        }
        Command::Forward {
            target_device,
            service,
            local_bind,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime
                .forward_service(target_device, service, local_bind)
                .await?;
        }
        Command::RendezvousStart {
            target_device,
            service,
            candidate,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let session = runtime
                .start_direct_rendezvous(target_device, service, parse_direct_candidates(candidate))
                .await?;
            println!("{}", serde_json::to_string_pretty(&session)?);
        }
        Command::RendezvousPending => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let pending = runtime.pending_direct_rendezvous().await?;
            println!("{}", serde_json::to_string_pretty(&pending)?);
        }
        Command::RendezvousGet { rendezvous_id } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let session = runtime.direct_rendezvous(&rendezvous_id).await?;
            println!("{}", serde_json::to_string_pretty(&session)?);
        }
        Command::RendezvousAnnounce {
            rendezvous_id,
            candidate,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let session = runtime
                .update_direct_rendezvous_candidates(
                    &rendezvous_id,
                    parse_direct_candidates(candidate),
                )
                .await?;
            println!("{}", serde_json::to_string_pretty(&session)?);
        }
        Command::DirectProbe {
            rendezvous_id,
            bind,
            wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let result = runtime
                .direct_probe(&rendezvous_id, bind, Duration::from_secs(wait_seconds))
                .await?;
            println!("{}", serde_json::to_string_pretty(&result)?);
        }
        Command::DirectConnect {
            target_device,
            service,
            bind,
            candidate_type,
            wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let result = runtime
                .direct_connect(
                    target_device,
                    service,
                    bind,
                    candidate_type,
                    Duration::from_secs(wait_seconds),
                )
                .await?;
            println!("{}", serde_json::to_string_pretty(&result)?);
        }
        Command::DirectServe {
            service,
            bind,
            candidate_type,
            wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime
                .direct_serve(
                    service,
                    bind,
                    candidate_type,
                    Duration::from_secs(wait_seconds),
                )
                .await?;
        }
        Command::DirectTcpForward {
            target_device,
            service,
            local_bind,
            udp_bind,
            candidate_type,
            wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime
                .direct_tcp_forward(
                    target_device,
                    service,
                    local_bind,
                    udp_bind,
                    candidate_type,
                    Duration::from_secs(wait_seconds),
                )
                .await?;
        }
        Command::DirectTcpServe {
            service,
            udp_bind,
            candidate_type,
            wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime
                .direct_tcp_serve(
                    service,
                    udp_bind,
                    candidate_type,
                    Duration::from_secs(wait_seconds),
                )
                .await?;
        }
        Command::AutoForward {
            target_device,
            service,
            local_bind,
            udp_bind,
            candidate_type,
            direct_wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime
                .auto_forward_service(
                    target_device,
                    service,
                    local_bind,
                    udp_bind,
                    candidate_type,
                    Duration::from_secs(direct_wait_seconds),
                )
                .await?;
        }
        Command::AutoServe {
            service,
            udp_bind,
            candidate_type,
            direct_wait_seconds,
        } => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            runtime
                .auto_serve(
                    service,
                    udp_bind,
                    candidate_type,
                    Duration::from_secs(direct_wait_seconds),
                )
                .await?;
        }
        Command::Status => {
            let mut runtime = AgentRuntime::load(&config_path).await?;
            let report = runtime.status_report().await;
            println!("{}", serde_json::to_string_pretty(&report)?);
        }
    }

    Ok(())
}

fn default_device_name() -> String {
    hostname::get()
        .ok()
        .and_then(|name| name.into_string().ok())
        .filter(|name| !name.trim().is_empty())
        .unwrap_or_else(|| "minipunch-device".to_string())
}

fn parse_direct_candidates(values: Vec<String>) -> Vec<DirectConnectionCandidate> {
    values
        .into_iter()
        .filter_map(|raw| {
            let raw = raw.trim().to_string();
            if raw.is_empty() {
                return None;
            }
            let (candidate_type, addr) = match raw.split_once('=') {
                Some((candidate_type, addr)) => (candidate_type.trim(), addr.trim()),
                None => ("manual", raw.as_str()),
            };
            Some(DirectConnectionCandidate {
                protocol: "udp".to_string(),
                addr: addr.to_string(),
                candidate_type: candidate_type.to_string(),
            })
        })
        .collect()
}
