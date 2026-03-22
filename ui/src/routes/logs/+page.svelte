<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import type { LogLine } from '$lib/api';
  import type { LogEntryPayload } from '$lib/ws';

  type TimeRange = 'live' | '15m' | '1h' | '4h' | '24h' | 'custom';

  type Filters = {
    level?: string;
    executionId?: string;
    serviceName?: string;
    stateName?: string;
    q?: string;
  };

  type Pill = {
    key: string;
    value: string;
    raw: string;
  };

  let logs: LogLine[] = $state([]);
  let total = $state(0);
  let loading = $state(false);
  let error = $state('');
  let historyLoaded = $state(false);
  let wsConnected = $state(false);
  let ws: WebSocket | null = null;

  let timeRange: TimeRange = $state('live');
  let customSince = $state('');
  let customUntil = $state('');

  const limit = 200;

  let rawQuery = $state('');
  let pills: Pill[] = $state([]);

  function getToken(): string | null {
    if (typeof localStorage === 'undefined') return null;
    return localStorage.getItem('kflow_token');
  }

  function computeSince(): string | undefined {
    const offsets: Record<string, number> = {
      '15m': 15 * 60_000,
      '1h': 3_600_000,
      '4h': 14_400_000,
      '24h': 86_400_000,
    };
    if (timeRange === 'custom') return customSince || undefined;
    const ms = offsets[timeRange];
    return ms ? new Date(Date.now() - ms).toISOString() : undefined;
  }

  function computeUntil(): string | undefined {
    return timeRange === 'custom' ? customUntil || undefined : undefined;
  }

  function parseQuery(raw: string): { filters: Filters; pills: Pill[] } {
    const filters: Filters = {};
    const freeText: string[] = [];
    const parsed: Pill[] = [];
    const tokens = raw.match(/\w+:[^\s]+|"[^"]*"|\S+/g) ?? [];
    for (const tok of tokens) {
      const colon = tok.indexOf(':');
      if (colon > 0) {
        const k = tok.slice(0, colon).toLowerCase();
        const v = tok.slice(colon + 1);
        if (k === 'level') {
          filters.level = v.toUpperCase();
          parsed.push({ key: 'level', value: v.toUpperCase(), raw: tok });
          continue;
        }
        if (k === 'execution' || k === 'execution_id') {
          filters.executionId = v;
          parsed.push({ key: 'execution', value: v, raw: tok });
          continue;
        }
        if (k === 'service' || k === 'service_name') {
          filters.serviceName = v;
          parsed.push({ key: 'service', value: v, raw: tok });
          continue;
        }
        if (k === 'state' || k === 'state_name') {
          filters.stateName = v;
          parsed.push({ key: 'state', value: v, raw: tok });
          continue;
        }
      }
      const text = tok.replace(/^"|"$/g, '');
      freeText.push(text);
      parsed.push({ key: '', value: text, raw: tok });
    }
    if (freeText.length > 0) filters.q = freeText.join(' ');
    return { filters, pills: parsed };
  }

  onMount(() => {
    const sp = $page.url.searchParams;
    const parts: string[] = [];
    if (sp.get('execution_id')) parts.push(`execution:${sp.get('execution_id')}`);
    if (sp.get('service_name')) parts.push(`service:${sp.get('service_name')}`);
    if (sp.get('state_name')) parts.push(`state:${sp.get('state_name')}`);
    if (sp.get('level')) parts.push(`level:${sp.get('level')}`);
    if (sp.get('q')) parts.push(sp.get('q')!);
    rawQuery = parts.join(' ');
    search();
  });

  onDestroy(() => {
    closeWS();
  });

  function closeWS() {
    if (ws) {
      ws.close();
      ws = null;
      wsConnected = false;
    }
  }

  function search() {
    const { filters: f, pills: p } = parseQuery(rawQuery);
    pills = p;
    logs = [];
    total = 0;
    closeWS();
    historyLoaded = false;
    loading = true;
    error = '';
    openWSLogs(f);
  }

  function removePill(pill: Pill) {
    rawQuery = rawQuery.replace(pill.raw, '').replace(/\s+/g, ' ').trim();
    search();
  }

  function selectTimeRange(tr: TimeRange) {
    timeRange = tr;
    if (tr !== 'custom') search();
  }

  function applyCustomRange() {
    search();
  }

  function openWSLogs(f: Filters) {
    const params = new URLSearchParams();
    if (f.executionId) params.set('execution_id', f.executionId);
    if (f.serviceName) params.set('service_name', f.serviceName);
    if (f.stateName) params.set('state_name', f.stateName);
    if (f.level) params.set('level', f.level);
    if (f.q) params.set('q', f.q);

    const since = computeSince();
    const until = computeUntil();
    if (since) params.set('since', since);
    if (until) params.set('until', until);

    params.set('limit', String(limit));
    const token = getToken();
    if (token) params.set('token', token);

    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = `${protocol}//${location.host}/api/v1/ws/logs?${params.toString()}`;

    ws = new WebSocket(url);

    ws.onopen = () => {
      wsConnected = true;
    };

    ws.onmessage = (ev) => {
      try {
        const event = JSON.parse(ev.data as string);
        if (event.type === 'log_entry') {
          const p = event.payload as LogEntryPayload;
          const entry: LogLine = {
            log_id: p.log_id,
            execution_id: p.execution_id,
            service_name: p.service_name,
            state_name: p.state_name,
            level: p.level,
            message: p.message,
            occurred_at: p.occurred_at,
          };
          if (logs.length >= 500) {
            logs = [...logs.slice(-499), entry];
          } else {
            logs = [...logs, entry];
          }
          total = logs.length;
        } else if (event.type === 'logs_end') {
          historyLoaded = true;
          loading = false;
        }
      } catch {
        // ignore malformed messages
      }
    };

    ws.onerror = () => {
      error = 'WebSocket connection error';
      loading = false;
    };

    ws.onclose = () => {
      wsConnected = false;
      if (!historyLoaded) {
        loading = false;
      }
    };
  }

  function levelBadge(level: string): string {
    return `badge badge-${level.toLowerCase()}`;
  }

  function pillClass(pill: Pill): string {
    if (pill.key === 'level') return `badge badge-${pill.value.toLowerCase()} gap-1 cursor-default`;
    return 'badge gap-1 bg-raised text-muted cursor-default';
  }

  function pillLabel(pill: Pill): string {
    if (pill.key === '') return pill.value;
    return `${pill.key}: ${pill.value}`;
  }

  const timeRangeLabels: Record<TimeRange, string> = {
    live: '● Live',
    '15m': '15m',
    '1h': '1h',
    '4h': '4h',
    '24h': '24h',
    custom: 'Custom',
  };
</script>

<div class="p-8 max-w-6xl">
<h1 class="text-3xl font-medium">Log Explorer</h1>

<form class="flex gap-2 mb-2" onsubmit={(e) => { e.preventDefault(); search(); }}>
  <input
    class="flex-1"
    bind:value={rawQuery}
    placeholder="Filter logs… e.g. level:ERROR execution:abc service:api state:Validate"
  />
  <button type="submit">Apply</button>
</form>

{#if pills.length > 0}
  <div class="flex flex-wrap gap-1 mb-3">
    {#each pills as pill (pill.raw + pill.key)}
      <span class={pillClass(pill)}>
        {pillLabel(pill)}
        <button
          type="button"
          class="ml-1 opacity-50 hover:opacity-100 font-bold leading-none"
          onclick={() => removePill(pill)}
          aria-label="Remove filter"
        >×</button>
      </span>
    {/each}
  </div>
{:else}
  <div class="mb-3"></div>
{/if}

<div class="flex flex-wrap items-center gap-1 mb-4">
  {#each (['live', '15m', '1h', '4h', '24h', 'custom'] as TimeRange[]) as tr}
    <button
      type="button"
      class={timeRange === tr ? 'ring-1 ring-accent text-accent bg-surface' : ''}
      onclick={() => selectTimeRange(tr)}
    >{timeRangeLabels[tr]}</button>
  {/each}
</div>

{#if timeRange === 'custom'}
  <div class="flex flex-wrap items-center gap-2 mb-4">
    <label class="text-sm text-muted" for="custom-since">From</label>
    <input id="custom-since" type="datetime-local" bind:value={customSince} class="text-sm" />
    <label class="text-sm text-muted" for="custom-until">To</label>
    <input id="custom-until" type="datetime-local" bind:value={customUntil} class="text-sm" />
    <button type="button" onclick={applyCustomRange}>Apply</button>
  </div>
{/if}

<div class="flex items-center gap-3 mb-2 min-h-[1.5rem]">
  {#if wsConnected}
    <span class="text-xs text-emerald-700 font-medium flex items-center gap-1.5">
      <span class="relative flex h-2 w-2">
        <span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-500 opacity-75"></span>
        <span class="relative inline-flex rounded-full h-2 w-2 bg-emerald-600"></span>
      </span>
      Live · {logs.length} logs
    </span>
  {:else if historyLoaded}
    <span class="text-xs text-muted">{logs.length} logs · Disconnected</span>
  {:else if loading}
    <span class="text-xs text-muted">Connecting…</span>
  {/if}

  {#if error}
    <span class="text-xs text-red-600">{error}</span>
  {/if}
</div>

<table>
  <thead>
    <tr>
      <th>Time</th>
      <th>Level</th>
      <th>Source</th>
      <th>State</th>
      <th>Message</th>
    </tr>
  </thead>
  <tbody>
    {#if logs.length === 0 && historyLoaded}
      <tr class="hover:bg-transparent cursor-default">
        <td colspan="5" class="empty border-none">No logs match the current filters.</td>
      </tr>
    {:else if logs.length === 0}
      <tr class="hover:bg-transparent cursor-default">
        <td colspan="5" class="empty border-none">Waiting for logs…</td>
      </tr>
    {:else}
      {#each logs as log (log.log_id)}
        <tr>
          <td class="text-xs whitespace-nowrap">{new Date(log.occurred_at).toLocaleString()}</td>
          <td><span class={levelBadge(log.level)}>{log.level}</span></td>
          <td class="text-xs">
            {#if log.execution_id}
              <code>{log.execution_id.slice(0, 8)}…</code>
            {:else if log.service_name}
              {log.service_name}
            {/if}
          </td>
          <td class="text-xs">{log.state_name || '—'}</td>
          <td class="text-sm">{log.message}</td>
        </tr>
      {/each}
    {/if}
  </tbody>
</table>
</div>
