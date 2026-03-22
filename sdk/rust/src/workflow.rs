use std::collections::HashMap;
use std::future::Future;
use std::pin::Pin;
use std::time::Duration;

use crate::{Error, Input, Output};

/// Terminal success sentinel — must match Go/Python SDK values exactly.
pub const SUCCEED: &str = "__succeed__";

/// Terminal failure sentinel — must match Go/Python SDK values exactly.
pub const FAIL: &str = "__fail__";

pub type HandlerFn = Box<
    dyn Fn(Input) -> Pin<Box<dyn Future<Output = Result<Output, Error>> + Send>>
        + Send
        + Sync,
>;

pub type ChoiceFn = Box<
    dyn Fn(Input) -> Pin<Box<dyn Future<Output = Result<String, Error>> + Send>>
        + Send
        + Sync,
>;

#[derive(Debug, Clone, Default)]
pub struct RetryPolicy {
    pub max_attempts:    u32,
    pub backoff_seconds: u64,
}

pub(crate) enum StateKind {
    Task,
    Choice,
    Wait(Duration),
}

pub struct TaskDef {
    pub(crate) name:           String,
    pub(crate) handler:        Option<HandlerFn>,
    pub(crate) choice_handler: Option<ChoiceFn>,
    pub(crate) service_target: Option<String>,
    pub(crate) retry:          Option<RetryPolicy>,
    pub(crate) catch:          Option<String>,
    pub(crate) kind:           StateKind,
}

impl TaskDef {
    fn new_task(name: String, handler: Option<HandlerFn>) -> Self {
        TaskDef {
            name,
            handler,
            choice_handler: None,
            service_target: None,
            retry:          None,
            catch:          None,
            kind:           StateKind::Task,
        }
    }

    fn new_choice(name: String, handler: ChoiceFn) -> Self {
        TaskDef {
            name,
            handler:        None,
            choice_handler: Some(handler),
            service_target: None,
            retry:          None,
            catch:          None,
            kind:           StateKind::Choice,
        }
    }

    fn new_wait(name: String, dur: Duration) -> Self {
        TaskDef {
            name,
            handler:        None,
            choice_handler: None,
            service_target: None,
            retry:          None,
            catch:          None,
            kind:           StateKind::Wait(dur),
        }
    }

    pub fn invoke_service(&mut self, service_name: impl Into<String>) -> &mut Self {
        self.service_target = Some(service_name.into());
        self
    }

    pub fn retry(&mut self, policy: RetryPolicy) -> &mut Self {
        self.retry = Some(policy);
        self
    }

    pub fn catch(&mut self, state_name: impl Into<String>) -> &mut Self {
        self.catch = Some(state_name.into());
        self
    }

    pub fn is_choice(&self) -> bool { matches!(self.kind, StateKind::Choice) }
    pub fn is_wait(&self) -> bool   { matches!(self.kind, StateKind::Wait(_)) }

    pub fn wait_duration(&self) -> Option<Duration> {
        if let StateKind::Wait(d) = &self.kind { Some(*d) } else { None }
    }
}

/// Returns a new StepBuilder for the named state.
pub fn step(name: impl Into<String>) -> StepBuilder {
    StepBuilder::new(name.into())
}

#[derive(Clone)]
pub struct StepBuilder {
    pub(crate) name:   String,
    pub(crate) next:   Option<String>,
    pub(crate) catch:  Option<String>,
    pub(crate) retry:  Option<RetryPolicy>,
    pub(crate) is_end: bool,
}

impl StepBuilder {
    pub fn new(name: String) -> Self {
        StepBuilder { name, next: None, catch: None, retry: None, is_end: false }
    }

    pub fn next(mut self, state_name: impl Into<String>) -> Self {
        self.next = Some(state_name.into());
        self
    }

    pub fn catch(mut self, state_name: impl Into<String>) -> Self {
        self.catch = Some(state_name.into());
        self
    }

    pub fn retry(mut self, policy: RetryPolicy) -> Self {
        self.retry = Some(policy);
        self
    }

    /// Equivalent to .next(SUCCEED).
    pub fn end(mut self) -> Self {
        self.next   = Some(SUCCEED.to_string());
        self.is_end = true;
        self
    }
}

pub struct Workflow {
    pub(crate) name:  String,
    pub(crate) image: String,
    pub(crate) tasks: HashMap<String, TaskDef>,
    pub(crate) names: Vec<String>, // insertion-order for duplicate detection
    pub(crate) steps: Vec<StepBuilder>,
}

impl Workflow {
    pub fn new(name: impl Into<String>) -> Self {
        Workflow {
            name:  name.into(),
            image: String::new(),
            tasks: HashMap::new(),
            names: Vec::new(),
            steps: Vec::new(),
        }
    }

    /// Set the container image for K8s Job execution. Empty = in-process.
    pub fn with_image(mut self, image: impl Into<String>) -> Self {
        self.image = image.into();
        self
    }

    pub fn task(&mut self, name: impl Into<String>, handler: Option<HandlerFn>) -> &mut TaskDef {
        let n = name.into();
        let td = TaskDef::new_task(n.clone(), handler);
        self.names.push(n.clone());
        self.tasks.insert(n.clone(), td);
        self.tasks.get_mut(&n).unwrap()
    }

    pub fn choice(&mut self, name: impl Into<String>, handler: ChoiceFn) -> &mut TaskDef {
        let n = name.into();
        let td = TaskDef::new_choice(n.clone(), handler);
        self.names.push(n.clone());
        self.tasks.insert(n.clone(), td);
        self.tasks.get_mut(&n).unwrap()
    }

    pub fn wait(&mut self, name: impl Into<String>, duration: Duration) -> &mut TaskDef {
        let n = name.into();
        let td = TaskDef::new_wait(n.clone(), duration);
        self.names.push(n.clone());
        self.tasks.insert(n.clone(), td);
        self.tasks.get_mut(&n).unwrap()
    }

    pub fn flow(&mut self, steps: Vec<StepBuilder>) -> &mut Self {
        self.steps = steps;
        self
    }

    pub fn validate(&self) -> Result<(), String> {
        // 1. flow() must have at least one step.
        if self.steps.is_empty() {
            return Err("flow() has not been called or contains no steps".to_string());
        }

        // 2. Globally unique state names.
        let mut seen = std::collections::HashSet::new();
        for n in &self.names {
            if !seen.insert(n) {
                return Err(format!("duplicate state name: {n:?}"));
            }
        }

        // 3. Each task has exactly one of: handler or invoke_service.
        for (name, td) in &self.tasks {
            if td.is_choice() || td.is_wait() { continue; }
            let has_handler  = td.handler.is_some();
            let has_service  = td.service_target.is_some();
            if has_handler && has_service {
                return Err(format!(
                    "state {name:?}: cannot have both a handler and invoke_service"
                ));
            }
            if !has_handler && !has_service {
                return Err(format!(
                    "state {name:?}: must have either a handler or invoke_service"
                ));
            }
        }

        // 4. Every next/catch target resolves to a registered state, SUCCEED, or FAIL.
        let valid: std::collections::HashSet<&str> = self
            .tasks
            .keys()
            .map(|s| s.as_str())
            .chain([SUCCEED, FAIL])
            .collect();

        for sb in &self.steps {
            if let Some(n) = &sb.next {
                if !valid.contains(n.as_str()) {
                    return Err(format!(
                        "step {:?}: next target {n:?} is not a registered state",
                        sb.name
                    ));
                }
            }
            if let Some(c) = &sb.catch {
                if !valid.contains(c.as_str()) {
                    return Err(format!(
                        "step {:?}: catch target {c:?} is not a registered state",
                        sb.name
                    ));
                }
            }
        }

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn handler() -> Option<HandlerFn> {
        Some(Box::new(|input| Box::pin(async move { Ok(input) })))
    }

    fn choice_fn() -> ChoiceFn {
        Box::new(|_| Box::pin(async move { Ok(SUCCEED.to_string()) }))
    }

    #[test]
    fn test_sentinels() {
        assert_eq!(SUCCEED, "__succeed__");
        assert_eq!(FAIL,    "__fail__");
    }

    #[test]
    fn test_with_image_sets_field() {
        let wf = Workflow::new("test").with_image("my-image:latest");
        assert_eq!(wf.image, "my-image:latest");
    }

    #[test]
    fn test_image_default_empty() {
        let wf = Workflow::new("test");
        assert!(wf.image.is_empty());
    }

    #[test]
    fn test_validate_ok() {
        let mut wf = Workflow::new("test");
        wf.task("A", handler());
        wf.flow(vec![step("A").end()]);
        assert!(wf.validate().is_ok());
    }

    #[test]
    fn test_validate_no_flow() {
        let mut wf = Workflow::new("test");
        wf.task("A", handler());
        assert!(wf.validate().is_err());
    }

    #[test]
    fn test_validate_duplicate_state() {
        let mut wf = Workflow::new("test");
        wf.task("A", handler());
        wf.task("A", handler()); // duplicate
        wf.flow(vec![step("A").end()]);
        assert!(wf.validate().unwrap_err().contains("duplicate"));
    }

    #[test]
    fn test_validate_handler_and_service() {
        let mut wf = Workflow::new("test");
        wf.task("A", handler()).invoke_service("svc");
        wf.flow(vec![step("A").end()]);
        assert!(wf.validate().unwrap_err().contains("both"));
    }

    #[test]
    fn test_validate_no_handler_no_service() {
        let mut wf = Workflow::new("test");
        wf.task("A", None); // no handler, no service
        wf.flow(vec![step("A").end()]);
        assert!(wf.validate().unwrap_err().contains("must have"));
    }

    #[test]
    fn test_validate_unknown_next_target() {
        let mut wf = Workflow::new("test");
        wf.task("A", handler());
        wf.flow(vec![step("A").next("NonExistent")]);
        assert!(wf.validate().unwrap_err().contains("not a registered state"));
    }

    #[test]
    fn test_validate_unknown_catch_target() {
        let mut wf = Workflow::new("test");
        wf.task("A", handler());
        wf.flow(vec![step("A").catch("Ghost").end()]);
        assert!(wf.validate().unwrap_err().contains("not a registered state"));
    }

    #[test]
    fn test_validate_choice_ok() {
        let mut wf = Workflow::new("test");
        wf.choice("Route", choice_fn());
        wf.task("Next", handler());
        wf.flow(vec![step("Route").next("Next"), step("Next").end()]);
        assert!(wf.validate().is_ok());
    }
}
