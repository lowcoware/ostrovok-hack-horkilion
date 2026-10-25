import { useState } from "react";
import {
  ApiError,
  checkout,
  patchPlan,
  type Check,
  type ComponentName,
  type Option,
  type Order,
  type Source,
  type Stage,
  type TripPlan,
} from "@/api";
import { Badge } from "@/components/ui/badge";
import { BorderBeam } from "@/components/ui/border-beam";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { ShimmerButton } from "@/components/ui/shimmer-button";

const STAGES: { id: Stage; label: string }[] = [
  { id: "clarify", label: "Уточнение" },
  { id: "inspire", label: "Вдохновение" },
  { id: "search", label: "Подбор" },
  { id: "compare", label: "Сравнение" },
  { id: "assemble", label: "Сборка" },
  { id: "checkout", label: "Покупка" },
  { id: "post_purchase", label: "После покупки" },
];

// Components in trip order.
const COMPONENTS: { id: ComponentName; label: string }[] = [
  { id: "outbound", label: "Туда" },
  { id: "transfer_arrival", label: "Трансфер до отеля" },
  { id: "stay", label: "Проживание" },
  { id: "transfer_departure", label: "Трансфер на обратную дорогу" },
  { id: "return", label: "Обратно" },
];

const MEALS: Record<string, string> = {
  room_only: "без питания",
  breakfast: "завтрак",
  half_board: "завтрак и ужин",
  all_inclusive: "всё включено",
};

const TRANSFER_MODES: Record<string, string> = { taxi: "Такси", shuttle: "Шаттл", public_transport: "Общественный транспорт" };

const CHECK_STYLE: Record<Check["status"], string> = {
  ok: "text-muted-foreground",
  warn: "text-amber-600 dark:text-amber-400",
  fail: "text-destructive",
};

const rub = (n: number) => n.toLocaleString("ru-RU") + " ₽";
const time = (iso: string) => iso.slice(11, 16);
const day = (iso: string) => iso.slice(8, 10) + "." + iso.slice(5, 7);
const hours = (min: number) => `${Math.floor(min / 60)} ч ${min % 60} мин`;

export function PlanPanel({
  plan,
  sessionId,
  busy,
  onTurnStarted,
}: {
  plan: TripPlan | null;
  sessionId: string | null;
  busy: boolean;
  onTurnStarted: () => void;
}) {
  const [order, setOrder] = useState<Order | null>(null);
  const [error, setError] = useState<string | null>(null);
  const stage: Stage = order ? "post_purchase" : (plan?.stage ?? "clarify");
  const current = STAGES.findIndex((s) => s.id === stage);
  const c = plan?.constraints;

  async function select(component: ComponentName, optionId: string) {
    if (!sessionId) return;
    setError(null);
    try {
      await patchPlan(sessionId, { select: { component, option_id: optionId } });
      onTurnStarted();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Не удалось изменить план");
    }
  }

  return (
    <section className="space-y-4">
      <ol className="flex flex-wrap gap-1.5">
        {STAGES.map((s, i) => (
          <li key={s.id}>
            <Badge variant={i === current ? "default" : i < current ? "secondary" : "outline"}>{s.label}</Badge>
          </li>
        ))}
      </ol>

      {order && (
        <Card className="relative overflow-hidden">
          <CardHeader>
            <CardTitle>Поездка оформлена</CardTitle>
            <CardDescription>Оплата имитирована, заказ {order.id.slice(0, 8)}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-1 text-sm">
            {order.items.map((it) => (
              <p key={it.offer_id}>
                {it.title} — {rub(it.price_rub)}
              </p>
            ))}
            <p className="font-semibold">Итого {rub(order.total_rub)}</p>
          </CardContent>
          <BorderBeam duration={6} size={120} />
        </Card>
      )}

      {!plan || !c?.destination ? (
        <Card>
          <CardHeader>
            <CardTitle>План поездки</CardTitle>
            <CardDescription>Появится, как только станет понятно направление.</CardDescription>
          </CardHeader>
        </Card>
      ) : (
        <>
          <Card>
            <CardHeader>
              <CardTitle>
                {c.origin ? `${c.origin} → ` : ""}
                {c.destination}
              </CardTitle>
              <CardDescription>
                {c.dates.depart && `${c.dates.depart} – ${c.dates.return}, ${c.dates.nights} ноч.`} Взрослых {c.adults}
                {c.child_ages.length > 0 && `, детей ${c.child_ages.length} (${c.child_ages.join(", ")} лет)`}
                {c.pets && ", с питомцем"}
                {c.budget && `, бюджет ${rub(c.budget.amount_rub)}`}
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-2 text-sm">
              {plan.weather && (
                <p>
                  Погода ({plan.weather.kind === "forecast" ? "прогноз" : "по прошлым годам"}): от{" "}
                  {Math.round(plan.weather.temp_min_c)} до {Math.round(plan.weather.temp_max_c)} °C, осадки{" "}
                  {plan.weather.precipitation_mm} мм <SourceBadge sources={plan.weather.sources} />
                </p>
              )}
              {plan.assumptions.map((a) => (
                <p key={a.field} className="text-muted-foreground">
                  Допущение: {a.reason} ({a.value}). Напишите в чат, чтобы поправить.
                </p>
              ))}
              {plan.conflicts.map((cf) => (
                <p key={cf.kind + cf.message} className="text-destructive">
                  {cf.message}
                </p>
              ))}
            </CardContent>
          </Card>

          {COMPONENTS.map(({ id, label }) => {
            const comp = plan.components[id];
            if (comp.options.length === 0) return null;
            return (
              <div key={id} className="space-y-2">
                <h3 className="text-sm font-medium text-muted-foreground">{label}</h3>
                <div className="grid gap-3 sm:grid-cols-2">
                  {comp.options.map((o) => (
                    <OptionCard
                      key={o.id}
                      option={o}
                      selected={o.id === comp.selected_id}
                      disabled={busy}
                      onSelect={() => select(id, o.id)}
                    />
                  ))}
                </div>
              </div>
            );
          })}

          {plan.checks.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Стыковки</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1 text-sm">
                {plan.checks.map((ch) => (
                  <p key={ch.kind} className={CHECK_STYLE[ch.status]}>
                    {ch.status === "ok" ? "✓" : ch.status === "warn" ? "!" : "✗"} {ch.message}
                  </p>
                ))}
              </CardContent>
            </Card>
          )}

          {plan.total_rub > 0 && !order && (
            <Cart plan={plan} sessionId={sessionId} onBooked={setOrder} disabled={busy} />
          )}
          {error && <p className="text-sm text-destructive">{error}</p>}
        </>
      )}
    </section>
  );
}

function SourceBadge({ sources }: { sources: Source[] }) {
  if (sources.length === 0) return null;
  const synthetic = sources.some((s) => s.kind === "synthetic");
  const real = sources.filter((s) => s.kind !== "synthetic").map((s) => s.provider);
  return (
    <span className="text-xs text-muted-foreground">
      [{[synthetic && "синтетика", ...real].filter(Boolean).join(" + ")}]
    </span>
  );
}

function OptionCard({
  option,
  selected,
  disabled,
  onSelect,
}: {
  option: Option;
  selected: boolean;
  disabled: boolean;
  onSelect: () => void;
}) {
  const unavailable = option.flags.includes("selected_unavailable");
  return (
    <Card className={selected ? "ring-2 ring-primary" : option.flags.includes("over_budget") ? "opacity-70" : undefined}>
      <CardHeader>
        <OptionTitle option={option} />
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <div className="flex flex-wrap gap-1.5">
          <OptionBadges option={option} />
          {option.flags.includes("over_budget") && <Badge variant="destructive">выше бюджета</Badge>}
          {unavailable && <Badge variant="destructive">больше не подходит</Badge>}
        </div>
        {option.explanation && (
          <div className="space-y-1">
            <p>{option.explanation.why}</p>
            {option.explanation.tradeoffs.map((t) => (
              <p key={t} className="text-muted-foreground">
                − {t}
              </p>
            ))}
            {option.explanation.risks.map((r) => (
              <p key={r} className="text-amber-600 dark:text-amber-400">
                ! {r}
              </p>
            ))}
          </div>
        )}
        <p className="flex items-baseline justify-between gap-2">
          <span className="text-lg font-semibold">{rub(option.price_rub)}</span>
          <SourceBadge sources={option.sources} />
        </p>
        {!selected && !unavailable && (
          <button
            className="w-full rounded-md border px-3 py-1.5 hover:bg-accent disabled:opacity-50"
            disabled={disabled}
            onClick={onSelect}
          >
            Выбрать
          </button>
        )}
      </CardContent>
    </Card>
  );
}

function OptionTitle({ option }: { option: Option }) {
  switch (option.kind) {
    case "stay":
      return (
        <>
          <CardTitle className="text-base">{option.offer.name}</CardTitle>
          <CardDescription>
            {"★".repeat(option.offer.stars)} · рейтинг {option.offer.rating} · {option.offer.distance_to_center_km} км от
            центра
          </CardDescription>
        </>
      );
    case "journey": {
      const segs = option.offer.segments;
      const first = segs[0];
      const last = segs[segs.length - 1];
      return (
        <>
          <CardTitle className="text-base">
            {first.mode === "air" ? "✈" : "🚆"} {first.carrier} {segs.map((s) => s.number).join(" + ")}
          </CardTitle>
          <CardDescription>
            {day(first.depart_at)} {time(first.depart_at)} {first.from.name} → {time(last.arrive_at)} {last.to.name} ·{" "}
            {hours(option.offer.duration_minutes)}
            {option.offer.transfers > 0 && `, пересадок ${option.offer.transfers}`}
          </CardDescription>
        </>
      );
    }
    case "transfer":
      return (
        <>
          <CardTitle className="text-base">{TRANSFER_MODES[option.offer.mode]}</CardTitle>
          <CardDescription>
            {option.offer.from.name} → {option.offer.to.name}, {time(option.offer.depart_at)}, {option.offer.duration_minutes} мин
          </CardDescription>
        </>
      );
  }
}

function OptionBadges({ option }: { option: Option }) {
  switch (option.kind) {
    case "stay":
      return (
        <>
          <Badge variant="secondary">{MEALS[option.offer.meal]}</Badge>
          <Badge variant={option.offer.free_cancellation ? "secondary" : "outline"}>
            {option.offer.free_cancellation ? "бесплатная отмена" : "без бесплатной отмены"}
          </Badge>
          {option.offer.pets_allowed && <Badge variant="secondary">можно с животными</Badge>}
        </>
      );
    case "journey":
      return (
        <>
          <Badge variant="secondary">{option.offer.baggage_included ? "с багажом" : "без багажа"}</Badge>
          <Badge variant={option.offer.refundable ? "secondary" : "outline"}>
            {option.offer.refundable ? "возвратный" : "невозвратный"}
          </Badge>
        </>
      );
    case "transfer":
      return null;
  }
}

function Cart({
  plan,
  sessionId,
  onBooked,
  disabled,
}: {
  plan: TripPlan;
  sessionId: string | null;
  onBooked: (o: Order) => void;
  disabled: boolean;
}) {
  const [guest, setGuest] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // One key per checkout attempt: a retry after a network error reuses it and
  // cannot create a second order.
  const [idemKey, setIdemKey] = useState(() => crypto.randomUUID());

  async function book() {
    if (!sessionId || !guest.trim()) return;
    setPending(true);
    setError(null);
    try {
      onBooked(await checkout(sessionId, idemKey, guest.trim()));
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // The cart changed (price or availability): the next attempt is a new order.
        setIdemKey(crypto.randomUUID());
      }
      setError(err instanceof Error ? err.message : "Не удалось оформить");
    } finally {
      setPending(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Итого {rub(plan.total_rub)}</CardTitle>
        <CardDescription>
          {plan.over_budget && plan.constraints.budget
            ? `Выше бюджета на ${rub(plan.total_rub - plan.constraints.budget.amount_rub)}`
            : "Выбранные варианты всех частей поездки"}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2">
        <Input placeholder="Имя гостя" value={guest} maxLength={200} onChange={(e) => setGuest(e.target.value)} />
        <ShimmerButton className="w-full" disabled={disabled || pending || !guest.trim()} onClick={book}>
          {pending ? "Оформляю..." : "Оформить (оплата имитирована)"}
        </ShimmerButton>
        {error && <p className="text-sm text-destructive">{error}</p>}
      </CardContent>
    </Card>
  );
}
