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

// ---- API functions ----

export async function listExecutions(params?: {
  workflow?: string;
  status?: string;
  limit?: number;
  offset?: number;
}): Promise<Execution[]> {
  return request<Execution[]>(`/api/v1/executions${qs(params ?? {})}`);
}

export async function getExecution(id: string): Promise<Execution> {
  return request<Execution>(`/api/v1/executions/${encodeURIComponent(id)}`);
}

export async function listExecutionStates(id: string): Promise<StateRecord[]> {
  return request<StateRecord[]>(`/api/v1/executions/${encodeURIComponent(id)}/states`);
}

export async function listServices(): Promise<Service[]> {
  return request<Service[]>('/api/v1/services');
}

export async function getService(name: string): Promise<Service> {
  return request<Service>(`/api/v1/services/${encodeURIComponent(name)}`);
}

export async function getExecutionEvents(
  id: string,
  params?: { since?: string; limit?: number },
): Promise<ExecutionEvent[]> {
  const resp = await request<{ events?: ExecutionEvent[] }>(
    `/api/v1/executions/${encodeURIComponent(id)}/events${qs(params ?? {})}`,
  );
  return resp.events ?? [];
}

export async function getServiceMetrics(
  name: string,
  params?: { since?: string; until?: string; limit?: number },
): Promise<ServiceMetric[]> {
  return request<ServiceMetric[]>(
    `/api/v1/services/${encodeURIComponent(name)}/metrics${qs(params ?? {})}`,
  );
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
  return request<{ logs: LogLine[]; total: number }>(`/api/v1/logs${qs(params)}`);
}
