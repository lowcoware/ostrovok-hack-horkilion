import type {
  ApiErrorBody,
  Order,
  PlanPatch,
  SessionCreated,
  SessionDetails,
  SourceList,
  TurnAccepted,
} from "./types";

const BASE = "/api/v1";

/** An error response of the API, see the Error schema. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorBody["error"]["code"] | "network";
  readonly details?: ApiErrorBody["error"]["details"];

  constructor(status: number, body: Partial<ApiErrorBody> | null) {
    super(body?.error?.message ?? `Ошибка ${status}`);
    this.status = status;
    this.code = body?.error?.code ?? "internal";
    this.details = body?.error?.details;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, {
    ...init,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw new ApiError(res.status, body);
  }
  return body as T;
}

export async function createSession(): Promise<string> {
  const { session_id } = await request<SessionCreated>("/sessions", { method: "POST" });
  return session_id;
}

export function getSession(sessionId: string) {
  return request<SessionDetails>(`/sessions/${sessionId}`);
}

export function sendMessage(sessionId: string, text: string) {
  return request<TurnAccepted>(`/sessions/${sessionId}/messages`, {
    method: "POST",
    body: JSON.stringify({ text }),
  });
}

/** Manual edit of the plan; the result arrives as a `plan` event. */
export function patchPlan(sessionId: string, patch: PlanPatch) {
  return request<TurnAccepted>(`/sessions/${sessionId}/plan`, {
    method: "POST",
    body: JSON.stringify(patch),
  });
}

export function openEvents(sessionId: string): EventSource {
  return new EventSource(`${BASE}/sessions/${sessionId}/events`);
}

export function checkout(sessionId: string, idempotencyKey: string, guestName: string) {
  return request<Order>(`/sessions/${sessionId}/checkout`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify({ guest_name: guestName }),
  });
}

export function listSources() {
  return request<SourceList>("/sources");
}
