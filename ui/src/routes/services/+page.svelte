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

<div class="p-8 max-w-6xl">
<h1>Services</h1>

<div class="filters">
  <button on:click={load}>Refresh</button>
</div>

{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty text-red-400">{error}</p>
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
          <td class="text-xs">{svc.ingress_host || '—'}</td>
          <td class="text-xs">{new Date(svc.updated_at).toLocaleString()}</td>
        </tr>
        {#if expandedService === svc.name}
          <tr>
            <td colspan="6">
              <div class="p-3 bg-surface rounded-md my-1">
                <div class="flex gap-6 text-sm text-muted flex-wrap mb-1">
                  <span>Timeout: <strong class="text-text">{svc.timeout_seconds}s</strong></span>
                  <span>Port: <strong class="text-text">{svc.port}</strong></span>
                  <span>Cluster IP: <strong class="text-text">{svc.cluster_ip || '—'}</strong></span>
                  <span>Scale: <strong class="text-text">{svc.min_scale} – {svc.max_scale}</strong></span>
                </div>
                <div class="flex gap-6 text-sm text-muted flex-wrap">
                  <a class="text-accent hover:text-accent-dim text-sm" href="/logs?service_name={encodeURIComponent(svc.name)}" on:click|stopPropagation={() => goto(`/logs?service_name=${encodeURIComponent(svc.name)}`)}>
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
</div>
