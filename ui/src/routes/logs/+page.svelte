<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import type { LogLine } from '$lib/api';
  import type { LogEntryPayload } from '$lib/ws';

  type Filters = {
    level?: string;
    executionId?: string;
    serviceName?: string;
    stateName?: string;
    since?: string;
    until?: string;
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

  const limit = 50;
  let offset = $state(0);

  let rawQuery = $state('');
  let pills: Pill[] = $state([]);

  function getToken(): string | null {
    if (typeof localStorage === 'undefined') return null;
    return localStorage.getItem('kflow_token');
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
        if (k === 'since') {
          filters.since = v;
          parsed.push({ key: 'since', value: v, raw: tok });
          continue;
        }
        if (k === 'until') {
          filters.until = v;
          parsed.push({ key: 'until', value: v, raw: tok });
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
    if (rawQuery.trim()) {
      search();
    }
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

  function search(resetOffset = true) {
    const { filters: f, pills: p } = parseQuery(rawQuery);
    pills = p;
    if (resetOffset) {
      offset = 0;
      logs = [];
      total = 0;
    }
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

  function openWSLogs(f: Filters) {
    const params = new URLSearchParams();
    if (f.executionId) params.set('execution_id', f.executionId);
    if (f.serviceName) params.set('service_name', f.serviceName);
    if (f.stateName) params.set('state_name', f.stateName);
    if (f.level) params.set('level', f.level);
    if (f.since) params.set('since', f.since);
    if (f.until) params.set('until', f.until);
    if (f.q) params.set('q', f.q);
    params.set('limit', String(limit));
    params.set('offset', String(offset));
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
          logs = [
            ...logs,
            {
              log_id: p.log_id,
              execution_id: p.execution_id,
              service_name: p.service_name,
              state_name: p.state_name,
              level: p.level,
              message: p.message,
              occurred_at: p.occurred_at,
            },
          ];
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

  function nextPage() {
    offset += limit;
    search(false);
  }

  function prevPage() {
    offset = Math.max(0, offset - limit);
    search(false);
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
</script>

<div class="p-8 max-w-6xl">
<h1 class="text-3xl font-medium">Log Explorer</h1>

<form class="flex gap-2 mb-2" onsubmit={(e) => { e.preventDefault(); search(); }}>
  <input
    class="flex-1"
    bind:value={rawQuery}
    placeholder="Filter logs… e.g. level:ERROR execution:abc service:api state:Validate"
  />
  <button type="submit">Search</button>
</form>

{#if pills.length > 0}
  <div class="flex flex-wrap gap-1 mb-4">
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
  <div class="mb-4"></div>
{/if}

{#if wsConnected}
  <span class="text-xs text-green-600 font-medium">● Live</span>
{:else if historyLoaded}
  <span class="text-xs text-muted">Disconnected</span>
{/if}

{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty text-red-600">{error}</p>
{:else}
  {#if logs.length > 0 || historyLoaded}
    <div class="text-xs text-muted mb-2">
      Showing {offset + 1}–{offset + logs.length} results
      {#if historyLoaded && !wsConnected}(history){/if}
    </div>
  {/if}

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
          <td colspan="5" class="empty border-none">No logs found for the selected filters.</td>
        </tr>
      {:else if logs.length === 0}
        <tr class="hover:bg-transparent cursor-default">
          <td colspan="5" class="empty border-none">Run a search to view logs.</td>
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

  {#if historyLoaded}
    <div class="flex gap-2 mt-4">
      <button onclick={prevPage} disabled={offset === 0} class="disabled:opacity-40 disabled:cursor-not-allowed">← Prev</button>
      <button onclick={nextPage}>Next →</button>
    </div>
  {/if}
{/if}
</div>
