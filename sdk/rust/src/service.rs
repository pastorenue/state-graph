use std::time::Duration;

use crate::workflow::HandlerFn;

#[repr(u8)]
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ServiceMode {
    Deployment = 0,
    Lambda     = 1,
}

impl Default for ServiceMode {
    fn default() -> Self { ServiceMode::Deployment }
}

pub struct ServiceDef {
    pub(crate) name:         String,
    pub(crate) handler:      Option<HandlerFn>,
    pub(crate) mode:         ServiceMode,
    pub(crate) port:         u16,
    pub(crate) min_scale:    u32,
    pub(crate) max_scale:    u32,
    pub(crate) ingress_host: Option<String>,
    pub(crate) timeout:      Duration,
}

impl ServiceDef {
    pub fn new(name: impl Into<String>) -> Self {
        ServiceDef {
            name:         name.into(),
            handler:      None,
            mode:         ServiceMode::Deployment,
            port:         8080,
            min_scale:    0,
            max_scale:    0,
            ingress_host: None,
            timeout:      Duration::from_secs(30),
        }
    }

    pub fn handler(mut self, fn_: HandlerFn) -> Self {
        self.handler = Some(fn_);
        self
    }

    pub fn mode(mut self, mode: ServiceMode) -> Self {
        self.mode = mode;
        self
    }

    pub fn port(mut self, port: u16) -> Self {
        self.port = port;
        self
    }

    pub fn scale(mut self, min: u32, max: u32) -> Self {
        self.min_scale = min;
        self.max_scale = max;
        self
    }

    pub fn expose(mut self, host: impl Into<String>) -> Self {
        self.ingress_host = Some(host.into());
        self
    }

    pub fn timeout(mut self, d: Duration) -> Self {
        self.timeout = d;
        self
    }

    pub fn validate(&self) -> Result<(), String> {
        if self.mode == ServiceMode::Deployment && self.min_scale < 1 {
            return Err(format!(
                "service {:?}: Deployment mode requires min_scale >= 1",
                self.name
            ));
        }
        Ok(())
    }
}

pub fn new_service(name: impl Into<String>) -> ServiceDef {
    ServiceDef::new(name)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_service_deployment_requires_min_scale() {
        let svc = new_service("svc").mode(ServiceMode::Deployment).scale(0, 5);
        assert!(svc.validate().unwrap_err().contains("min_scale"));
    }

    #[test]
    fn test_service_lambda_no_constraint() {
        let svc = new_service("svc").mode(ServiceMode::Lambda).scale(0, 5);
        assert!(svc.validate().is_ok());
    }

    #[test]
    fn test_service_deployment_valid() {
        let svc = new_service("svc").mode(ServiceMode::Deployment).scale(1, 5);
        assert!(svc.validate().is_ok());
    }
}
