<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { listExecutions, type Execution } from '$lib/api';
  import { wsEvents } from '$lib/wsStore';
  import type { StateTransitionPayload } from '$lib/ws';

  let executions: Execution[] = $state([]);
  let loading = $state(true);
  let error = $state('');
  let filterWorkflow = $state('');
  let filterStatus = $state('');
  let mounted = $state(false);
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;

  async function load() {
    loading = true;
    error = '';
    try {
      executions = await listExecutions({
        workflow: filterWorkflow || undefined,
        status: filterStatus || undefined,
        limit: 50,
      });
    } catch (e) {
      const err = e as { error: string };
      error = err.error ?? 'Failed to load executions';
    } finally {
      loading = false;
    }
  }

  onMount(() => { load().then(() => { mounted = true; }); });

  $effect(() => {
    const ev = $wsEvents;
    if (ev?.type === 'state_transition') {
      const p = ev.payload as StateTransitionPayload;
      const idx = executions.findIndex((ex) => ex.id === p.execution_id);
      if (idx >= 0) {
        executions[idx] = {
          ...executions[idx],
          status: p.to_status as Execution['status'],
          updated_at: ev.timestamp,
        };
      }
    }
  });

  $effect(() => {
    const _ = filterStatus;
    if (mounted) load();
  });

  $effect(() => {
    const _ = filterWorkflow;
    if (!mounted) return;
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(load, 400);
    return () => clearTimeout(debounceTimer);
  });

  function duration(exec: Execution): string {
    const ms = new Date(exec.updated_at).getTime() - new Date(exec.created_at).getTime();
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
    return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`;
  }

  function shortId(id: string): string {
    return id.length > 12 ? `${id.slice(0, 8)}…` : id;
  }

  function inputSummary(input: Record<string, unknown>): string {
    const s = JSON.stringify(input);
    return s.length > 60 ? `${s.slice(0, 57)}…` : s;
  }
</script>

<div class="p-8 max-w-6xl">
<h1>Executions</h1>

<div class="filters">
  <input bind:value={filterWorkflow} placeholder="Workflow name" />
  <select bind:value={filterStatus}>
    <option value="">All statuses</option>
    <option value="Pending">Pending</option>
    <option value="Running">Running</option>
    <option value="Completed">Completed</option>
    <option value="Failed">Failed</option>
  </select>
  <button onclick={load}>Refresh</button>
</div>

{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty text-red-600">{error}</p>
{:else if executions.length === 0}
  <p class="empty">No executions found.</p>
{:else}
  <table>
    <thead>
      <tr>
        <th>ID</th>
        <th>Workflow</th>
        <th>Status</th>
        <th>Started</th>
        <th>Duration</th>
        <th>Input</th>
      </tr>
    </thead>
    <tbody>
      {#each executions as exec (exec.id)}
        <tr onclick={() => goto(`/executions/${exec.id}`)}>
          <td><code>{shortId(exec.id)}</code></td>
          <td>{exec.workflow}</td>
          <td><span class="badge badge-{exec.status.toLowerCase()}">{exec.status}</span></td>
          <td>{new Date(exec.created_at).toLocaleString()}</td>
          <td>{duration(exec)}</td>
          <td class="text-xs text-muted">{inputSummary(exec.input)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}
</div>
