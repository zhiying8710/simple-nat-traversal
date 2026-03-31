use std::time::{SystemTime, UNIX_EPOCH};

pub fn unix_timestamp_now() -> i64 {
    let duration = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    duration.as_secs() as i64
}
