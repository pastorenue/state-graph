<script lang="ts">
  import { goto } from '$app/navigation';
  import { login } from '$lib/api';

  let apiKey = $state('');
  let error = $state('');
  let loading = $state(false);

  async function handleSubmit() {
    error = '';
    loading = true;
    try {
      const token = await login(apiKey);
      localStorage.setItem('kflow_token', token);
      await goto('/');
    } catch (e: unknown) {
      const err = e as { error?: string };
      error = err.error ?? 'Login failed';
    } finally {
      loading = false;
    }
  }
</script>

<div class="login-wrap">
  <form class="login-card" onsubmit={handleSubmit}>
    <h1>kflow</h1>
    <p class="subtitle">Control Plane</p>

    <label for="api-key">API Key</label>
    <input
      id="api-key"
      type="password"
      placeholder="Enter your API key"
      bind:value={apiKey}
      disabled={loading}
      autocomplete="current-password"
    />

    {#if error}
      <p class="error">{error}</p>
    {/if}

    <button type="submit" disabled={loading || !apiKey}>
      {loading ? 'Signing in…' : 'Login'}
    </button>
  </form>
</div>

<style>
  .login-wrap {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    background: #181825;
  }

  .login-card {
    background: #1e1e2e;
    border: 1px solid #313244;
    border-radius: 8px;
    padding: 2rem;
    width: 100%;
    max-width: 380px;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  h1 {
    margin: 0;
    font-size: 1.5rem;
    color: #cdd6f4;
    text-align: center;
  }

  .subtitle {
    margin: 0 0 0.5rem;
    text-align: center;
    color: #6c7086;
    font-size: 0.85rem;
  }

  label {
    font-size: 0.875rem;
    color: #a6adc8;
  }

  input {
    width: 100%;
    box-sizing: border-box;
    padding: 0.5rem 0.75rem;
    border: 1px solid #313244;
    border-radius: 4px;
    background: #181825;
    color: #cdd6f4;
    font-size: 0.9rem;
  }

  input:focus {
    outline: none;
    border-color: #89b4fa;
  }

  button {
    margin-top: 0.5rem;
    padding: 0.6rem;
    background: #89b4fa;
    color: #1e1e2e;
    border: none;
    border-radius: 4px;
    font-size: 0.9rem;
    font-weight: 600;
    cursor: pointer;
    transition: background 0.15s;
  }

  button:hover:not(:disabled) {
    background: #74c7ec;
  }

  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .error {
    margin: 0;
    color: #f38ba8;
    font-size: 0.85rem;
  }
</style>
