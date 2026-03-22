<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import type { LogLine } from '$lib/api';
  import type { LogEntryPayload } from '$lib/ws';

  let logs: LogLine[] = $state([]);
  let total = $state(0);
  let loading = $state(false);
  let error = $state('');
  let historyLoaded = $state(false);
  let wsConnected = $state(false);
  let ws: WebSocket | null = null;

  const limit = 50;
  let offset = $state(0);

  let filterExecId = $state('');
  let filterServiceName = $state('');
  let filterStateName = $state('');
  let filterLevel = $state('');
  let filterSince = $state('');
  let filterUntil = $state('');
  let filterQ = $state('');

  function getToken(): string | null {
    if (typeof localStorage === 'undefined') return null;
    return localStorage.getItem('kflow_token');
  }

  onMount(() => {
    const sp = $page.url.searchParams;
    filterExecId = sp.get('execution_id') ?? '';
    filterServiceName = sp.get('service_name') ?? '';
    filterStateName = sp.get('state_name') ?? '';
    filterLevel = sp.get('level') ?? '';
    filterQ = sp.get('q') ?? '';
    if (filterExecId || filterServiceName || filterLevel || filterQ) {
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
    if (resetOffset) {
      offset = 0;
      logs = [];
      total = 0;
    }
    closeWS();
    historyLoaded = false;
    loading = true;
    error = '';
    openWSLogs();
  }

  function openWSLogs() {
    const params = new URLSearchParams();
    if (filterExecId) params.set('execution_id', filterExecId);
    if (filterServiceName) params.set('service_name', filterServiceName);
    if (filterStateName) params.set('state_name', filterStateName);
    if (filterLevel) params.set('level', filterLevel);
    if (filterSince) params.set('since', filterSince);
    if (filterUntil) params.set('until', filterUntil);
    if (filterQ) params.set('q', filterQ);
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
</script>

<div class="p-8 max-w-6xl">
<h1 class="text-3xl font-medium">Log Explorer</h1>

<form class="filters" onsubmit={(e) => { e.preventDefault(); search(); }}>
  <input bind:value={filterExecId} placeholder="Execution ID" />
  <input bind:value={filterServiceName} placeholder="Service name" />
  <input bind:value={filterStateName} placeholder="State name" />
  <select bind:value={filterLevel}>
    <option value="">All levels</option>
    <option value="INFO">INFO</option>
    <option value="WARN">WARN</option>
    <option value="ERROR">ERROR</option>
    <option value="DEBUG">DEBUG</option>
  </select>
  <input bind:value={filterSince} placeholder="Since (ISO 8601)" />
  <input bind:value={filterUntil} placeholder="Until (ISO 8601)" />
  <input bind:value={filterQ} placeholder="Search text" class="min-w-[180px]" />
  <button type="submit">Search</button>
</form>

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
