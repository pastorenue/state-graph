import { writable } from 'svelte/store';
import type { WSEvent } from './ws';

export const wsEvents = writable<WSEvent | null>(null);
export const wsConnected = writable<boolean>(false);
