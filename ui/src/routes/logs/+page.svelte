<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { queryLogs, type LogLine } from '$lib/api';

  let logs: LogLine[] = [];
  let total = 0;
  let loading = false;
  let error = '';

  const limit = 50;
  let offset = 0;

  let filterExecId = '';
  let filterServiceName = '';
  let filterStateName = '';
  let filterLevel = '';
  let filterSince = '';
  let filterUntil = '';
  let filterQ = '';

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

  async function search(resetOffset = true) {
    if (resetOffset) offset = 0;
    loading = true;
    error = '';
    try {
      const result = await queryLogs({
        execution_id: filterExecId || undefined,
        service_name: filterServiceName || undefined,
        state_name: filterStateName || undefined,
        level: filterLevel || undefined,
        since: filterSince || undefined,
        until: filterUntil || undefined,
        q: filterQ || undefined,
        limit,
        offset,
      });
      logs = result.logs;
      total = result.total;
    } catch (e) {
      const err = e as { error: string };
      error = err.error ?? 'Failed to query logs';
    } finally {
      loading = false;
    }
  }

  async function nextPage() {
    offset += limit;
    await search(false);
  }

  async function prevPage() {
    offset = Math.max(0, offset - limit);
    await search(false);
  }

  function levelBadge(level: string): string {
    return `badge badge-${level.toLowerCase()}`;
  }
</script>

<h1>Log Explorer</h1>

<form class="filters" on:submit|preventDefault={() => search()}>
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
  <input bind:value={filterQ} placeholder="Search text" style="min-width:180px" />
  <button type="submit">Search</button>
</form>

{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty" style="color:#f38ba8">{error}</p>
{:else if logs.length === 0 && total === 0}
  <p class="empty">No logs found for the selected filters.</p>
{:else}
  <div class="pagination-info">
    Showing {offset + 1}–{Math.min(offset + limit, total)} of {total} results
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
      {#each logs as log (log.log_id)}
        <tr>
          <td style="font-size:0.8rem;white-space:nowrap">{new Date(log.occurred_at).toLocaleString()}</td>
          <td><span class={levelBadge(log.level)}>{log.level}</span></td>
          <td style="font-size:0.8rem">
            {#if log.execution_id}
              <code>{log.execution_id.slice(0, 8)}…</code>
            {:else if log.service_name}
              {log.service_name}
            {/if}
          </td>
          <td style="font-size:0.8rem">{log.state_name || '—'}</td>
          <td style="font-size:0.85rem">{log.message}</td>
        </tr>
      {/each}
    </tbody>
  </table>

  <div class="pagination">
    <button on:click={prevPage} disabled={offset === 0}>← Prev</button>
    <button on:click={nextPage} disabled={offset + limit >= total}>Next →</button>
  </div>
{/if}

<style>
  .pagination-info {
    font-size: 0.8rem;
    color: #6c7086;
    margin-bottom: 0.5rem;
  }

  .pagination {
    display: flex;
    gap: 0.5rem;
    margin-top: 1rem;
  }

  button:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
</style>
