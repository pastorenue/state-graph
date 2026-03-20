const BASE = import.meta.env.VITE_API_BASE ?? '';

function getToken(): string | null {
  if (typeof localStorage === 'undefined') return null;
  return localStorage.getItem('kflow_token');
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const token = getToken();
  const headers: Record<string, string> = { ...(init.headers as Record<string, string>) };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const res = await fetch(`${BASE}${path}`, { ...init, headers });

  if (res.status === 401) {
    if (typeof localStorage !== 'undefined') {
      localStorage.removeItem('kflow_token');
    }
    if (typeof window !== 'undefined') {
      window.location.href = '/login';
    }
    throw { error: 'unauthorized', code: 'auth_required' };
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText, code: String(res.status) }));
    throw body as { error: string; code: string };
  }
  return res.json() as Promise<T>;
}

export async function getAuthStatus(): Promise<{ auth_enabled: boolean }> {
  const res = await fetch(`${BASE}/api/v1/auth/status`);
  if (!res.ok) return { auth_enabled: false };
  return res.json() as Promise<{ auth_enabled: boolean }>;
}

export async function login(apiKey: string): Promise<string> {
  const res = await fetch(`${BASE}/api/v1/auth/token`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ api_key: apiKey }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText, code: String(res.status) }));
    throw body as { error: string; code: string };
  }
  const data = (await res.json()) as { token: string };
  return data.token;
}

function qs(params: Record<string, string | number | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : '';
}

// ---- Types ----

export interface Execution {
  id: string;
  workflow: string;
  status: 'Pending' | 'Running' | 'Completed' | 'Failed';
  input: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface StateRecord {
  execution_id: string;
  state_name: string;
  status: 'Pending' | 'Running' | 'Completed' | 'Failed';
  input: Record<string, unknown>;
  output: Record<string, unknown>;
  error: string;
  attempt: number;
  created_at: string;
  updated_at: string;
}

export interface Service {
  name: string;
  mode: 'Deployment' | 'Lambda';
  port: number;
  min_scale: number;
  max_scale: number;
  ingress_host: string;
  timeout_seconds: number;
  status: 'Pending' | 'Running' | 'Failed' | 'Stopped';
  cluster_ip: string;
  created_at: string;
  updated_at: string;
}

export interface ExecutionEvent {
  event_id: string;
  execution_id: string;
  state_name: string;
  from_status: string;
  to_status: string;
  error: string;
  occurred_at: string;
}

export interface ServiceMetric {
  metric_id: string;
  service_name: string;
  invocation_id: string;
  duration_ms: number;
  status_code: number;
  error: string;
  occurred_at: string;
}

export interface LogLine {
  log_id: string;
  execution_id: string;
  service_name: string;
  state_name: string;
  level: 'INFO' | 'WARN' | 'ERROR' | 'DEBUG';
  message: string;
  occurred_at: string;
}

// ---- Proto JSON normalization ----
// grpc-gateway serialises proto fields as camelCase and enums as "ENUM_VALUE" strings.
// These helpers translate to the snake_case / display-string format the templates use.

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function protoStatus(s: string): Execution['status'] {
  const map: Record<string, Execution['status']> = {
    STATUS_PENDING: 'Pending',
    STATUS_RUNNING: 'Running',
    STATUS_COMPLETED: 'Completed',
    STATUS_FAILED: 'Failed',
  };
  return map[s] ?? (s as Execution['status']);
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function protoServiceStatus(s: string): Service['status'] {
  const map: Record<string, Service['status']> = {
    SERVICE_STATUS_PENDING: 'Pending',
    SERVICE_STATUS_RUNNING: 'Running',
    SERVICE_STATUS_FAILED: 'Failed',
    SERVICE_STATUS_STOPPED: 'Stopped',
  };
  return map[s] ?? (s as Service['status']);
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function toExecution(r: any): Execution {
  return {
    id: r.id ?? '',
    workflow: r.workflow ?? '',
    status: protoStatus(r.status),
    input: r.input ?? {},
    created_at: r.createdAt ?? r.created_at ?? '',
    updated_at: r.updatedAt ?? r.updated_at ?? '',
  };
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function toStateRecord(r: any): StateRecord {
  return {
    execution_id: r.executionId ?? r.execution_id ?? '',
    state_name: r.stateName ?? r.state_name ?? '',
    status: protoStatus(r.status),
    input: r.input ?? {},
    output: r.output ?? {},
    error: r.error ?? '',
    attempt: r.attempt ?? 1,
    created_at: r.createdAt ?? r.created_at ?? '',
    updated_at: r.updatedAt ?? r.updated_at ?? '',
  };
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function toService(r: any): Service {
  return {
    name: r.name ?? '',
    mode: r.mode === 'lambda' ? 'Lambda' : 'Deployment',
    port: r.port ?? 0,
    min_scale: r.minScale ?? r.min_scale ?? 0,
    max_scale: r.maxScale ?? r.max_scale ?? 0,
    ingress_host: r.ingressHost ?? r.ingress_host ?? '',
    timeout_seconds: r.timeoutSeconds ?? r.timeout_seconds ?? 0,
    status: protoServiceStatus(r.status),
    cluster_ip: r.clusterIp ?? r.cluster_ip ?? '',
    created_at: r.createdAt ?? r.created_at ?? '',
    updated_at: r.updatedAt ?? r.updated_at ?? '',
  };
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function toExecutionEvent(r: any): ExecutionEvent {
  return {
    event_id: r.eventId ?? r.event_id ?? '',
    execution_id: r.executionId ?? r.execution_id ?? '',
    state_name: r.stateName ?? r.state_name ?? '',
    from_status: r.fromStatus ?? r.from_status ?? '',
    to_status: r.toStatus ?? r.to_status ?? '',
    error: r.error ?? '',
    occurred_at: r.occurredAt ?? r.occurred_at ?? '',
  };
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function toLogLine(r: any): LogLine {
  return {
    log_id: r.logId ?? r.log_id ?? '',
    execution_id: r.executionId ?? r.execution_id ?? '',
    service_name: r.serviceName ?? r.service_name ?? '',
    state_name: r.stateName ?? r.state_name ?? '',
    level: r.level ?? 'INFO',
    message: r.message ?? '',
    occurred_at: r.occurredAt ?? r.occurred_at ?? '',
  };
}

// ---- API functions ----

export async function listExecutions(params?: {
  workflow?: string;
  status?: string;
  limit?: number;
  offset?: number;
}): Promise<Execution[]> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ executions?: any[] }>(`/api/v1/executions${qs(params ?? {})}`);
  return (resp.executions ?? []).map(toExecution);
}

export async function getExecution(id: string): Promise<Execution> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ execution?: any }>(`/api/v1/executions/${encodeURIComponent(id)}`);
  return toExecution(resp.execution ?? resp);
}

export async function listExecutionStates(id: string): Promise<StateRecord[]> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ states?: any[] }>(`/api/v1/executions/${encodeURIComponent(id)}/states`);
  return (resp.states ?? []).map(toStateRecord);
}

export async function listServices(): Promise<Service[]> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ services?: any[] }>('/api/v1/services');
  return (resp.services ?? []).map(toService);
}

export async function getService(name: string): Promise<Service> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ service?: any }>(`/api/v1/services/${encodeURIComponent(name)}`);
  return toService(resp.service ?? resp);
}

export async function getExecutionEvents(
  id: string,
  params?: { since?: string; limit?: number },
): Promise<ExecutionEvent[]> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ events?: any[] }>(
    `/api/v1/executions/${encodeURIComponent(id)}/events${qs(params ?? {})}`,
  );
  return (resp.events ?? []).map(toExecutionEvent);
}

export async function getServiceMetrics(
  name: string,
  params?: { since?: string; until?: string; limit?: number },
): Promise<ServiceMetric[]> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ metrics?: any[] }>(
    `/api/v1/services/${encodeURIComponent(name)}/metrics${qs(params ?? {})}`,
  );
  return (resp.metrics ?? []).map((r: any) => ({
    metric_id: r.metricId ?? r.metric_id ?? '',
    service_name: r.serviceName ?? r.service_name ?? '',
    invocation_id: r.invocationId ?? r.invocation_id ?? '',
    duration_ms: r.durationMs ?? r.duration_ms ?? 0,
    status_code: r.statusCode ?? r.status_code ?? 0,
    error: r.error ?? '',
    occurred_at: r.occurredAt ?? r.occurred_at ?? '',
  }));
}

export async function queryLogs(params: {
  execution_id?: string;
  service_name?: string;
  state_name?: string;
  level?: string;
  since?: string;
  until?: string;
  q?: string;
  limit?: number;
  offset?: number;
}): Promise<{ logs: LogLine[]; total: number }> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const resp = await request<{ logs?: any[]; total?: number }>(`/api/v1/logs${qs(params)}`);
  return { logs: (resp.logs ?? []).map(toLogLine), total: resp.total ?? 0 };
}
