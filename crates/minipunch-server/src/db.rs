use std::sync::Arc;

use minipunch_core::{
    AdminClearDevicesResponse, AdminDevicesResponse, BootstrapInitResponse, CreateJoinTokenRequest, DeviceSummary,
    DirectConnectionCandidate, DirectRendezvousParticipant, DirectRendezvousSession,
    HeartbeatResponse, JoinTokenResponse, NetworkSnapshot, PendingDirectRendezvousResponse,
    RegisterDeviceRequest, RegisterDeviceResponse, ServiceDefinition, StartDirectRendezvousRequest,
    UpdateDirectRendezvousCandidatesRequest, UpsertServiceRequest, device_id_from_public_key,
    generate_token, hash_secret, registration_message, relay_key_binding_message, service_id,
    unix_timestamp_now, verify_signature_base64,
};
use rusqlite::{Connection, OptionalExtension, params};
use tokio::sync::Mutex;

use crate::error::{Result, ServerError};

const JOIN_TOKEN_DEFAULT_TTL_MINUTES: i64 = 10;
const LEGACY_SESSION_TTL_SECONDS: i64 = 24 * 60 * 60;
const ONLINE_WINDOW_SECONDS: i64 = 90;
const DIRECT_RENDEZVOUS_TTL_SECONDS: i64 = 45;

#[derive(Clone)]
pub struct Database {
    conn: Arc<Mutex<Connection>>,
}

impl Database {
    pub async fn open(path: impl AsRef<std::path::Path>) -> Result<Self> {
        let conn = Connection::open(path)?;
        let db = Self {
            conn: Arc::new(Mutex::new(conn)),
        };
        db.init_schema().await?;
        Ok(db)
    }

    async fn init_schema(&self) -> Result<()> {
        let conn = self.conn.lock().await;
        conn.execute_batch(
            r#"
            PRAGMA journal_mode = WAL;
            CREATE TABLE IF NOT EXISTS server_config (
                key TEXT PRIMARY KEY,
                value TEXT NOT NULL
            );
            CREATE TABLE IF NOT EXISTS join_tokens (
                token_hash TEXT PRIMARY KEY,
                note TEXT,
                expires_at INTEGER NOT NULL,
                used_at INTEGER,
                created_at INTEGER NOT NULL
            );
            CREATE TABLE IF NOT EXISTS devices (
                device_id TEXT PRIMARY KEY,
                device_name TEXT NOT NULL,
                public_key TEXT NOT NULL UNIQUE,
                relay_public_key TEXT,
                relay_public_key_signature TEXT,
                os TEXT NOT NULL,
                version TEXT NOT NULL,
                session_deadline_at INTEGER,
                created_at INTEGER NOT NULL,
                last_seen_at INTEGER NOT NULL
            );
            CREATE TABLE IF NOT EXISTS device_sessions (
                token_hash TEXT PRIMARY KEY,
                device_id TEXT NOT NULL,
                created_at INTEGER NOT NULL,
                expires_at INTEGER NOT NULL,
                FOREIGN KEY(device_id) REFERENCES devices(device_id)
            );
            CREATE TABLE IF NOT EXISTS services (
                service_id TEXT PRIMARY KEY,
                owner_device_id TEXT NOT NULL,
                service_name TEXT NOT NULL,
                protocol TEXT NOT NULL,
                target_host TEXT NOT NULL,
                target_port INTEGER NOT NULL,
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL,
                UNIQUE(owner_device_id, service_name),
                FOREIGN KEY(owner_device_id) REFERENCES devices(device_id)
            );
            CREATE TABLE IF NOT EXISTS service_acl (
                service_id TEXT NOT NULL,
                source_device_id TEXT NOT NULL,
                PRIMARY KEY(service_id, source_device_id),
                FOREIGN KEY(service_id) REFERENCES services(service_id)
            );
            CREATE TABLE IF NOT EXISTS direct_rendezvous_attempts (
                rendezvous_id TEXT PRIMARY KEY,
                service_id TEXT NOT NULL,
                service_name TEXT NOT NULL,
                source_device_id TEXT NOT NULL,
                target_device_id TEXT NOT NULL,
                source_announced_at INTEGER,
                target_announced_at INTEGER,
                source_candidates_json TEXT NOT NULL,
                target_candidates_json TEXT NOT NULL,
                created_at INTEGER NOT NULL,
                updated_at INTEGER NOT NULL,
                expires_at INTEGER NOT NULL,
                FOREIGN KEY(service_id) REFERENCES services(service_id),
                FOREIGN KEY(source_device_id) REFERENCES devices(device_id),
                FOREIGN KEY(target_device_id) REFERENCES devices(device_id)
            );
            "#,
        )?;
        add_column_if_missing(
            &conn,
            "ALTER TABLE devices ADD COLUMN relay_public_key TEXT",
        )?;
        add_column_if_missing(
            &conn,
            "ALTER TABLE devices ADD COLUMN relay_public_key_signature TEXT",
        )?;
        add_column_if_missing(
            &conn,
            "ALTER TABLE devices ADD COLUMN session_deadline_at INTEGER",
        )?;
        Ok(())
    }

    pub async fn bootstrap_init(&self) -> Result<BootstrapInitResponse> {
        let mut conn = self.conn.lock().await;
        let already_initialized = conn
            .query_row(
                "SELECT value FROM server_config WHERE key = 'admin_token_hash'",
                [],
                |row| row.get::<_, String>(0),
            )
            .optional()?
            .is_some();
        if already_initialized {
            return Err(ServerError::Conflict(
                "server is already initialized".to_string(),
            ));
        }

        let now = unix_timestamp_now();
        let admin_token = generate_token("adm");
        let join_token = generate_token("join");
        let join_token_expires_at = now + JOIN_TOKEN_DEFAULT_TTL_MINUTES * 60;
        let tx = conn.transaction()?;
        tx.execute(
            "INSERT INTO server_config(key, value) VALUES('admin_token_hash', ?1)",
            params![hash_secret(&admin_token)],
        )?;
        tx.execute(
            "INSERT INTO join_tokens(token_hash, note, expires_at, created_at) VALUES(?1, ?2, ?3, ?4)",
            params![hash_secret(&join_token), Some("bootstrap".to_string()), join_token_expires_at, now],
        )?;
        tx.commit()?;

        Ok(BootstrapInitResponse {
            admin_token,
            first_join_token: join_token,
            join_token_expires_at,
        })
    }

    pub async fn create_join_token(
        &self,
        admin_token: &str,
        request: CreateJoinTokenRequest,
    ) -> Result<JoinTokenResponse> {
        self.ensure_admin(admin_token).await?;
        let now = unix_timestamp_now();
        let join_token = generate_token("join");
        let ttl_minutes = request
            .expires_in_minutes
            .unwrap_or(JOIN_TOKEN_DEFAULT_TTL_MINUTES as u64) as i64;
        let expires_at = now + ttl_minutes * 60;
        let conn = self.conn.lock().await;
        conn.execute(
            "INSERT INTO join_tokens(token_hash, note, expires_at, created_at) VALUES(?1, ?2, ?3, ?4)",
            params![hash_secret(&join_token), request.note.clone(), expires_at, now],
        )?;
        Ok(JoinTokenResponse {
            join_token,
            expires_at,
            note: request.note,
        })
    }

    pub async fn register_device(
        &self,
        request: RegisterDeviceRequest,
    ) -> Result<RegisterDeviceResponse> {
        let expected_device_id = device_id_from_public_key(&request.public_key);
        if request.device_id != expected_device_id {
            return Err(ServerError::BadRequest(
                "device_id does not match public_key".to_string(),
            ));
        }

        let message = registration_message(
            &request.device_id,
            &request.device_name,
            &request.os,
            &request.nonce,
        );
        verify_signature_base64(&request.public_key, &message, &request.signature)
            .map_err(|err| ServerError::BadRequest(err.to_string()))?;
        verify_signature_base64(
            &request.public_key,
            &relay_key_binding_message(&request.device_id, &request.relay_public_key),
            &request.relay_public_key_signature,
        )
        .map_err(|err| ServerError::BadRequest(err.to_string()))?;

        let now = unix_timestamp_now();
        let mut conn = self.conn.lock().await;

        let existing_device = conn
            .query_row(
                "SELECT public_key, session_deadline_at FROM devices WHERE device_id = ?1",
                params![request.device_id],
                |row| Ok((row.get::<_, String>(0)?, row.get::<_, Option<i64>>(1)?)),
            )
            .optional()?;

        let session_expires_at = match existing_device {
            Some((public_key, session_deadline_at)) => {
                if public_key != request.public_key {
                    return Err(ServerError::Forbidden);
                }
                if let Some(deadline) = session_deadline_at
                    && deadline < now
                {
                    return Err(ServerError::Unauthorized);
                }
                conn.execute(
                    "UPDATE devices SET device_name = ?1, relay_public_key = ?2, relay_public_key_signature = ?3, os = ?4, version = ?5, last_seen_at = ?6 WHERE device_id = ?7",
                    params![
                        request.device_name,
                        request.relay_public_key,
                        request.relay_public_key_signature,
                        request.os,
                        request.version,
                        now,
                        request.device_id
                    ],
                )?;
                session_deadline_at.unwrap_or(now + LEGACY_SESSION_TTL_SECONDS)
            }
            None => {
                let join_token = request
                    .join_token
                    .as_deref()
                    .ok_or_else(|| ServerError::Unauthorized)?;
                let session_deadline_at =
                    self.consume_join_token_locked(&mut conn, join_token, now)?;
                conn.execute(
                    "INSERT INTO devices(device_id, device_name, public_key, relay_public_key, relay_public_key_signature, os, version, session_deadline_at, created_at, last_seen_at) VALUES(?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)",
                    params![
                        request.device_id,
                        request.device_name,
                        request.public_key,
                        request.relay_public_key,
                        request.relay_public_key_signature,
                        request.os,
                        request.version,
                        session_deadline_at,
                        now,
                        now
                    ],
                )?;
                session_deadline_at
            }
        };

        let session_token = generate_token("sess");
        conn.execute(
            "DELETE FROM device_sessions WHERE device_id = ?1",
            params![request.device_id],
        )?;
        conn.execute(
            "INSERT INTO device_sessions(token_hash, device_id, created_at, expires_at) VALUES(?1, ?2, ?3, ?4)",
            params![
                hash_secret(&session_token),
                request.device_id,
                now,
                session_expires_at
            ],
        )?;

        Ok(RegisterDeviceResponse {
            device_id: request.device_id,
            session_token,
            session_expires_at,
        })
    }

    pub async fn heartbeat(&self, session_token: &str) -> Result<HeartbeatResponse> {
        let device_id = self.session_device_id(session_token).await?;
        let now = unix_timestamp_now();
        let conn = self.conn.lock().await;
        conn.execute(
            "UPDATE devices SET last_seen_at = ?1 WHERE device_id = ?2",
            params![now, device_id],
        )?;
        Ok(HeartbeatResponse {
            device_id,
            seen_at: now,
        })
    }

    pub async fn touch_device(&self, device_id: &str) -> Result<()> {
        let now = unix_timestamp_now();
        let conn = self.conn.lock().await;
        conn.execute(
            "UPDATE devices SET last_seen_at = ?1 WHERE device_id = ?2",
            params![now, device_id],
        )?;
        Ok(())
    }

    pub async fn upsert_service(
        &self,
        session_token: &str,
        request: UpsertServiceRequest,
    ) -> Result<ServiceDefinition> {
        if request.name.trim().is_empty() {
            return Err(ServerError::BadRequest(
                "service name cannot be empty".to_string(),
            ));
        }

        let device_id = self.session_device_id(session_token).await?;
        let service_id = service_id(&device_id, &request.name);
        let now = unix_timestamp_now();
        let mut conn = self.conn.lock().await;
        let tx = conn.transaction()?;
        tx.execute(
            r#"
            INSERT INTO services(service_id, owner_device_id, service_name, protocol, target_host, target_port, created_at, updated_at)
            VALUES(?1, ?2, ?3, 'tcp', ?4, ?5, ?6, ?7)
            ON CONFLICT(service_id) DO UPDATE SET
                target_host = excluded.target_host,
                target_port = excluded.target_port,
                updated_at = excluded.updated_at
            "#,
            params![
                service_id,
                device_id,
                request.name,
                "",
                0,
                now,
                now
            ],
        )?;
        tx.execute(
            "DELETE FROM service_acl WHERE service_id = ?1",
            params![service_id],
        )?;
        for allowed_device_id in &request.allowed_device_ids {
            tx.execute(
                "INSERT INTO service_acl(service_id, source_device_id) VALUES(?1, ?2)",
                params![service_id, allowed_device_id],
            )?;
        }
        tx.commit()?;

        Ok(ServiceDefinition {
            service_id,
            owner_device_id: device_id,
            name: request.name,
            protocol: "tcp".to_string(),
        })
    }

    pub async fn network_snapshot(&self, session_token: &str) -> Result<NetworkSnapshot> {
        let requester_device_id = self.session_device_id(session_token).await?;
        let conn = self.conn.lock().await;
        let now = unix_timestamp_now();

        let mut devices_stmt = conn.prepare(
            "SELECT device_id, device_name, os, public_key, relay_public_key, relay_public_key_signature, last_seen_at FROM devices ORDER BY device_name ASC",
        )?;
        let device_rows = devices_stmt.query_map([], |row| {
            let last_seen_at: i64 = row.get(6)?;
            Ok(DeviceSummary {
                device_id: row.get(0)?,
                device_name: row.get(1)?,
                os: row.get(2)?,
                identity_public_key: row.get(3)?,
                relay_public_key: row.get(4)?,
                relay_public_key_signature: row.get(5)?,
                last_seen_at,
                is_online: now - last_seen_at <= ONLINE_WINDOW_SECONDS,
            })
        })?;
        let mut devices = Vec::new();
        for row in device_rows {
            devices.push(row?);
        }

        let mut services_stmt = conn.prepare(
            r#"
            SELECT
                s.service_id,
                s.owner_device_id,
                s.service_name,
                s.protocol
            FROM services s
            WHERE s.owner_device_id = ?1
               OR EXISTS (
                    SELECT 1 FROM service_acl acl
                    WHERE acl.service_id = s.service_id AND acl.source_device_id = ?1
               )
            ORDER BY s.owner_device_id ASC, s.service_name ASC
            "#,
        )?;
        let service_rows =
            services_stmt.query_map(params![requester_device_id.clone()], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, String>(3)?,
                ))
            })?;
        let mut services = Vec::new();
        for row in service_rows {
            let (service_id, owner_device_id, name, protocol) = row?;
            services.push(ServiceDefinition {
                service_id,
                owner_device_id,
                name,
                protocol,
            });
        }

        Ok(NetworkSnapshot {
            requester_device_id,
            devices,
            services,
        })
    }

    pub async fn admin_devices(&self, admin_token: &str) -> Result<AdminDevicesResponse> {
        self.ensure_admin(admin_token).await?;
        let conn = self.conn.lock().await;
        let now = unix_timestamp_now();
        let mut stmt = conn.prepare(
            "SELECT device_id, device_name, os, public_key, relay_public_key, relay_public_key_signature, last_seen_at FROM devices ORDER BY device_name ASC",
        )?;
        let rows = stmt.query_map([], |row| {
            let last_seen_at: i64 = row.get(6)?;
            Ok(DeviceSummary {
                device_id: row.get(0)?,
                device_name: row.get(1)?,
                os: row.get(2)?,
                identity_public_key: row.get(3)?,
                relay_public_key: row.get(4)?,
                relay_public_key_signature: row.get(5)?,
                last_seen_at,
                is_online: now - last_seen_at <= ONLINE_WINDOW_SECONDS,
            })
        })?;
        let mut devices = Vec::new();
        for row in rows {
            devices.push(row?);
        }
        Ok(AdminDevicesResponse { devices })
    }

    pub async fn clear_devices(&self, admin_token: &str) -> Result<AdminClearDevicesResponse> {
        self.ensure_admin(admin_token).await?;
        let mut conn = self.conn.lock().await;
        let tx = conn.transaction()?;

        let deleted_rendezvous_attempts =
            tx.execute("DELETE FROM direct_rendezvous_attempts", [])? as usize;
        let deleted_acl_entries = tx.execute("DELETE FROM service_acl", [])? as usize;
        let deleted_services = tx.execute("DELETE FROM services", [])? as usize;
        let deleted_sessions = tx.execute("DELETE FROM device_sessions", [])? as usize;
        let deleted_devices = tx.execute("DELETE FROM devices", [])? as usize;

        tx.commit()?;

        Ok(AdminClearDevicesResponse {
            deleted_devices,
            deleted_sessions,
            deleted_services,
            deleted_acl_entries,
            deleted_rendezvous_attempts,
        })
    }

    pub async fn start_direct_rendezvous(
        &self,
        session_token: &str,
        request: StartDirectRendezvousRequest,
    ) -> Result<DirectRendezvousSession> {
        if request.target_device_id.trim().is_empty() {
            return Err(ServerError::BadRequest(
                "target_device_id cannot be empty".to_string(),
            ));
        }
        if request.service_name.trim().is_empty() {
            return Err(ServerError::BadRequest(
                "service_name cannot be empty".to_string(),
            ));
        }
        validate_direct_candidates(&request.source_candidates)?;

        let source_device_id = self.session_device_id(session_token).await?;
        let direct_service_id = service_id(&request.target_device_id, &request.service_name);
        let service = self
            .authorize_relay_service(&source_device_id, &direct_service_id)
            .await?;
        let now = unix_timestamp_now();
        let expires_at = now + DIRECT_RENDEZVOUS_TTL_SECONDS;
        let rendezvous_id = generate_token("rvz");
        let source_candidates_json = serde_json::to_string(&request.source_candidates)
            .map_err(|err| ServerError::Internal(err.to_string()))?;
        let target_candidates_json =
            serde_json::to_string(&Vec::<DirectConnectionCandidate>::new())
                .map_err(|err| ServerError::Internal(err.to_string()))?;
        let conn = self.conn.lock().await;
        conn.execute(
            r#"
            INSERT INTO direct_rendezvous_attempts(
                rendezvous_id,
                service_id,
                service_name,
                source_device_id,
                target_device_id,
                source_announced_at,
                target_announced_at,
                source_candidates_json,
                target_candidates_json,
                created_at,
                updated_at,
                expires_at
            ) VALUES(?1, ?2, ?3, ?4, ?5, ?6, NULL, ?7, ?8, ?9, ?10, ?11)
            "#,
            params![
                rendezvous_id,
                service.service_id,
                service.name,
                source_device_id,
                service.owner_device_id,
                now,
                source_candidates_json,
                target_candidates_json,
                now,
                now,
                expires_at
            ],
        )?;
        self.direct_rendezvous_by_id_for_device(&conn, &rendezvous_id, None)
    }

    pub async fn pending_direct_rendezvous(
        &self,
        session_token: &str,
    ) -> Result<PendingDirectRendezvousResponse> {
        let target_device_id = self.session_device_id(session_token).await?;
        let conn = self.conn.lock().await;
        let now = unix_timestamp_now();
        let mut stmt = conn.prepare(
            r#"
            SELECT rendezvous_id
            FROM direct_rendezvous_attempts
            WHERE target_device_id = ?1 AND expires_at >= ?2
            ORDER BY updated_at DESC, created_at DESC
            "#,
        )?;
        let rows = stmt.query_map(params![target_device_id, now], |row| {
            row.get::<_, String>(0)
        })?;
        let mut attempts = Vec::new();
        for row in rows {
            let rendezvous_id = row?;
            attempts.push(self.direct_rendezvous_by_id_for_device(&conn, &rendezvous_id, None)?);
        }
        Ok(PendingDirectRendezvousResponse { attempts })
    }

    pub async fn direct_rendezvous(
        &self,
        session_token: &str,
        rendezvous_id: &str,
    ) -> Result<DirectRendezvousSession> {
        let device_id = self.session_device_id(session_token).await?;
        let conn = self.conn.lock().await;
        self.direct_rendezvous_by_id_for_device(&conn, rendezvous_id, Some(device_id.as_str()))
    }

    pub async fn update_direct_rendezvous_candidates(
        &self,
        session_token: &str,
        rendezvous_id: &str,
        request: UpdateDirectRendezvousCandidatesRequest,
    ) -> Result<DirectRendezvousSession> {
        validate_direct_candidates(&request.candidates)?;
        let device_id = self.session_device_id(session_token).await?;
        let now = unix_timestamp_now();
        let candidates_json = serde_json::to_string(&request.candidates)
            .map_err(|err| ServerError::Internal(err.to_string()))?;

        let conn = self.conn.lock().await;
        let row = conn
            .query_row(
                "SELECT source_device_id, target_device_id, expires_at FROM direct_rendezvous_attempts WHERE rendezvous_id = ?1",
                params![rendezvous_id],
                |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, String>(1)?,
                        row.get::<_, i64>(2)?,
                    ))
                },
            )
            .optional()?
            .ok_or_else(|| ServerError::NotFound("direct rendezvous was not found".to_string()))?;

        if row.2 < now {
            return Err(ServerError::Conflict(
                "direct rendezvous has already expired".to_string(),
            ));
        }

        if row.0 == device_id {
            conn.execute(
                r#"
                UPDATE direct_rendezvous_attempts
                SET source_candidates_json = ?1, source_announced_at = ?2, updated_at = ?3
                WHERE rendezvous_id = ?4
                "#,
                params![candidates_json, now, now, rendezvous_id],
            )?;
        } else if row.1 == device_id {
            conn.execute(
                r#"
                UPDATE direct_rendezvous_attempts
                SET target_candidates_json = ?1, target_announced_at = ?2, updated_at = ?3
                WHERE rendezvous_id = ?4
                "#,
                params![candidates_json, now, now, rendezvous_id],
            )?;
        } else {
            return Err(ServerError::Forbidden);
        }

        self.direct_rendezvous_by_id_for_device(&conn, rendezvous_id, Some(device_id.as_str()))
    }

    pub async fn device_identity_public_key(&self, device_id: &str) -> Result<String> {
        let conn = self.conn.lock().await;
        conn.query_row(
            "SELECT public_key FROM devices WHERE device_id = ?1",
            params![device_id],
            |row| row.get::<_, String>(0),
        )
        .optional()?
        .ok_or_else(|| ServerError::NotFound("device was not found".to_string()))
    }

    pub async fn authorize_relay_service(
        &self,
        source_device_id: &str,
        service_id: &str,
    ) -> Result<ServiceDefinition> {
        let conn = self.conn.lock().await;
        let service = conn
            .query_row(
                r#"
                SELECT service_id, owner_device_id, service_name, protocol
                FROM services
                WHERE service_id = ?1
                "#,
                params![service_id],
                |row| {
                    Ok(ServiceDefinition {
                        service_id: row.get(0)?,
                        owner_device_id: row.get(1)?,
                        name: row.get(2)?,
                        protocol: row.get(3)?,
                    })
                },
            )
            .optional()?
            .ok_or_else(|| ServerError::NotFound("service was not found".to_string()))?;

        if service.owner_device_id != source_device_id {
            let is_allowed = conn
                .query_row(
                    "SELECT 1 FROM service_acl WHERE service_id = ?1 AND source_device_id = ?2",
                    params![service_id, source_device_id],
                    |_| Ok(()),
                )
                .optional()?
                .is_some();
            if !is_allowed {
                return Err(ServerError::Forbidden);
            }
        }

        Ok(service)
    }

    async fn ensure_admin(&self, admin_token: &str) -> Result<()> {
        let conn = self.conn.lock().await;
        let stored_hash = conn
            .query_row(
                "SELECT value FROM server_config WHERE key = 'admin_token_hash'",
                [],
                |row| row.get::<_, String>(0),
            )
            .optional()?
            .ok_or(ServerError::Unauthorized)?;
        if stored_hash == hash_secret(admin_token) {
            Ok(())
        } else {
            Err(ServerError::Unauthorized)
        }
    }

    pub async fn session_device_id(&self, session_token: &str) -> Result<String> {
        let now = unix_timestamp_now();
        let conn = self.conn.lock().await;
        let row = conn
            .query_row(
                "SELECT device_id, expires_at FROM device_sessions WHERE token_hash = ?1",
                params![hash_secret(session_token)],
                |row| Ok((row.get::<_, String>(0)?, row.get::<_, i64>(1)?)),
            )
            .optional()?;

        match row {
            Some((device_id, expires_at)) if expires_at >= now => Ok(device_id),
            _ => Err(ServerError::Unauthorized),
        }
    }

    fn consume_join_token_locked(
        &self,
        conn: &mut Connection,
        join_token: &str,
        now: i64,
    ) -> Result<i64> {
        let row = conn
            .query_row(
                "SELECT expires_at, used_at FROM join_tokens WHERE token_hash = ?1",
                params![hash_secret(join_token)],
                |row| Ok((row.get::<_, i64>(0)?, row.get::<_, Option<i64>>(1)?)),
            )
            .optional()?;

        match row {
            Some((expires_at, None)) if expires_at >= now => {
                conn.execute(
                    "UPDATE join_tokens SET used_at = ?1 WHERE token_hash = ?2",
                    params![now, hash_secret(join_token)],
                )?;
                Ok(expires_at)
            }
            Some((expires_at, _)) if expires_at < now => Err(ServerError::Unauthorized),
            _ => Err(ServerError::Unauthorized),
        }
    }

    fn direct_rendezvous_by_id_for_device(
        &self,
        conn: &Connection,
        rendezvous_id: &str,
        viewer_device_id: Option<&str>,
    ) -> Result<DirectRendezvousSession> {
        let row = conn
            .query_row(
                r#"
                SELECT
                    rendezvous_id,
                    service_id,
                    service_name,
                    source_device_id,
                    target_device_id,
                    source_announced_at,
                    target_announced_at,
                    source_candidates_json,
                    target_candidates_json,
                    created_at,
                    updated_at,
                    expires_at
                FROM direct_rendezvous_attempts
                WHERE rendezvous_id = ?1
                "#,
                params![rendezvous_id],
                |row| {
                    Ok(DirectRendezvousRow {
                        rendezvous_id: row.get(0)?,
                        service_id: row.get(1)?,
                        service_name: row.get(2)?,
                        source_device_id: row.get(3)?,
                        target_device_id: row.get(4)?,
                        source_announced_at: row.get(5)?,
                        target_announced_at: row.get(6)?,
                        source_candidates_json: row.get(7)?,
                        target_candidates_json: row.get(8)?,
                        created_at: row.get(9)?,
                        updated_at: row.get(10)?,
                        expires_at: row.get(11)?,
                    })
                },
            )
            .optional()?
            .ok_or_else(|| ServerError::NotFound("direct rendezvous was not found".to_string()))?;

        if let Some(viewer_device_id) = viewer_device_id {
            if viewer_device_id != row.source_device_id && viewer_device_id != row.target_device_id
            {
                return Err(ServerError::Forbidden);
            }
        }

        direct_rendezvous_from_row(row)
    }
}

fn add_column_if_missing(conn: &Connection, sql: &str) -> Result<()> {
    match conn.execute(sql, []) {
        Ok(_) => Ok(()),
        Err(rusqlite::Error::SqliteFailure(_, Some(message)))
            if message.contains("duplicate column name") =>
        {
            Ok(())
        }
        Err(err) => Err(ServerError::from(err)),
    }
}

#[derive(Debug)]
struct DirectRendezvousRow {
    rendezvous_id: String,
    service_id: String,
    service_name: String,
    source_device_id: String,
    target_device_id: String,
    source_announced_at: Option<i64>,
    target_announced_at: Option<i64>,
    source_candidates_json: String,
    target_candidates_json: String,
    created_at: i64,
    updated_at: i64,
    expires_at: i64,
}

fn validate_direct_candidates(candidates: &[DirectConnectionCandidate]) -> Result<()> {
    for candidate in candidates {
        if candidate.protocol.trim().is_empty() {
            return Err(ServerError::BadRequest(
                "direct candidate protocol cannot be empty".to_string(),
            ));
        }
        if candidate.addr.trim().is_empty() {
            return Err(ServerError::BadRequest(
                "direct candidate addr cannot be empty".to_string(),
            ));
        }
        if candidate.candidate_type.trim().is_empty() {
            return Err(ServerError::BadRequest(
                "direct candidate type cannot be empty".to_string(),
            ));
        }
    }
    Ok(())
}

fn direct_rendezvous_from_row(row: DirectRendezvousRow) -> Result<DirectRendezvousSession> {
    let source_candidates =
        serde_json::from_str::<Vec<DirectConnectionCandidate>>(&row.source_candidates_json)
            .map_err(|err| ServerError::Internal(err.to_string()))?;
    let target_candidates =
        serde_json::from_str::<Vec<DirectConnectionCandidate>>(&row.target_candidates_json)
            .map_err(|err| ServerError::Internal(err.to_string()))?;
    let now = unix_timestamp_now();
    let status = if row.expires_at < now {
        "expired".to_string()
    } else if !source_candidates.is_empty() && !target_candidates.is_empty() {
        "ready".to_string()
    } else if row.target_announced_at.is_none() {
        "waiting_for_target".to_string()
    } else {
        "gathering_candidates".to_string()
    };

    Ok(DirectRendezvousSession {
        rendezvous_id: row.rendezvous_id,
        service_id: row.service_id,
        service_name: row.service_name,
        source_device_id: row.source_device_id.clone(),
        target_device_id: row.target_device_id.clone(),
        status,
        created_at: row.created_at,
        updated_at: row.updated_at,
        expires_at: row.expires_at,
        source: DirectRendezvousParticipant {
            device_id: row.source_device_id,
            announced_at: row.source_announced_at,
            candidates: source_candidates,
        },
        target: DirectRendezvousParticipant {
            device_id: row.target_device_id,
            announced_at: row.target_announced_at,
            candidates: target_candidates,
        },
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use minipunch_core::{DeviceIdentity, RelayKeypair};

    fn test_db_path(name: &str) -> std::path::PathBuf {
        let mut path = std::env::temp_dir();
        path.push(format!(
            "minipunch-server-test-{name}-{}.db",
            generate_token("tmp")
        ));
        path
    }

    async fn register_test_device(
        db: &Database,
        join_token: String,
        device_name: &str,
    ) -> RegisterDeviceResponse {
        let identity = DeviceIdentity::generate();
        let relay_identity = RelayKeypair::generate();
        register_test_device_with_identity(
            db,
            Some(join_token),
            device_name,
            &identity,
            &relay_identity,
        )
        .await
    }

    async fn register_test_device_with_identity(
        db: &Database,
        join_token: Option<String>,
        device_name: &str,
        identity: &DeviceIdentity,
        relay_identity: &RelayKeypair,
    ) -> RegisterDeviceResponse {
        let device_id = identity.device_id();
        let nonce = generate_token("nonce");
        db.register_device(RegisterDeviceRequest {
            join_token,
            device_id: device_id.clone(),
            device_name: device_name.to_string(),
            os: "test-os".to_string(),
            version: "test-version".to_string(),
            public_key: identity.public_key_base64(),
            relay_public_key: relay_identity.public_key_base64(),
            relay_public_key_signature: identity.sign_base64(&relay_key_binding_message(
                &device_id,
                &relay_identity.public_key_base64(),
            )),
            nonce: nonce.clone(),
            signature: identity.sign_base64(&registration_message(
                &device_id,
                device_name,
                "test-os",
                &nonce,
            )),
        })
        .await
        .expect("register test device")
    }

    #[tokio::test]
    async fn first_session_deadline_matches_join_token_expiry() {
        let db_path = test_db_path("session-deadline");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: Some(24 * 60 * 30),
                    note: Some("deadline-test".to_string()),
                },
            )
            .await
            .expect("create join token");

        let registered = register_test_device(&db, join.join_token.clone(), "deadline-target").await;
        assert_eq!(registered.session_expires_at, join.expires_at);

        let _ = std::fs::remove_file(db_path);
    }

    #[tokio::test]
    async fn refreshed_session_keeps_original_join_token_deadline() {
        let db_path = test_db_path("session-refresh");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: Some(24 * 60 * 30),
                    note: Some("refresh-test".to_string()),
                },
            )
            .await
            .expect("create join token");

        let identity = DeviceIdentity::generate();
        let relay_identity = RelayKeypair::generate();
        let first = register_test_device_with_identity(
            &db,
            Some(join.join_token.clone()),
            "refresh-target",
            &identity,
            &relay_identity,
        )
        .await;
        let refreshed = register_test_device_with_identity(
            &db,
            None,
            "refresh-target",
            &identity,
            &relay_identity,
        )
        .await;

        assert_eq!(first.session_expires_at, join.expires_at);
        assert_eq!(refreshed.session_expires_at, join.expires_at);

        let _ = std::fs::remove_file(db_path);
    }

    #[tokio::test]
    async fn upsert_service_scrubs_legacy_target_metadata_and_snapshot_stays_minimal() {
        let db_path = test_db_path("metadata");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let source_join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: None,
                    note: Some("source".to_string()),
                },
            )
            .await
            .expect("create source join token");

        let target = register_test_device(&db, bootstrap.first_join_token, "target").await;
        let source = register_test_device(&db, source_join.join_token, "source").await;

        let service_id = service_id(&target.device_id, "ssh");
        let now = unix_timestamp_now();
        {
            let conn = db.conn.lock().await;
            conn.execute(
                "INSERT INTO services(service_id, owner_device_id, service_name, protocol, target_host, target_port, created_at, updated_at) VALUES(?1, ?2, ?3, 'tcp', ?4, ?5, ?6, ?7)",
                params![service_id, target.device_id, "ssh", "127.0.0.1", 22, now, now],
            )
            .expect("seed legacy service row");
        }

        db.upsert_service(
            &target.session_token,
            UpsertServiceRequest {
                name: "ssh".to_string(),
                allowed_device_ids: vec![source.device_id.clone()],
            },
        )
        .await
        .expect("upsert service");

        {
            let conn = db.conn.lock().await;
            let (target_host, target_port): (String, i64) = conn
                .query_row(
                    "SELECT target_host, target_port FROM services WHERE service_id = ?1",
                    params![service_id],
                    |row| Ok((row.get(0)?, row.get(1)?)),
                )
                .expect("read stored service metadata");
            assert_eq!(target_host, "");
            assert_eq!(target_port, 0);
        }

        let snapshot = db
            .network_snapshot(&source.session_token)
            .await
            .expect("load network snapshot");
        assert_eq!(snapshot.services.len(), 1);
        assert_eq!(snapshot.services[0].service_id, service_id);
        assert_eq!(snapshot.services[0].owner_device_id, target.device_id);
        assert_eq!(snapshot.services[0].name, "ssh");
        assert_eq!(snapshot.services[0].protocol, "tcp");

        let authorized = db
            .authorize_relay_service(&source.device_id, &snapshot.services[0].service_id)
            .await
            .expect("authorize relay service");
        assert_eq!(authorized.service_id, snapshot.services[0].service_id);
        assert_eq!(authorized.name, "ssh");
        assert_eq!(authorized.protocol, "tcp");

        let _ = std::fs::remove_file(db_path);
    }

    #[tokio::test]
    async fn clear_devices_removes_registered_state() {
        let db_path = test_db_path("clear-devices");
        let db = Database::open(&db_path).await.expect("open test db");
        let bootstrap = db.bootstrap_init().await.expect("bootstrap server");
        let source_join = db
            .create_join_token(
                &bootstrap.admin_token,
                CreateJoinTokenRequest {
                    expires_in_minutes: None,
                    note: Some("source".to_string()),
                },
            )
            .await
            .expect("create source join token");

        let target = register_test_device(&db, bootstrap.first_join_token, "target").await;
        let source = register_test_device(&db, source_join.join_token, "source").await;

        db.upsert_service(
            &target.session_token,
            UpsertServiceRequest {
                name: "ssh".to_string(),
                allowed_device_ids: vec![source.device_id.clone()],
            },
        )
        .await
        .expect("upsert service");

        let cleared = db
            .clear_devices(&bootstrap.admin_token)
            .await
            .expect("clear devices");
        assert_eq!(cleared.deleted_devices, 2);
        assert_eq!(cleared.deleted_sessions, 2);
        assert_eq!(cleared.deleted_services, 1);
        assert_eq!(cleared.deleted_acl_entries, 1);

        let devices = db
            .admin_devices(&bootstrap.admin_token)
            .await
            .expect("list devices after clear");
        assert!(devices.devices.is_empty());
        assert!(db.network_snapshot(&source.session_token).await.is_err());

        let _ = std::fs::remove_file(db_path);
    }
}
