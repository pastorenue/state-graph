pub mod workflow;
pub mod service;
pub mod runner;

pub use workflow::{Workflow, StepBuilder, step, SUCCEED, FAIL};
pub use service::{ServiceDef, ServiceMode};
pub use runner::{run, run_service, run_local};

/// Input received by a state handler. Must be JSON-serialisable.
pub type Input = std::collections::HashMap<String, serde_json::Value>;

/// Output returned by a state handler. Must be JSON-serialisable.
pub type Output = std::collections::HashMap<String, serde_json::Value>;

/// Wraps a user-facing error message from a handler.
#[derive(Debug, thiserror::Error)]
#[error("{0}")]
pub struct Error(pub String);

impl From<&str> for Error {
    fn from(s: &str) -> Self { Error(s.to_string()) }
}
impl From<String> for Error {
    fn from(s: String) -> Self { Error(s) }
}
