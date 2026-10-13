import axios, { AxiosError, type AxiosInstance, type InternalAxiosRequestConfig } from 'axios';
import * as SecureStore from 'expo-secure-store';
import { API_URL } from '../config';
import type { ApiError, ApiSuccess, AuthTokens } from '../types/api';

/**
 * HTTP client.
 *
 * One concern lives here that is easy to get wrong: when the access token
 * expires, several in-flight requests fail at once. Rather than firing a
 * refresh per request, the first 401 starts a single refresh and the others
 * queue behind it.
 *
 * Tokens live in the device keychain (SecureStore), mirrored in memory so the
 * request interceptor and the websocket can read them synchronously. The
 * mirror is filled once at start-up by `initTokens`, before the session is
 * restored.
 */

const TOKEN_KEY = 'bt.tokens';

export interface StoredTokens {
  accessToken: string;
  refreshToken: string;
}

let cached: StoredTokens | null = null;
let initialised = false;

/** Reads the stored session into memory. Call once before `hydrate`. */
export async function initTokens(): Promise<void> {
  if (initialised) return;
  try {
    const raw = await SecureStore.getItemAsync(TOKEN_KEY);
    cached = raw ? (JSON.parse(raw) as StoredTokens) : null;
  } catch {
    cached = null;
  }
  initialised = true;
}

export function loadTokens(): StoredTokens | null {
  return cached;
}

export function saveTokens(tokens: AuthTokens | StoredTokens): void {
  cached = { accessToken: tokens.accessToken, refreshToken: tokens.refreshToken };
  SecureStore.setItemAsync(TOKEN_KEY, JSON.stringify(cached)).catch(() => {
    /* the session simply will not survive a restart */
  });
}

export function clearTokens(): void {
  cached = null;
  SecureStore.deleteItemAsync(TOKEN_KEY).catch(() => undefined);
}

export class ApiRequestError extends Error {
  constructor(
    public readonly code: string,
    message: string,
    public readonly status: number,
    public readonly details?: unknown,
  ) {
    super(message);
    this.name = 'ApiRequestError';
  }
}

const baseURL = API_URL;

export const http: AxiosInstance = axios.create({
  baseURL,
  timeout: 15_000,
  headers: { 'content-type': 'application/json' },
});

http.interceptors.request.use((config: InternalAxiosRequestConfig) => {
  const tokens = loadTokens();
  if (tokens?.accessToken) config.headers.set('authorization', 'Bearer ' + tokens.accessToken);
  return config;
});

let refreshing: Promise<string | null> | null = null;
let onUnauthorised: (() => void) | null = null;

export function setUnauthorisedHandler(handler: () => void): void {
  onUnauthorised = handler;
}

async function refreshAccessToken(): Promise<string | null> {
  const tokens = loadTokens();
  if (!tokens?.refreshToken) return null;
  try {
    const response = await axios.post<ApiSuccess<{ tokens: AuthTokens }>>(
      baseURL + '/auth/refresh',
      { refreshToken: tokens.refreshToken },
      { headers: { 'content-type': 'application/json' } },
    );
    saveTokens(response.data.data.tokens);
    return response.data.data.tokens.accessToken;
  } catch {
    clearTokens();
    return null;
  }
}

http.interceptors.response.use(
  (response) => response,
  async (error: AxiosError<ApiError>) => {
    const original = error.config as (InternalAxiosRequestConfig & { _retried?: boolean }) | undefined;
    const status = error.response?.status ?? 0;
    const payload = error.response?.data;

    const isAuthFailure = status === 401 && payload?.error?.code !== 'INVALID_CREDENTIALS';
    if (isAuthFailure && original && !original._retried && !original.url?.includes('/auth/refresh')) {
      original._retried = true;
      refreshing =
        refreshing ??
        refreshAccessToken().finally(() => {
          refreshing = null;
        });
      const token = await refreshing;
      if (token) {
        original.headers.set('authorization', 'Bearer ' + token);
        return http.request(original);
      }
      onUnauthorised?.();
    }

    if (payload && payload.success === false) {
      throw new ApiRequestError(payload.error.code, payload.error.message, status, payload.error.details);
    }
    throw new ApiRequestError(
      'NETWORK_ERROR',
      error.message === 'Network Error' ? 'Cannot reach the trading server' : error.message,
      status,
    );
  },
);

/** Unwraps the success envelope so callers deal in plain data. */
export async function apiGet<T>(url: string, params?: Record<string, unknown>): Promise<T> {
  const response = await http.get<ApiSuccess<T>>(url, { params });
  return response.data.data;
}

export async function apiPost<T>(url: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  const response = await http.post<ApiSuccess<T>>(url, body, headers ? { headers } : undefined);
  return response.data.data;
}

export async function apiPatch<T>(url: string, body?: unknown): Promise<T> {
  const response = await http.patch<ApiSuccess<T>>(url, body);
  return response.data.data;
}

export async function apiPut<T>(url: string, body?: unknown): Promise<T> {
  const response = await http.put<ApiSuccess<T>>(url, body);
  return response.data.data;
}

/**
 * Multipart POST.
 *
 * The content type is set to multipart without a boundary; React Native's
 * networking layer writes the boundary itself when the body is a FormData.
 */
export async function postForm<T>(url: string, form: FormData): Promise<T> {
  const response = await http.post<ApiSuccess<T>>(url, form, {
    headers: { 'content-type': 'multipart/form-data' },
    transformRequest: (data) => data,
  });
  return response.data.data;
}

/** Absolute URL plus auth header, for an authenticated image (a deposit screenshot). */
export function authenticatedSource(path: string): { uri: string; headers: Record<string, string> } {
  const tokens = loadTokens();
  return {
    uri: baseURL + path,
    headers: tokens?.accessToken ? { Authorization: 'Bearer ' + tokens.accessToken } : {},
  };
}
