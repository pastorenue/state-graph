#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${KFLOW_BASE_URL:-http://localhost:8080}"
API_KEY="${KFLOW_API_KEY:-}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

FAILED=0

log()  { echo -e "${CYAN}[demo]${RESET} $*"; }
step() { echo -e "\n${BOLD}$*${RESET}"; }
ok()   { echo -e "  ${GREEN}✓${RESET} $*"; }
fail() { echo -e "  ${RED}✗${RESET} $*"; FAILED=1; }
warn() { echo -e "  ${YELLOW}!${RESET} $*"; }

api_post() {
  local path="$1"
  local body="$2"
  local args=(-s -X POST -H "Content-Type: application/json" -d "$body")
  [[ -n "$API_KEY" ]] && args+=(-H "Authorization: Bearer $API_KEY")
  curl "${args[@]}" "${BASE_URL}${path}"
}

api_get() {
  local path="$1"
  local args=(-s -X GET)
  [[ -n "$API_KEY" ]] && args+=(-H "Authorization: Bearer $API_KEY")
  curl "${args[@]}" "${BASE_URL}${path}"
}

extract_json() {
  local key="$1"
  local json="$2"
  python3 -c "
import sys, json
try:
    d = json.loads(sys.argv[1])
    keys = sys.argv[2].split('.')
    v = d
    for k in keys:
        v = v[k]
    print(v)
except Exception:
    print('')
" "$json" "$key"
}

poll_execution() {
  local exec_id="$1"
  local attempts=0
  local max=30

  while [[ $attempts -lt $max ]]; do
    local resp
    resp=$(api_get "/api/v1/executions/${exec_id}")
    local status
    status=$(extract_json "execution.status" "$resp")

    case "$status" in
      STATUS_COMPLETED)
        ok "execution ${exec_id} → COMPLETED"
        return 0
        ;;
      STATUS_FAILED)
        local err
        err=$(extract_json "execution.error" "$resp")
        fail "execution ${exec_id} → FAILED: ${err}"
        return 1
        ;;
      *)
        sleep 1
        ((attempts++))
        ;;
    esac
  done

  fail "poll timeout after ${max}s for execution ${exec_id}"
  return 1
}

show_states() {
  local exec_id="$1"
  local resp
  resp=$(api_get "/api/v1/executions/${exec_id}/states")
  python3 -c "
import sys, json
try:
    d = json.loads(sys.argv[1])
    states = d.get('states', [])
    for s in states:
        name   = s.get('stateName', '?')
        status = s.get('status', '?').replace('STATUS_', '')
        attempt = s.get('attempt', 0)
        print(f'    {name}  [{status}]  attempt={attempt}')
except Exception as e:
    print(f'    (could not parse states: {e})')
" "$resp"
}

show_events() {
  local exec_id="$1"
  local resp
  resp=$(api_get "/api/v1/executions/${exec_id}/events") || { warn "telemetry unavailable"; return; }
  python3 -c "
import sys, json
try:
    d = json.loads(sys.argv[1])
    events = d.get('events', [])
    if not events:
        print('    (no events recorded)')
        sys.exit(0)
    for e in events:
        kind  = e.get('kind', '?')
        state = e.get('state_name', '')
        ts    = e.get('timestamp', '')
        label = f'{state}: ' if state else ''
        print(f'    {ts}  {label}{kind}')
except Exception:
    print('    (telemetry unavailable)')
" "$resp" || warn "telemetry unavailable"
}

health_check() {
  step "Health check"
  local resp
  resp=$(curl -sf "${BASE_URL}/healthz" 2>/dev/null) || {
    fail "orchestrator not reachable at ${BASE_URL} — run 'make up' first"
    exit 1
  }
  ok "orchestrator is up (${BASE_URL})"
}

run_single_task() {
  step "Workflow 1: single-task"

  local graph
  graph='{
    "graph": {
      "name": "single-task",
      "states": [
        {"name": "ProcessData", "kind": "task"}
      ],
      "steps": [
        {"name": "ProcessData", "is_end": true}
      ]
    }
  }'

  local reg_resp
  reg_resp=$(api_post "/api/v1/workflows" "$graph")
  local reg_err
  reg_err=$(extract_json "error" "$reg_resp")
  if [[ -n "$reg_err" && "$reg_err" != "None" && "$reg_err" != "null" && "$reg_err" != "" ]]; then
    warn "register: ${reg_err} (may already exist)"
  else
    ok "registered single-task"
  fi

  local input='{"user":"alice","action":"purchase","amount":99.99}'
  local run_resp
  run_resp=$(api_post "/api/v1/workflows/single-task/run" "$input")
  local exec_id
  exec_id=$(extract_json "executionId" "$run_resp")

  if [[ -z "$exec_id" || "$exec_id" == "None" ]]; then
    fail "could not start single-task: $(echo "$run_resp" | head -c 200)"
    return
  fi
  ok "started execution ${exec_id}"

  poll_execution "$exec_id" || return
  log "states:"
  show_states "$exec_id"
}

run_graph_flow() {
  step "Workflow 2: graph-flow"

  local graph
  graph='{
    "graph": {
      "name": "graph-flow",
      "states": [
        {"name": "ValidateInput", "kind": "task"},
        {"name": "EnrichData",    "kind": "task"},
        {"name": "NotifyUser",    "kind": "task"},
        {"name": "HandleError",   "kind": "task"}
      ],
      "steps": [
        {"name": "ValidateInput", "next": "EnrichData",  "catch": "HandleError"},
        {"name": "EnrichData",    "next": "NotifyUser",  "catch": "HandleError"},
        {"name": "NotifyUser",    "is_end": true},
        {"name": "HandleError",   "is_end": true}
      ]
    }
  }'

  local reg_resp
  reg_resp=$(api_post "/api/v1/workflows" "$graph")
  local reg_err
  reg_err=$(extract_json "error" "$reg_resp")
  if [[ -n "$reg_err" && "$reg_err" != "None" && "$reg_err" != "null" && "$reg_err" != "" ]]; then
    warn "register: ${reg_err} (may already exist)"
  else
    ok "registered graph-flow"
  fi

  local input='{"order_id":"ORD-42","customer":"bob","items":3,"total":249.50}'
  local run_resp
  run_resp=$(api_post "/api/v1/workflows/graph-flow/run" "$input")
  local exec_id
  exec_id=$(extract_json "executionId" "$run_resp")

  if [[ -z "$exec_id" || "$exec_id" == "None" ]]; then
    fail "could not start graph-flow: $(echo "$run_resp" | head -c 200)"
    return
  fi
  ok "started execution ${exec_id}"

  poll_execution "$exec_id" || return
  log "states:"
  show_states "$exec_id"
  log "telemetry events:"
  show_events "$exec_id"
}

main() {
  echo -e "\n${BOLD}kflow demo${RESET} — end-to-end smoke test\n"

  health_check
  run_single_task
  run_graph_flow

  step "Summary"
  if [[ $FAILED -eq 0 ]]; then
    ok "all workflows completed successfully"
  else
    fail "one or more workflows did not complete"
  fi
  echo -e "\n  Dashboard: ${CYAN}http://localhost:5173${RESET}\n"

  exit $FAILED
}

main
