// MSW handlers for running the frontend without a backend (VITE_MOCKS=1).
// They follow api/openapi.yaml: a new session already holds the full trip
// fixture, and every message or plan patch replays the events of one turn.
import { http, HttpResponse, sse } from "msw";
import type { AgentEvent, Order, PlanPatch, SourceList, TripPlan } from "../api/types";
import { tripFixture } from "./fixture";

interface MockSession {
  plan: TripPlan;
  messages: { role: "user" | "assistant"; text: string; created_at: string }[];
  // Open SSE connections of the session.
  clients: Set<(event: AgentEvent) => void>;
}

const sessions = new Map<string, MockSession>();
let seq = 0;

function session(id: string): MockSession {
  let s = sessions.get(id);
  if (!s) {
    // Unknown ids (e.g. from localStorage after a reload) get the fixture too.
    s = { plan: tripFixture(id), messages: [], clients: new Set() };
    sessions.set(id, s);
  }
  return s;
}

const now = () => new Date().toISOString();
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function emit(s: MockSession, turnId: string, event: Omit<AgentEvent, "session_id" | "turn_id" | "seq">) {
  const full = { ...event, session_id: s.plan.session_id, turn_id: turnId, seq: ++seq } as AgentEvent;
  s.clients.forEach((send) => send(full));
}

const REPLY =
  "Собрала поездку в Сочи на 20–25 ноября: прямой рейс Аэрофлота, «Морской бриз» в пяти минутах от моря " +
  "с завтраками и бесплатной отменой, такси из аэропорта и обратно. Итого 133 500 ₽ — в бюджете. " +
  "Прилёт раньше заселения примерно на час, могу запросить ранний заезд. Возраст ребёнка я предположила — поправьте, если не 6 лет.";

/** Replays one turn: status, streamed tokens, the new plan, done. */
async function playTurn(s: MockSession, turnId: string, userText: string) {
  s.messages.push({ role: "user", text: userText, created_at: now() });
  await sleep(150);
  emit(s, turnId, { type: "status", data: { stage: s.plan.stage, text: "разбираю запрос" } });
  await sleep(400);
  emit(s, turnId, { type: "status", data: { stage: "search", text: "ищу перелёты и отели" } });
  await sleep(600);
  for (const word of REPLY.split(/(?<= )/)) {
    emit(s, turnId, { type: "token", data: { text: word } });
    await sleep(25);
  }
  s.plan = { ...s.plan, version: s.plan.version + 1 };
  emit(s, turnId, { type: "plan", data: s.plan });
  s.messages.push({ role: "assistant", text: REPLY, created_at: now() });
  emit(s, turnId, { type: "done", data: { text: REPLY, stage: s.plan.stage } });
}

/** Applies a patch roughly like the agent: selection, constraints, totals. */
function applyPatch(plan: TripPlan, patch: PlanPatch): TripPlan {
  const next: TripPlan = structuredClone(plan);
  const { select, ...constraints } = patch;
  Object.assign(next.constraints, constraints, {
    dates: { ...plan.constraints.dates, ...constraints.dates },
    preferences: { ...plan.constraints.preferences, ...constraints.preferences },
  });
  if (select) {
    const comp = next.components[select.component];
    if (comp.options.some((o) => o.id === select.option_id)) comp.selected_id = select.option_id;
  }
  const selected = Object.values(next.components).map((c) => c.options.find((o) => o.id === c.selected_id));
  next.total_rub = selected.reduce((sum, o) => sum + (o?.price_rub ?? 0), 0);
  next.over_budget = !!next.constraints.budget && next.total_rub > next.constraints.budget.amount_rub;
  return next;
}

const sources: SourceList = {
  providers: [
    {
      provider: "open-meteo",
      title: "Open-Meteo",
      description: "Прогноз погоды и архив наблюдений.",
      kind: "real",
      url: "https://open-meteo.com",
      license: "CC-BY-4.0",
      used_for: ["погода"],
    },
    {
      provider: "osm",
      title: "OpenStreetMap",
      description: "Координаты отелей, станций и аэропортов.",
      kind: "real",
      url: "https://www.openstreetmap.org",
      license: "ODbL-1.0",
      used_for: ["расположение"],
    },
    {
      provider: "synthetic",
      title: "Синтетические данные",
      description: "Цены и расписания, сгенерированные поверх реальных объектов.",
      kind: "synthetic",
      used_for: ["цены", "расписания"],
    },
  ],
};

export const handlers = [
  http.post("/api/v1/sessions", () => {
    const id = crypto.randomUUID();
    session(id);
    return HttpResponse.json({ session_id: id }, { status: 201 });
  }),

  http.get("/api/v1/sessions/:id", ({ params }) => {
    const s = session(String(params.id));
    return HttpResponse.json({ plan: s.plan, messages: s.messages });
  }),

  http.post("/api/v1/sessions/:id/messages", async ({ params, request }) => {
    const { text } = (await request.json()) as { text: string };
    const turnId = crypto.randomUUID();
    void playTurn(session(String(params.id)), turnId, text);
    return HttpResponse.json({ message_id: turnId }, { status: 202 });
  }),

  http.post("/api/v1/sessions/:id/plan", async ({ params, request }) => {
    const patch = (await request.json()) as PlanPatch;
    const s = session(String(params.id));
    const turnId = crypto.randomUUID();
    s.plan = applyPatch(s.plan, patch);
    void playTurn(s, turnId, "Правка плана: " + JSON.stringify(patch));
    return HttpResponse.json({ message_id: turnId }, { status: 202 });
  }),

  sse("/api/v1/sessions/:id/events", ({ params, client }) => {
    const s = session(String(params.id));
    s.clients.add((event) => client.send({ data: event }));
  }),

  http.post("/api/v1/sessions/:id/checkout", async ({ params, request }) => {
    const { guest_name } = (await request.json()) as { guest_name: string };
    const plan = session(String(params.id)).plan;
    const items = Object.values(plan.components)
      .map((c) => c.options.find((o) => o.id === c.selected_id))
      .filter((o) => o !== undefined)
      .map((o) => ({
        kind: o.kind,
        offer_id: o.id,
        title:
          o.kind === "stay"
            ? o.offer.name
            : o.kind === "journey"
              ? `${o.offer.segments[0].carrier} ${o.offer.segments[0].number}`
              : `Трансфер ${o.offer.from.name} → ${o.offer.to.name}`,
        price_rub: o.price_rub,
      }));
    if (items.length === 0) {
      return HttpResponse.json({ error: { code: "nothing_selected", message: "в плане нет выбранных вариантов" } }, { status: 409 });
    }
    const order: Order = {
      id: crypto.randomUUID(),
      session_id: plan.session_id,
      status: "paid",
      items,
      total_rub: items.reduce((sum, i) => sum + i.price_rub, 0),
      guest_name,
      created_at: now(),
    };
    return HttpResponse.json(order);
  }),

  http.get("/api/v1/sources", () => HttpResponse.json(sources)),
  http.get("/api/v1/me", () => HttpResponse.json({ owner_id: "anon:mock", anonymous: true })),
  http.get("/api/v1/orders", () => HttpResponse.json({ orders: [] })),
];
