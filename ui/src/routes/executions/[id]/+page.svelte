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

{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty" style="color:#f38ba8">{error}</p>
{:else if execution}
  {#if wasDisconnected}
    <div class="reconnect-notice">WebSocket reconnected — refreshing state…</div>
  {/if}

  <div class="meta">
    <h1>Execution: <code>{execution.id}</code></h1>
    <div class="meta-row">
      <span>Workflow: <strong>{execution.workflow}</strong></span>
      <span>Status: <span class="badge badge-{execution.status.toLowerCase()}">{execution.status}</span></span>
      <span>Started: {new Date(execution.created_at).toLocaleString()}</span>
      <span>Duration: {duration(execution.created_at, execution.updated_at)}</span>
    </div>
  </div>

  <h2>States</h2>
  {#if states.length === 0}
    <p class="empty">No states yet.</p>
  {:else}
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
        {#each states as s (s.state_name)}
          <tr on:click={() => toggleState(s.state_name)}>
            <td>{s.state_name}</td>
            <td><span class="badge badge-{s.status.toLowerCase()}">{s.status}</span></td>
            <td>{s.attempt}</td>
            <td>{duration(s.created_at, s.updated_at)}</td>
            <td style="color:#f38ba8;font-size:0.8rem">{s.error || '—'}</td>
          </tr>
          {#if expandedState === s.state_name}
            <tr>
              <td colspan="5">
                <div class="expanded">
                  <div class="json-block">
                    <strong>Input</strong>
                    <pre>{JSON.stringify(s.input, null, 2)}</pre>
                  </div>
                  <div class="json-block">
                    <strong>Output</strong>
                    <pre>{JSON.stringify(s.output, null, 2)}</pre>
                  </div>
                </div>
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  {/if}

  <h2>Event Timeline</h2>
  {#if events.length === 0}
    <p class="empty">No events recorded.</p>
  {:else}
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
        {#each events as ev (ev.event_id)}
          <tr>
            <td style="font-size:0.8rem">{new Date(ev.occurred_at).toLocaleString()}</td>
            <td>{ev.state_name}</td>
            <td>
              <span class="badge badge-{ev.from_status.toLowerCase()}">{ev.from_status}</span>
              →
              <span class="badge badge-{ev.to_status.toLowerCase()}">{ev.to_status}</span>
            </td>
            <td style="color:#f38ba8;font-size:0.8rem">{ev.error || '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{/if}

<style>
  .meta {
    margin-bottom: 1.5rem;
  }

  .meta-row {
    display: flex;
    gap: 1.5rem;
    flex-wrap: wrap;
    font-size: 0.9rem;
    color: #a6adc8;
  }

  h2 {
    font-size: 1rem;
    color: #a6adc8;
    margin: 1.5rem 0 0.5rem;
    border-bottom: 1px solid #313244;
    padding-bottom: 0.4rem;
  }

  .expanded {
    display: flex;
    gap: 1rem;
    padding: 0.75rem 0;
    flex-wrap: wrap;
  }

  .json-block {
    flex: 1;
    min-width: 200px;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }

  .reconnect-notice {
    background: #f9e2af;
    color: #1e1e2e;
    padding: 0.5rem 0.75rem;
    border-radius: 4px;
    margin-bottom: 1rem;
    font-size: 0.875rem;
  }
</style>
