export interface WSEvent {
  type: 'state_transition' | 'service_update';
  payload: StateTransitionPayload | ServiceUpdatePayload;
  timestamp: string;
}

export interface StateTransitionPayload {
  execution_id: string;
  state_name: string;
  from_status: string;
  to_status: string;
  error?: string;
}

export interface ServiceUpdatePayload {
  service_name: string;
  status: string;
}

export type EventHandler = (event: WSEvent) => void;

export function createWSClient(
  path: string,
  onEvent: EventHandler,
  getToken: () => string | null = () => null,
  reconnectDelayMs = 2000,
): { connect: () => void; disconnect: () => void; isConnected: () => boolean } {
  let ws: WebSocket | null = null;
  let intentionalClose = false;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  function connect() {
    intentionalClose = false;
    open();
  }

  function open() {
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const token = getToken();
    const suffix = token ? `?token=${encodeURIComponent(token)}` : '';
    const url = `${protocol}//${location.host}${path}${suffix}`;
    ws = new WebSocket(url);

    ws.onmessage = (ev) => {
      try {
        const event = JSON.parse(ev.data as string) as WSEvent;
        onEvent(event);
      } catch {
        // malformed message — ignore
      }
    };

    ws.onclose = () => {
      ws = null;
      if (!intentionalClose) {
        reconnectTimer = setTimeout(open, reconnectDelayMs);
      }
    };

    ws.onerror = () => {
      ws?.close();
    };
  }

  function disconnect() {
    intentionalClose = true;
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    ws?.close();
    ws = null;
  }

  function isConnected() {
    return ws !== null && ws.readyState === WebSocket.OPEN;
  }

  return { connect, disconnect, isConnected };
}
