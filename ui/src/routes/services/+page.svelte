<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { listServices, type Service } from '$lib/api';
  import { wsEvents } from '$lib/wsStore';
  import type { ServiceUpdatePayload } from '$lib/ws';

  let services: Service[] = [];
  let expandedService: string | null = null;
  let loading = true;
  let error = '';

  async function load() {
    loading = true;
    error = '';
    try {
      services = await listServices();
    } catch (e) {
      const err = e as { error: string };
      error = err.error ?? 'Failed to load services';
    } finally {
      loading = false;
    }
  }

  onMount(load);

  $: if ($wsEvents?.type === 'service_update') {
    const p = $wsEvents.payload as ServiceUpdatePayload;
    const idx = services.findIndex((s) => s.name === p.service_name);
    if (idx >= 0) {
      services[idx] = {
        ...services[idx],
        status: p.status as Service['status'],
        updated_at: $wsEvents.timestamp,
      };
      services = [...services];
    }
  }

  function toggleService(name: string) {
    expandedService = expandedService === name ? null : name;
  }
</script>

<h1>Services</h1>

<div class="filters">
  <button on:click={load}>Refresh</button>
</div>

{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty" style="color:#f38ba8">{error}</p>
{:else if services.length === 0}
  <p class="empty">No services registered.</p>
{:else}
  <table>
    <thead>
      <tr>
        <th>Name</th>
        <th>Mode</th>
        <th>Status</th>
        <th>Replicas</th>
        <th>Endpoint</th>
        <th>Last Updated</th>
      </tr>
    </thead>
    <tbody>
      {#each services as svc (svc.name)}
        <tr on:click={() => toggleService(svc.name)}>
          <td><strong>{svc.name}</strong></td>
          <td>{svc.mode}</td>
          <td><span class="badge badge-{svc.status.toLowerCase()}">{svc.status}</span></td>
          <td>{svc.mode === 'Deployment' ? `${svc.min_scale}–${svc.max_scale}` : '—'}</td>
          <td style="font-size:0.8rem">{svc.ingress_host || '—'}</td>
          <td style="font-size:0.8rem">{new Date(svc.updated_at).toLocaleString()}</td>
        </tr>
        {#if expandedService === svc.name}
          <tr>
            <td colspan="6">
              <div class="detail">
                <div class="detail-row">
                  <span>Timeout: <strong>{svc.timeout_seconds}s</strong></span>
                  <span>Port: <strong>{svc.port}</strong></span>
                  <span>Cluster IP: <strong>{svc.cluster_ip || '—'}</strong></span>
                  <span>Scale: <strong>{svc.min_scale} – {svc.max_scale}</strong></span>
                </div>
                <div class="detail-row">
                  <a href="/logs?service_name={encodeURIComponent(svc.name)}" on:click|stopPropagation={() => goto(`/logs?service_name=${encodeURIComponent(svc.name)}`)}>
                    View logs →
                  </a>
                </div>
              </div>
            </td>
          </tr>
        {/if}
      {/each}
    </tbody>
  </table>
{/if}

<style>
  .detail {
    padding: 0.75rem;
    background: #1e1e2e;
    border-radius: 4px;
    margin: 0.25rem 0;
  }

  .detail-row {
    display: flex;
    gap: 1.5rem;
    font-size: 0.875rem;
    color: #a6adc8;
    flex-wrap: wrap;
    margin-bottom: 0.4rem;
  }

  .detail-row a {
    color: #89b4fa;
    font-size: 0.875rem;
  }
</style>
