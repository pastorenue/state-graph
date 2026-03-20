use std::collections::HashMap;

use crate::workflow::{RetryPolicy, Workflow, FAIL, SUCCEED};
use crate::service::ServiceDef;
use crate::{Input, Output};

/// Entry point for workflow execution.
///
/// Dispatch priority:
/// 1. --state=<name>   → execute one state via RunnerService gRPC, then exit.
/// 2. --service=<name> → enter service execution path.
/// 3. (no flag)        → validate, serialise workflow, POST to Control Plane.
///
/// TODO(Phase 13): implement gRPC runner protocol using tonic stubs generated
/// from proto/kflow/v1/runner.proto (tonic-build in build.rs).
pub fn run(wf: Workflow) {
    if let Some(state_name) = flag("state") {
        run_state(wf, &state_name);
        return;
    }
    if flag("service").is_some() {
        eprintln!("kflow: --service flag passed to run(); use run_service() for service dispatch");
        std::process::exit(1);
    }
    // Registration path — validate then POST to Control Plane.
    if let Err(e) = wf.validate() {
        panic!("kflow::run: invalid workflow: {e}");
    }
    post_workflow(&wf);
}

/// Entry point for service registration and execution.
///
/// Dispatch priority:
/// 1. --service=<name> matches svc → execute service worker.
/// 2. (no match) → validate svc, POST definition to Control Plane.
///
/// TODO(Phase 13): implement gRPC runner protocol.
pub fn run_service(svc: ServiceDef) {
    if let Some(name) = flag("service") {
        if name == svc.name {
            run_service_worker(svc);
            return;
        }
    }
    if let Err(e) = svc.validate() {
        panic!("kflow::run_service: invalid service: {e}");
    }
    post_service(&svc);
}

/// Runs a workflow entirely in-process without any Control Plane or Kubernetes.
///
/// WARNING: for local development and unit testing only. Never use in production.
///
/// Returns the final Output when the workflow reaches a terminal state, or
/// Err if the workflow fails without a Catch handler.
pub fn run_local(wf: Workflow, input: Input) -> Result<Output, String> {
    if let Err(e) = wf.validate() {
        return Err(format!("run_local: invalid workflow: {e}"));
    }

    let graph = build_graph(&wf);
    let entry = wf.steps.first().map(|s| s.name.clone()).ok_or("run_local: no steps defined")?;

    let rt = tokio::runtime::Runtime::new().map_err(|e| e.to_string())?;
    rt.block_on(execute_workflow(&wf, &graph, entry, input))
}

// ---------------------------------------------------------------------------
// Graph types
// ---------------------------------------------------------------------------

struct Node {
    next:  Option<String>,
    catch: Option<String>,
    retry: Option<RetryPolicy>,
}

fn build_graph(wf: &Workflow) -> HashMap<String, Node> {
    let mut graph = HashMap::new();
    for sb in &wf.steps {
        let td    = wf.tasks.get(&sb.name);
        let catch = sb.catch.clone().or_else(|| td.and_then(|t| t.catch.clone()));
        let retry = sb.retry.clone().or_else(|| td.and_then(|t| t.retry.clone()));
        graph.insert(sb.name.clone(), Node {
            next:  sb.next.clone(),
            catch,
            retry,
        });
    }
    graph
}

// ---------------------------------------------------------------------------
// Async execution
// ---------------------------------------------------------------------------

async fn execute_workflow(
    wf:     &Workflow,
    graph:  &HashMap<String, Node>,
    entry:  String,
    input:  Input,
) -> Result<Output, String> {
    let mut current     = input;
    let mut node_name   = entry;

    loop {
        if node_name == SUCCEED || node_name == FAIL {
            return Ok(current);
        }

        let node = graph.get(&node_name).ok_or_else(|| {
            format!("run_local: state {node_name:?} not found in graph")
        })?;

        let td = wf.tasks.get(&node_name).ok_or_else(|| {
            format!("run_local: state {node_name:?} not registered")
        })?;

        let result = execute_state(td, node, current.clone()).await;

        match result {
            Ok(output) => {
                if td.is_choice() {
                    // Choice: key is the next state name.
                    let choice = output
                        .get("__choice__")
                        .and_then(|v| v.as_str())
                        .unwrap_or(FAIL)
                        .to_string();
                    current   = current; // pass same input to choice target
                    node_name = choice;
                } else {
                    let next = node.next.clone().unwrap_or_else(|| FAIL.to_string());
                    if next == SUCCEED || next == FAIL {
                        return Ok(output);
                    }
                    current   = output;
                    node_name = next;
                }
            }
            Err(e) => {
                match &node.catch {
                    Some(catch_name) if catch_name != SUCCEED && catch_name != FAIL => {
                        current.insert("_error".to_string(), serde_json::Value::String(e));
                        node_name = catch_name.clone();
                    }
                    _ => return Err(e),
                }
            }
        }
    }
}

async fn execute_state(
    td:    &crate::workflow::TaskDef,
    node:  &Node,
    input: Input,
) -> Result<Output, String> {
    // Wait state
    if td.is_wait() {
        if let Some(dur) = td.wait_duration() {
            tokio::time::sleep(dur).await;
        }
        return Ok(Output::new());
    }

    let policy = node.retry.as_ref();
    let max_attempts    = policy.map(|p| p.max_attempts.max(1)).unwrap_or(1);
    let backoff_seconds = policy.map(|p| p.backoff_seconds).unwrap_or(0);

    let mut last_err = String::new();

    for attempt in 0..max_attempts {
        if attempt > 0 && backoff_seconds > 0 {
            tokio::time::sleep(std::time::Duration::from_secs(backoff_seconds)).await;
        }

        let result = if td.is_choice() {
            if let Some(fn_) = &td.choice_handler {
                fn_(input.clone())
                    .await
                    .map(|choice| {
                        let mut out = Output::new();
                        out.insert("__choice__".to_string(), serde_json::Value::String(choice));
                        out
                    })
                    .map_err(|e| e.to_string())
            } else {
                Err(format!("state {:?}: choice handler not set", td.name))
            }
        } else if let Some(fn_) = &td.handler {
            fn_(input.clone()).await.map_err(|e| e.to_string())
        } else if let Some(svc) = &td.service_target {
            Err(format!(
                "run_local: service dispatch not available for {svc:?}; use run() with a live Control Plane"
            ))
        } else {
            Err(format!("state {:?}: no handler configured", td.name))
        };

        match result {
            Ok(out)  => return Ok(out),
            Err(e)   => last_err = e,
        }
    }

    Err(last_err)
}

// ---------------------------------------------------------------------------
// Control Plane HTTP helpers (stubs — full impl uses httpx / reqwest)
// ---------------------------------------------------------------------------

fn post_workflow(wf: &Workflow) {
    let _ = &wf.name; // used for URL construction (Phase 5 integration)
    // TODO(Phase 5 integration): serialise workflow and POST to
    // $KFLOW_CONTROL_PLANE_URL/api/v1/workflows/:name/run using reqwest.
    eprintln!("kflow: Control Plane HTTP submission not yet wired (set KFLOW_CONTROL_PLANE_URL)");
}

fn post_service(_svc: &ServiceDef) {
    // TODO: POST to /api/v1/services.
    eprintln!("kflow: Control Plane service registration not yet wired");
}

fn run_state(_wf: Workflow, state_name: &str) {
    // TODO(Phase 13): dial KFLOW_GRPC_ENDPOINT, GetInput(token), run handler,
    // CompleteState/FailState, std::process::exit(0/1).
    eprintln!(
        "kflow: --state={state_name} requires gRPC RunnerService (Phase 13 not yet implemented)"
    );
    std::process::exit(1);
}

fn run_service_worker(_svc: ServiceDef) {
    // TODO(Phase 13): Deployment → start tonic gRPC ServiceRunnerService server.
    //                 Lambda    → dial KFLOW_GRPC_ENDPOINT, GetInput, run, CompleteState/FailState.
    eprintln!("kflow: service worker mode requires gRPC RunnerService (Phase 13 not yet implemented)");
    std::process::exit(1);
}

fn flag(name: &str) -> Option<String> {
    let prefix = format!("--{name}=");
    let args: Vec<String> = std::env::args().collect();
    for (i, arg) in args.iter().enumerate() {
        if let Some(val) = arg.strip_prefix(&prefix) {
            return Some(val.to_string());
        }
        if arg == &format!("--{name}") {
            if let Some(val) = args.get(i + 1) {
                return Some(val.clone());
            }
        }
    }
    None
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::workflow::{step, Workflow, RetryPolicy};
    use crate::{Error, Input, Output};
    use std::sync::{Arc, Mutex};

    fn ok_handler(v: serde_json::Value) -> crate::workflow::HandlerFn {
        Box::new(move |mut inp: Input| {
            let v = v.clone();
            Box::pin(async move {
                inp.insert("result".to_string(), v);
                Ok(inp)
            })
        })
    }

    fn fail_handler(msg: &'static str) -> crate::workflow::HandlerFn {
        Box::new(move |_: Input| {
            Box::pin(async move { Err(Error::from(msg)) })
        })
    }

    fn counter_handler(counter: Arc<Mutex<u32>>, fail_until: u32) -> crate::workflow::HandlerFn {
        Box::new(move |inp: Input| {
            let counter = counter.clone();
            Box::pin(async move {
                let mut c = counter.lock().unwrap();
                *c += 1;
                if *c < fail_until {
                    Err(Error::from("transient"))
                } else {
                    Ok(inp)
                }
            })
        })
    }

    #[test]
    fn test_run_local_single_step() {
        let mut wf = Workflow::new("test");
        wf.task("A", Some(ok_handler(serde_json::json!(42))));
        wf.flow(vec![step("A").end()]);

        let output = run_local(wf, Input::new()).unwrap();
        assert_eq!(output["result"], serde_json::json!(42));
    }

    #[test]
    fn test_run_local_multi_step() {
        let mut wf = Workflow::new("test");
        wf.task("Double", Some(Box::new(|mut inp: Input| Box::pin(async move {
            let v = inp["v"].as_i64().unwrap();
            inp.insert("v".to_string(), serde_json::json!(v * 2));
            Ok(inp)
        }))));
        wf.task("AddOne", Some(Box::new(|mut inp: Input| Box::pin(async move {
            let v = inp["v"].as_i64().unwrap();
            inp.insert("v".to_string(), serde_json::json!(v + 1));
            Ok(inp)
        }))));
        wf.flow(vec![step("Double").next("AddOne"), step("AddOne").end()]);

        let mut input = Input::new();
        input.insert("v".to_string(), serde_json::json!(5));
        let output = run_local(wf, input).unwrap();
        assert_eq!(output["v"], serde_json::json!(11)); // 5*2=10, +1=11
    }

    #[test]
    fn test_run_local_retry() {
        let counter = Arc::new(Mutex::new(0u32));
        let mut wf = Workflow::new("test");
        wf.task("Flaky", Some(counter_handler(counter.clone(), 3)));
        wf.flow(vec![step("Flaky").retry(RetryPolicy { max_attempts: 3, backoff_seconds: 0 }).end()]);

        let result = run_local(wf, Input::new());
        assert!(result.is_ok());
        assert_eq!(*counter.lock().unwrap(), 3);
    }

    #[test]
    fn test_run_local_catch() {
        let mut wf = Workflow::new("test");
        wf.task("Risky", Some(fail_handler("boom")));
        wf.task("Handler", Some(Box::new(|inp: Input| Box::pin(async move { Ok(inp) }))));
        wf.flow(vec![
            step("Risky").catch("Handler").end(),
            step("Handler").end(),
        ]);

        let output = run_local(wf, Input::new()).unwrap();
        assert_eq!(output["_error"], serde_json::json!("boom"));
    }

    #[test]
    fn test_run_local_no_catch_returns_err() {
        let mut wf = Workflow::new("test");
        wf.task("Boom", Some(fail_handler("uncaught")));
        wf.flow(vec![step("Boom").end()]);

        let result = run_local(wf, Input::new());
        assert!(result.unwrap_err().contains("uncaught"));
    }

    #[test]
    fn test_run_local_choice() {
        let mut wf = Workflow::new("test");
        wf.choice("Route", Box::new(|inp: Input| Box::pin(async move {
            let amount = inp.get("amount").and_then(|v| v.as_i64()).unwrap_or(0);
            Ok(if amount > 1000 { "High".to_string() } else { "Low".to_string() })
        })));
        wf.task("High", Some(Box::new(|_: Input| Box::pin(async move {
            let mut out = Output::new();
            out.insert("path".to_string(), serde_json::json!("high"));
            Ok(out)
        }))));
        wf.task("Low", Some(Box::new(|_: Input| Box::pin(async move {
            let mut out = Output::new();
            out.insert("path".to_string(), serde_json::json!("low"));
            Ok(out)
        }))));
        wf.flow(vec![
            step("Route").next("High"),
            step("High").end(),
            step("Low").end(),
        ]);

        let mut input = Input::new();
        input.insert("amount".to_string(), serde_json::json!(2000));
        let out = run_local(wf, input).unwrap();
        assert_eq!(out["path"], serde_json::json!("high"));
    }

    #[test]
    fn test_run_local_invalid_workflow() {
        let wf = Workflow::new("test"); // no steps
        assert!(run_local(wf, Input::new()).is_err());
    }
}
