use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::{Json, extract::rejection::JsonRejection};
use minipunch_core::ErrorResponse;
use thiserror::Error;

pub type Result<T> = std::result::Result<T, ServerError>;

#[derive(Debug, Error)]
pub enum ServerError {
    #[error("unauthorized")]
    Unauthorized,
    #[error("forbidden")]
    Forbidden,
    #[error("{0}")]
    BadRequest(String),
    #[error("{0}")]
    Conflict(String),
    #[error("{0}")]
    NotFound(String),
    #[error("{0}")]
    Internal(String),
}

impl From<rusqlite::Error> for ServerError {
    fn from(value: rusqlite::Error) -> Self {
        Self::Internal(value.to_string())
    }
}

impl From<JsonRejection> for ServerError {
    fn from(value: JsonRejection) -> Self {
        Self::BadRequest(value.body_text())
    }
}

impl IntoResponse for ServerError {
    fn into_response(self) -> Response {
        let status = match self {
            ServerError::Unauthorized => StatusCode::UNAUTHORIZED,
            ServerError::Forbidden => StatusCode::FORBIDDEN,
            ServerError::BadRequest(_) => StatusCode::BAD_REQUEST,
            ServerError::Conflict(_) => StatusCode::CONFLICT,
            ServerError::NotFound(_) => StatusCode::NOT_FOUND,
            ServerError::Internal(_) => StatusCode::INTERNAL_SERVER_ERROR,
        };
        let body = Json(ErrorResponse {
            error: self.to_string(),
        });
        (status, body).into_response()
    }
}
