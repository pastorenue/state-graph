<script lang="ts">
  import { onDestroy, onMount, type Snippet } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { createWSClient } from '$lib/ws';
  import { wsEvents, wsConnected } from '$lib/wsStore';

  let { children }: { children: Snippet } = $props();

  let client: ReturnType<typeof createWSClient>;

  function getToken(): string | null {
    if (typeof localStorage === 'undefined') return null;
    return localStorage.getItem('kflow_token');
  }

  function isTokenExpired(token: string): boolean {
    try {
      const parts = token.split('.');
      if (parts.length !== 2) return true;
      const payload = JSON.parse(atob(parts[0].replace(/-/g, '+').replace(/_/g, '/')));
      return new Date(payload.expires_at) <= new Date();
    } catch {
      return true;
    }
  }

  function logout() {
    localStorage.removeItem('kflow_token');
    goto('/login');
  }

  onMount(() => {
    const currentPath = $page.url.pathname;
    if (currentPath !== '/login') {
      const token = getToken();
      if (!token) {
        goto('/login');
        return;
      }
      if (isTokenExpired(token)) {
        localStorage.removeItem('kflow_token');
        goto('/login');
        return;
      }
    }

    client = createWSClient(
      '/api/v1/ws',
      (ev) => { wsEvents.set(ev); },
      getToken,
    );
    client.connect();

    const interval = setInterval(() => {
      wsConnected.set(client.isConnected());
    }, 1000);

    return () => clearInterval(interval);
  });

  onDestroy(() => {
    client?.disconnect();
  });
</script>

<nav>
  <a href="/">Executions</a>
  <a href="/services">Services</a>
  <a href="/logs">Logs</a>
  {#if $wsConnected}
    <span class="ws-badge connected">WS live</span>
  {:else}
    <span class="ws-badge disconnected">WS disconnected</span>
  {/if}
  <button class="logout-btn" onclick={logout}>Logout</button>
</nav>

<main>
  {@render children()}
</main>

<style>
  nav {
    display: flex;
    gap: 1.5rem;
    align-items: center;
    padding: 0.75rem 1.5rem;
    background: #1e1e2e;
    border-bottom: 1px solid #313244;
  }

  nav a {
    color: #cdd6f4;
    text-decoration: none;
    font-weight: 500;
  }

  nav a:hover {
    color: #89b4fa;
  }

  .ws-badge {
    margin-left: auto;
    font-size: 0.75rem;
    padding: 0.2rem 0.6rem;
    border-radius: 9999px;
  }

  .logout-btn {
    margin-left: 0.5rem;
    background: transparent;
    border: 1px solid #45475a;
    color: #a6adc8;
    font-size: 0.75rem;
    padding: 0.2rem 0.6rem;
    border-radius: 4px;
    cursor: pointer;
  }

  .logout-btn:hover {
    background: #313244;
    color: #cdd6f4;
  }

  .ws-badge.connected {
    background: #a6e3a1;
    color: #1e1e2e;
  }

  .ws-badge.disconnected {
    background: #f38ba8;
    color: #1e1e2e;
  }

  main {
    padding: 1.5rem;
    font-family: system-ui, sans-serif;
    background: #181825;
    min-height: calc(100vh - 49px);
    color: #cdd6f4;
  }

  :global(body) {
    margin: 0;
    background: #181825;
  }

  :global(table) {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9rem;
  }

  :global(th) {
    text-align: left;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid #313244;
    color: #a6adc8;
    font-weight: 500;
  }

  :global(td) {
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid #1e1e2e;
  }

  :global(tr:hover td) {
    background: #24273a;
    cursor: pointer;
  }

  :global(.badge) {
    display: inline-block;
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    font-weight: 600;
  }

  :global(.badge-pending)   { background: #45475a; color: #cdd6f4; }
  :global(.badge-running)   { background: #89b4fa; color: #1e1e2e; }
  :global(.badge-completed) { background: #a6e3a1; color: #1e1e2e; }
  :global(.badge-failed)    { background: #f38ba8; color: #1e1e2e; }
  :global(.badge-stopped)   { background: #45475a; color: #cdd6f4; }
  :global(.badge-info)      { background: #89b4fa; color: #1e1e2e; }
  :global(.badge-warn)      { background: #f9e2af; color: #1e1e2e; }
  :global(.badge-error)     { background: #f38ba8; color: #1e1e2e; }
  :global(.badge-debug)     { background: #45475a; color: #cdd6f4; }

  :global(button) {
    padding: 0.4rem 0.9rem;
    border: 1px solid #313244;
    border-radius: 4px;
    background: #313244;
    color: #cdd6f4;
    cursor: pointer;
    font-size: 0.875rem;
  }

  :global(button:hover) { background: #45475a; }

  :global(input, select) {
    padding: 0.35rem 0.6rem;
    border: 1px solid #313244;
    border-radius: 4px;
    background: #1e1e2e;
    color: #cdd6f4;
    font-size: 0.875rem;
  }

  :global(.filters) {
    display: flex;
    gap: 0.75rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
    align-items: center;
  }

  :global(.empty) {
    text-align: center;
    color: #6c7086;
    padding: 2rem;
  }

  :global(pre) {
    background: #1e1e2e;
    border: 1px solid #313244;
    border-radius: 4px;
    padding: 0.75rem;
    overflow-x: auto;
    font-size: 0.8rem;
    color: #cdd6f4;
    margin: 0;
  }

  :global(h1) {
    margin-top: 0;
    font-size: 1.25rem;
    color: #cdd6f4;
  }
</style>
