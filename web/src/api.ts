// Types mirror internal/agent/plan.go and internal/platform/turn.go.

export type Stage =
  | "clarify"
  | "inspire"
  | "search"
  | "compare"
  | "assemble"
  | "checkout"
  | "post_purchase";

export interface HotelOption {
  id: string;
  name: string;
  city: string;
  stars: number;
  rating: number;
  meal: string;
  free_cancellation: boolean;
  pets_allowed: boolean;
  price_per_night_rub: number;
  total_price_rub: number;
  distance_to_center_km: number;
  over_budget?: boolean;
  synthetic: boolean;
}

export interface TripPlan {
  session_id: string;
  version: number;
  stage: Stage;
  constraints: {
    destination?: string;
    check_in?: string;
    nights?: number;
    adults?: number;
    children?: number;
    pets?: boolean;
    budget_rub?: number;
  };
  assumptions: { field: string; value: string; reason: string }[];
  options: HotelOption[];
  selected?: HotelOption;
  weather?: {
    city: string;
    date: string;
    temp_max_c: number;
    temp_min_c: number;
    precipitation_mm: number;
    source: "forecast" | "last_year";
  };
  conflicts: { kind: string; message: string }[];
}

export type AgentEvent =
  | { type: "status"; stage?: Stage; text?: string }
  | { type: "token"; text: string }
  | { type: "plan"; plan: TripPlan }
  | { type: "done"; stage?: Stage }
  | { type: "error"; message: string };

export interface Order {
  id: string;
  session_id: string;
  status: string;
  hotel_id: string;
  hotel_name: string;
  check_in: string;
  nights: number;
  total_price_rub: number;
  created_at: string;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(body.error ?? `Ошибка ${res.status}`);
  }
  return body as T;
}

export async function createSession(): Promise<string> {
  const { session_id } = await request<{ session_id: string }>("/api/sessions", { method: "POST" });
  return session_id;
}

export function sendMessage(sessionId: string, text: string) {
  return request<{ message_id: string }>(`/api/sessions/${sessionId}/messages`, {
    method: "POST",
    body: JSON.stringify({ text }),
  });
}

export function openEvents(sessionId: string): EventSource {
  return new EventSource(`/api/sessions/${sessionId}/events`);
}

export function checkout(
  sessionId: string,
  idempotencyKey: string,
  body: {
    hotel_id: string;
    check_in: string;
    nights: number;
    adults: number;
    children: number;
    guest_name: string;
    with_pets: boolean;
  },
) {
  return request<Order>(`/api/sessions/${sessionId}/checkout`, {
    method: "POST",
    headers: { "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(body),
  });
}
