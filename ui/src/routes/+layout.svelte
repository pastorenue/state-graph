<script lang="ts">
  import { onDestroy, onMount, type Snippet } from 'svelte';
  import { page } from '$app/stores';
  import { createWSClient } from '$lib/ws';
  import { wsEvents, wsConnected } from '$lib/wsStore';
  import '../app.css';

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

  const navLinks = [
    {
      href: '/',
      label: 'Executions',
      icon: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/></svg>`,
    },
    {
      href: '/services',
      label: 'Services',
      icon: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><hexagon x="3" y="3" width="18" height="18" rx="2"/><path d="M12 2L2 7l10 5 10-5-10-5z"/><path d="M2 17l10 5 10-5"/><path d="M2 12l10 5 10-5"/></svg>`,
    },
    {
      href: '/logs',
      label: 'Logs',
      icon: `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="15" y2="18"/></svg>`,
    },
  ];

  function isActive(href: string, pathname: string): boolean {
    if (href === '/') return pathname === '/';
    return pathname.startsWith(href);
  }
</script>

<<<<<<< HEAD
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
=======
<div class="flex h-screen overflow-hidden bg-base">
  <!-- Sidebar -->
  <aside class="w-60 shrink-0 flex flex-col bg-surface border-r border-border">
    <!-- Brand -->
    <div class="py-6 px-5 flex items-center gap-2">
      <span class="w-2 h-2 rounded-full bg-accent"></span>
      <span class="text-text font-semibold tracking-tight">kflow</span>
    </div>
>>>>>>> 14fc29f (feat(ui): Tailwind CSS v4 + sidebar redesign)

    <!-- Nav links -->
    <nav class="flex-1 px-3 flex flex-col gap-1">
      {#each navLinks as link}
        {@const active = isActive(link.href, $page.url.pathname)}
        <a
          href={link.href}
          class="flex items-center gap-3 px-3 py-2 rounded-md text-sm transition-colors
            {active
              ? 'bg-accent/10 text-accent border-l-2 border-accent pl-[10px]'
              : 'text-muted hover:text-text hover:bg-raised border-l-2 border-transparent pl-[10px]'}"
        >
          <span class="shrink-0">{@html link.icon}</span>
          {link.label}
        </a>
      {/each}
    </nav>

    <!-- WS status pill -->
    <div class="px-5 py-4">
      {#if $wsConnected}
        <div class="flex items-center gap-2 text-xs text-green-400">
          <span class="w-2 h-2 rounded-full bg-green-400"></span>
          WS live
        </div>
      {:else}
        <div class="flex items-center gap-2 text-xs text-red-400">
          <span class="w-2 h-2 rounded-full bg-red-400"></span>
          WS disconnected
        </div>
      {/if}
    </div>
  </aside>

<<<<<<< HEAD
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
=======
  <!-- Main content -->
  <main class="flex-1 overflow-y-auto">
    {@render children()}
  </main>
</div>
>>>>>>> 14fc29f (feat(ui): Tailwind CSS v4 + sidebar redesign)
