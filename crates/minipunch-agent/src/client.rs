use anyhow::{Context, Result, bail};
use minipunch_core::{
    DirectRendezvousSession, ErrorResponse, HeartbeatResponse, NetworkSnapshot,
    PendingDirectRendezvousResponse, RegisterDeviceRequest, RegisterDeviceResponse,
    ServiceDefinition, StartDirectRendezvousRequest, UpdateDirectRendezvousCandidatesRequest,
    UpsertServiceRequest,
};

#[derive(Clone)]
pub struct ControlPlaneClient {
    base_url: String,
    http: reqwest::Client,
}

impl ControlPlaneClient {
    pub fn new(base_url: impl Into<String>) -> Self {
        Self {
            base_url: base_url.into().trim_end_matches('/').to_string(),
            http: reqwest::Client::new(),
        }
    }

    pub async fn register_device(
        &self,
        request: &RegisterDeviceRequest,
    ) -> Result<RegisterDeviceResponse> {
        let response = self
            .http
            .post(self.endpoint("/api/v1/devices/register"))
            .json(request)
            .send()
            .await
            .context("failed to call register endpoint")?;
        parse_json_response(response).await
    }

    pub async fn heartbeat(&self, session_token: &str) -> Result<HeartbeatResponse> {
        let response = self
            .http
            .post(self.endpoint("/api/v1/devices/heartbeat"))
            .bearer_auth(session_token)
            .send()
            .await
            .context("failed to call heartbeat endpoint")?;
        parse_json_response(response).await
    }

    pub async fn upsert_service(
        &self,
        session_token: &str,
        request: &UpsertServiceRequest,
    ) -> Result<ServiceDefinition> {
        let response = self
            .http
            .post(self.endpoint("/api/v1/services/upsert"))
            .bearer_auth(session_token)
            .json(request)
            .send()
            .await
            .context("failed to call upsert service endpoint")?;
        parse_json_response(response).await
    }

    pub async fn network_snapshot(&self, session_token: &str) -> Result<NetworkSnapshot> {
        let response = self
            .http
            .get(self.endpoint("/api/v1/network"))
            .bearer_auth(session_token)
            .send()
            .await
            .context("failed to call network endpoint")?;
        parse_json_response(response).await
    }

    pub async fn start_direct_rendezvous(
        &self,
        session_token: &str,
        request: &StartDirectRendezvousRequest,
    ) -> Result<DirectRendezvousSession> {
        let response = self
            .http
            .post(self.endpoint("/api/v1/direct/rendezvous/start"))
            .bearer_auth(session_token)
            .json(request)
            .send()
            .await
            .context("failed to call start direct rendezvous endpoint")?;
        parse_json_response(response).await
    }

    pub async fn pending_direct_rendezvous(
        &self,
        session_token: &str,
    ) -> Result<PendingDirectRendezvousResponse> {
        let response = self
            .http
            .get(self.endpoint("/api/v1/direct/rendezvous/pending"))
            .bearer_auth(session_token)
            .send()
            .await
            .context("failed to call pending direct rendezvous endpoint")?;
        parse_json_response(response).await
    }

    pub async fn direct_rendezvous(
        &self,
        session_token: &str,
        rendezvous_id: &str,
    ) -> Result<DirectRendezvousSession> {
        let response = self
            .http
            .get(self.endpoint(&format!("/api/v1/direct/rendezvous/{rendezvous_id}")))
            .bearer_auth(session_token)
            .send()
            .await
            .context("failed to call direct rendezvous status endpoint")?;
        parse_json_response(response).await
    }

    pub async fn update_direct_rendezvous_candidates(
        &self,
        session_token: &str,
        rendezvous_id: &str,
        request: &UpdateDirectRendezvousCandidatesRequest,
    ) -> Result<DirectRendezvousSession> {
        let response = self
            .http
            .post(self.endpoint(&format!(
                "/api/v1/direct/rendezvous/{rendezvous_id}/candidates"
            )))
            .bearer_auth(session_token)
            .json(request)
            .send()
            .await
            .context("failed to call direct rendezvous candidates endpoint")?;
        parse_json_response(response).await
    }

    fn endpoint(&self, path: &str) -> String {
        format!("{}{}", self.base_url, path)
    }
}

async fn parse_json_response<T: serde::de::DeserializeOwned>(
    response: reqwest::Response,
) -> Result<T> {
    let status = response.status();
    if status.is_success() {
        return response
            .json::<T>()
            .await
            .context("failed to decode success response");
    }

    if let Ok(err) = response.json::<ErrorResponse>().await {
        bail!("server returned {}: {}", status, err.error);
    }

    bail!("server returned non-success status {}", status)
}
