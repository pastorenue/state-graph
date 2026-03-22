use std::collections::HashMap;

use crate::workflow::{RetryPolicy, Workflow, FAIL, SUCCEED};
use crate::service::ServiceDef;
use crate::{Input, Output};

/// Entry point for workflow execution.
///
/// Dispatch priority:
/// 1. KFLOW_STATE_TOKEN set → worker mode (K8s Job container executing one state).
/// 2. --state=<name>        → same worker mode via CLI flag.
/// 3. (no flag)             → validate, serialise workflow, POST to Control Plane.
pub fn run(wf: Workflow, input: Input) {
    let token = std::env::var("KFLOW_STATE_TOKEN").unwrap_or_default();
    if !token.is_empty() {
        let state_name = flag("state").unwrap_or_else(|| {
            eprintln!("kflow: KFLOW_STATE_TOKEN set but --state=<name> missing");
            std::process::exit(1);
        });
        run_state(wf, &state_name, &token);
        return;
    }

    if let Some(state_name) = flag("state") {
        run_state(wf, &state_name, "");
        return;
    }
    if flag("service").is_some() {
        eprintln!("kflow: --service flag passed to run(); use run_service() for service dispatch");
        std::process::exit(1);
    }
    if let Err(e) = wf.validate() {
        panic!("kflow::run: invalid workflow: {e}");
    }
    post_workflow(&wf, &input);
}

/// Entry point for service registration and execution.
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
    let mut current   = input;
    let mut node_name = entry;

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
                    let choice = output
                        .get("__choice__")
                        .and_then(|v| v.as_str())
                        .unwrap_or(FAIL)
                        .to_string();
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
    if td.is_wait() {
        if let Some(dur) = td.wait_duration() {
            tokio::time::sleep(dur).await;
        }
        return Ok(Output::new());
    }

    let policy          = node.retry.as_ref();
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
            Ok(out) => return Ok(out),
            Err(e)  => last_err = e,
        }
    }

    Err(last_err)
}

// ---------------------------------------------------------------------------
// Worker mode: dial RunnerService, GetInput, call handler, report result
// ---------------------------------------------------------------------------

fn run_state(wf: Workflow, state_name: &str, token: &str) {
    use crate::proto::kflow::v1::{
        runner_service_client::RunnerServiceClient,
        CompleteStateRequest, FailStateRequest, GetInputRequest,
    };

    let endpoint = std::env::var("KFLOW_GRPC_ENDPOINT")
        .unwrap_or_else(|_| "http://kflow-cp.kflow.svc.cluster.local:9090".to_string());
    let token = token.to_string();

    let rt = tokio::runtime::Runtime::new().expect("tokio runtime");
    rt.block_on(async move {
        let mut client = match RunnerServiceClient::connect(endpoint).await {
            Ok(c)  => c,
            Err(e) => {
                eprintln!("kflow: connect to RunnerService: {e}");
                std::process::exit(1);
            }
        };

        let resp = match client.get_input(GetInputRequest { token: token.clone() }).await {
            Ok(r)  => r.into_inner(),
            Err(e) => {
                let _ = client.fail_state(FailStateRequest {
                    token: token.clone(),
                    error_message: e.to_string(),
                }).await;
                std::process::exit(1);
            }
        };

        let handler_input: Input = resp.payload
            .map(|s| {
                s.fields.into_iter()
                    .map(|(k, v)| (k, proto_value_to_json(v)))
                    .collect()
            })
            .unwrap_or_default();

        let td = match wf.tasks.get(state_name) {
            Some(t) => t,
            None => {
                let _ = client.fail_state(FailStateRequest {
                    token: token.clone(),
                    error_message: format!("unknown state: {state_name}"),
                }).await;
                std::process::exit(1);
            }
        };

        let handler = match td.handler.as_ref() {
            Some(h) => h,
            None => {
                let _ = client.fail_state(FailStateRequest {
                    token: token.clone(),
                    error_message: format!("state {state_name:?}: no inline handler"),
                }).await;
                std::process::exit(1);
            }
        };

        match handler(handler_input).await {
            Ok(output) => {
                let struct_val = to_proto_struct(&output);
                match client.complete_state(CompleteStateRequest {
                    token,
                    output: Some(struct_val),
                }).await {
                    Ok(_) => std::process::exit(0),
                    Err(e) => {
                        eprintln!("kflow: CompleteState: {e}");
                        std::process::exit(1);
                    }
                }
            }
            Err(e) => {
                let _ = client.fail_state(FailStateRequest {
                    token,
                    error_message: e.to_string(),
                }).await;
                std::process::exit(1);
            }
        }
    });
}

fn run_service_worker(_svc: ServiceDef) {
    eprintln!("kflow: service worker mode not yet implemented");
    std::process::exit(1);
}

// ---------------------------------------------------------------------------
// Control Plane HTTP submission
// ---------------------------------------------------------------------------

fn post_workflow(wf: &Workflow, input: &Input) {
    let rt = tokio::runtime::Runtime::new().expect("tokio runtime");
    rt.block_on(async move {
        let endpoint = std::env::var("KFLOW_SERVER")
            .unwrap_or_else(|_| "http://localhost:8080".to_string());
        let api_key = std::env::var("KFLOW_API_KEY").unwrap_or_default();
        let client = reqwest::Client::new();
        let mut builder = client.post(format!("{endpoint}/api/v1/workflows"))
            .json(&serialise_graph(wf));
        if !api_key.is_empty() {
            builder = builder.header("Authorization", format!("Bearer {api_key}"));
        }
        let reg = builder.send().await.expect("register workflow");
        let status = reg.status().as_u16();
        if ![200u16, 201, 409].contains(&status) {
            let body = reg.text().await.unwrap_or_default();
            eprintln!("kflow: register failed {status}: {body}");
            std::process::exit(1);
        }

        let mut run_builder = client
            .post(format!("{endpoint}/api/v1/workflows/{}/run", wf.name))
            .json(&serde_json::json!({"input": input}));
        if !api_key.is_empty() {
            run_builder = run_builder.header("Authorization", format!("Bearer {api_key}"));
        }
        let run = run_builder.send().await.expect("trigger workflow");
        let run_status = run.status().as_u16();
        if ![200u16, 201, 202].contains(&run_status) {
            let body = run.text().await.unwrap_or_default();
            eprintln!("kflow: run failed {run_status}: {body}");
            std::process::exit(1);
        }
    });
}

fn post_service(_svc: &ServiceDef) {
    eprintln!("kflow: Control Plane service registration not yet wired");
}

fn serialise_graph(wf: &Workflow) -> serde_json::Value {
    let steps: Vec<_> = wf.steps.iter().map(|sb| {
        let td    = wf.tasks.get(&sb.name);
        let retry = sb.retry.clone().or_else(|| td.and_then(|t| t.retry.clone()));
        let mut step = serde_json::json!({
            "name":    sb.name,
            "next":    sb.next.clone().unwrap_or_default(),
            "catch":   sb.catch.clone()
                .or_else(|| td.and_then(|t| t.catch.clone()))
                .unwrap_or_default(),
            "is_end":  sb.next.as_deref() == Some(SUCCEED),
        });
        if let Some(r) = retry {
            step["retry"] = serde_json::json!({
                "max_attempts": r.max_attempts,
                "backoff_seconds": r.backoff_seconds,
            });
        }
        step
    }).collect();

    let mut graph = serde_json::json!({ "name": wf.name, "steps": steps });
    if !wf.image.is_empty() {
        graph["image"] = serde_json::json!(&wf.image);
    }
    serde_json::json!({ "graph": graph })
}

// ---------------------------------------------------------------------------
// Proto value conversion helpers
// ---------------------------------------------------------------------------

fn proto_value_to_json(v: prost_types::Value) -> serde_json::Value {
    use prost_types::value::Kind;
    match v.kind {
        Some(Kind::NullValue(_))   => serde_json::Value::Null,
        Some(Kind::BoolValue(b))   => serde_json::Value::Bool(b),
        Some(Kind::NumberValue(n)) => serde_json::json!(n),
        Some(Kind::StringValue(s)) => serde_json::Value::String(s),
        Some(Kind::ListValue(l))   => {
            serde_json::Value::Array(l.values.into_iter().map(proto_value_to_json).collect())
        }
        Some(Kind::StructValue(s)) => {
            let map: serde_json::Map<_, _> = s.fields.into_iter()
                .map(|(k, v)| (k, proto_value_to_json(v)))
                .collect();
            serde_json::Value::Object(map)
        }
        None => serde_json::Value::Null,
    }
}

fn json_to_proto_value(v: serde_json::Value) -> prost_types::Value {
    use prost_types::value::Kind;
    let kind = match v {
        serde_json::Value::Null      => Kind::NullValue(0),
        serde_json::Value::Bool(b)   => Kind::BoolValue(b),
        serde_json::Value::Number(n) => Kind::NumberValue(n.as_f64().unwrap_or(0.0)),
        serde_json::Value::String(s) => Kind::StringValue(s),
        serde_json::Value::Array(a)  => Kind::ListValue(prost_types::ListValue {
            values: a.into_iter().map(json_to_proto_value).collect(),
        }),
        serde_json::Value::Object(o) => Kind::StructValue(prost_types::Struct {
            fields: o.into_iter().map(|(k, v)| (k, json_to_proto_value(v))).collect(),
        }),
    };
    prost_types::Value { kind: Some(kind) }
}

fn to_proto_struct(output: &Output) -> prost_types::Struct {
    prost_types::Struct {
        fields: output.iter()
            .map(|(k, v)| (k.clone(), json_to_proto_value(v.clone())))
            .collect(),
    }
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
        assert_eq!(output["v"], serde_json::json!(11));
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
        let wf = Workflow::new("test");
        assert!(run_local(wf, Input::new()).is_err());
    }

    #[test]
    fn test_serialise_graph_includes_image() {
        let mut wf = Workflow::new("test").with_image("kflow-example:dev");
        wf.task("A", Some(ok_handler(serde_json::json!(1))));
        wf.flow(vec![step("A").end()]);
        let json = serialise_graph(&wf);
        assert_eq!(json["graph"]["image"], "kflow-example:dev");
    }

    #[test]
    fn test_serialise_graph_no_image_field_when_empty() {
        let mut wf = Workflow::new("test");
        wf.task("A", Some(ok_handler(serde_json::json!(1))));
        wf.flow(vec![step("A").end()]);
        let json = serialise_graph(&wf);
        assert!(json["graph"]["image"].is_null());
    }
}
