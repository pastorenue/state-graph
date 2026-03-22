<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import {
    getExecution,
    listExecutionStates,
    getExecutionEvents,
    type Execution,
    type StateRecord,
    type ExecutionEvent,
  } from '$lib/api';
  import { wsEvents, wsConnected } from '$lib/wsStore';
  import type { StateTransitionPayload } from '$lib/ws';

  $: id = $page.params.id as string;

  let execution: Execution | null = null;
  let states: StateRecord[] = [];
  let events: ExecutionEvent[] = [];
  let expandedState: string | null = null;
  let loading = true;
  let error = '';
  let wasDisconnected = false;

  async function load() {
    loading = true;
    error = '';
    try {
      [execution, states, events] = await Promise.all([
        getExecution(id),
        listExecutionStates(id),
        getExecutionEvents(id, { limit: 100 }),
      ]);
    } catch (e) {
      const err = e as { error: string };
      error = err.error ?? 'Failed to load execution';
    } finally {
      loading = false;
    }
  }

  onMount(load);

  $: if ($wsEvents?.type === 'state_transition') {
    const p = $wsEvents.payload as StateTransitionPayload;
    if (p.execution_id === id) {
      const idx = states.findIndex((s) => s.state_name === p.state_name);
      if (idx >= 0) {
        states[idx] = {
          ...states[idx],
          status: p.to_status as StateRecord['status'],
          error: p.error ?? '',
          updated_at: $wsEvents.timestamp,
        };
        states = [...states];
      }
      if (execution) {
        execution = {
          ...execution,
          status: p.to_status as Execution['status'],
          updated_at: $wsEvents.timestamp,
        };
      }
    }
  }

  $: {
    if (!$wsConnected && !loading) {
      wasDisconnected = true;
    }
    if ($wsConnected && wasDisconnected) {
      wasDisconnected = false;
      load();
    }
  }

  function duration(created: string, updated: string): string {
    const ms = new Date(updated).getTime() - new Date(created).getTime();
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
    return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`;
  }

  function toggleState(name: string) {
    expandedState = expandedState === name ? null : name;
  }
</script>

<div class="p-8 max-w-6xl">
{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty text-red-600">{error}</p>
{:else if execution}
  {#if wasDisconnected}
    <div class="bg-amber-900/30 text-amber-300 px-3 py-2 rounded-md mb-4 text-sm">WebSocket reconnected — refreshing state…</div>
  {/if}

  <div class="mb-6">
    <h1 class="text-2xl font-medium">Execution: <code>{execution.id}</code></h1>
    <div class="flex gap-6 flex-wrap text-sm text-muted">
      <span>Workflow: <strong class="text-text">{execution.workflow}</strong></span>
      <span>Status: <span class="badge badge-{execution.status.toLowerCase()}">{execution.status}</span></span>
      <span>Started: {new Date(execution.created_at).toLocaleString()}</span>
      <span>Duration: {duration(execution.created_at, execution.updated_at)}</span>
    </div>
  </div>

  <h2 class="text-xl text-muted mt-6 mb-2 border-b border-border pb-1">States</h2>
  <table>
    <thead>
      <tr>
        <th>State</th>
        <th>Status</th>
        <th>Attempt</th>
        <th>Duration</th>
        <th>Error</th>
      </tr>
    </thead>
    <tbody>
      {#if states.length === 0}
        <tr class="hover:bg-transparent cursor-default">
          <td colspan="5" class="empty border-none">No states yet.</td>
        </tr>
      {:else}
        {#each states as s (s.state_name)}
          <tr on:click={() => toggleState(s.state_name)}>
            <td>{s.state_name}</td>
            <td><span class="badge badge-{s.status.toLowerCase()}">{s.status}</span></td>
            <td>{s.attempt}</td>
            <td>{duration(s.created_at, s.updated_at)}</td>
            <td class="text-red-600 text-xs">{s.error || '—'}</td>
          </tr>
          {#if expandedState === s.state_name}
            <tr>
              <td colspan="5">
                <div class="flex gap-4 py-3 flex-wrap">
                  <div class="flex-1 min-w-[200px] flex flex-col gap-1">
                    <strong class="text-sm text-muted">Input</strong>
                    <pre>{JSON.stringify(s.input, null, 2)}</pre>
                  </div>
                  <div class="flex-1 min-w-[200px] flex flex-col gap-1">
                    <strong class="text-sm text-muted">Output</strong>
                    <pre>{JSON.stringify(s.output, null, 2)}</pre>
                  </div>
                </div>
              </td>
            </tr>
          {/if}
        {/each}
      {/if}
    </tbody>
  </table>

  <h2 class="text-xl text-muted mt-6 mb-2 border-b border-border pb-1">Event Timeline</h2>
  <table>
    <thead>
      <tr>
        <th>Time</th>
        <th>State</th>
        <th>Transition</th>
        <th>Error</th>
      </tr>
    </thead>
    <tbody>
      {#if events.length === 0}
        <tr class="hover:bg-transparent cursor-default">
          <td colspan="4" class="empty border-none">No events recorded.</td>
        </tr>
      {:else}
        {#each events as ev (ev.event_id)}
          <tr>
            <td class="text-xs">{new Date(ev.occurred_at).toLocaleString()}</td>
            <td>{ev.state_name}</td>
            <td>
              <span class="badge badge-{ev.from_status.toLowerCase()}">{ev.from_status}</span>
              →
              <span class="badge badge-{ev.to_status.toLowerCase()}">{ev.to_status}</span>
            </td>
            <td class="text-red-600 text-xs">{ev.error || '—'}</td>
          </tr>
        {/each}
      {/if}
    </tbody>
  </table>
{/if}
</div>
