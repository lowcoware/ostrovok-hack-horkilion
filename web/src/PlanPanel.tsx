import { useState } from "react";
import { checkout, type HotelOption, type Order, type Stage, type TripPlan } from "@/api";
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

const rub = (n: number) => n.toLocaleString("ru-RU") + " ₽";

export function PlanPanel({ plan, sessionId }: { plan: TripPlan | null; sessionId: string | null }) {
  const [order, setOrder] = useState<Order | null>(null);
  const stage: Stage = order ? "post_purchase" : (plan?.stage ?? "clarify");
  const current = STAGES.findIndex((s) => s.id === stage);
  const c = plan?.constraints;

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
            <CardTitle>Бронирование оформлено</CardTitle>
            <CardDescription>Оплата имитирована, заказ {order.id.slice(0, 8)}</CardDescription>
          </CardHeader>
          <CardContent className="text-sm">
            {order.hotel_name}, заезд {order.check_in}, {order.nights} ноч., {rub(order.total_price_rub)}
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
              <CardTitle>{c.destination}</CardTitle>
              <CardDescription>
                Заезд {c.check_in}, {c.nights} ноч., взрослых {c.adults}
                {c.children ? `, детей ${c.children}` : ""}
                {c.pets ? ", с питомцем" : ""}
                {c.budget_rub ? `, бюджет ${rub(c.budget_rub)}` : ""}
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-2 text-sm">
              {plan.weather && (
                <p>
                  Погода ({plan.weather.source === "forecast" ? "прогноз" : "в этот день год назад"}): от{" "}
                  {Math.round(plan.weather.temp_min_c)} до {Math.round(plan.weather.temp_max_c)} °C, осадки{" "}
                  {plan.weather.precipitation_mm} мм
                </p>
              )}
              {plan.assumptions.map((a) => (
                <p key={a.field} className="text-muted-foreground">
                  Допущение: {a.reason} ({a.value}). Напишите в чат, чтобы поправить.
                </p>
              ))}
              {plan.conflicts.map((cf) => (
                <p key={cf.kind} className="text-destructive">
                  {cf.message}
                </p>
              ))}
            </CardContent>
          </Card>

          {plan.options.length > 0 && (
            <p className="text-xs text-muted-foreground">Данные об отелях синтетические, погода реальная (Open-Meteo).</p>
          )}
          <div className="grid gap-3 sm:grid-cols-2">
            {plan.options.map((h) => (
              <HotelCard key={h.id} hotel={h} plan={plan} sessionId={sessionId} onBooked={setOrder} />
            ))}
          </div>
        </>
      )}
    </section>
  );
}

function HotelCard({
  hotel,
  plan,
  sessionId,
  onBooked,
}: {
  hotel: HotelOption;
  plan: TripPlan;
  sessionId: string | null;
  onBooked: (o: Order) => void;
}) {
  const [open, setOpen] = useState(false);
  const [guest, setGuest] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // One key per booking attempt: a retry after a network error reuses it and
  // cannot create a second order.
  const [idemKey] = useState(() => crypto.randomUUID());

  async function book() {
    if (!sessionId || !guest.trim()) return;
    setPending(true);
    setError(null);
    try {
      const c = plan.constraints;
      const order = await checkout(sessionId, idemKey, {
        hotel_id: hotel.id,
        check_in: c.check_in!,
        nights: c.nights!,
        adults: c.adults!,
        children: c.children ?? 0,
        guest_name: guest.trim(),
        with_pets: c.pets ?? false,
      });
      onBooked(order);
      setOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Не удалось оформить");
    } finally {
      setPending(false);
    }
  }

  return (
    <Card className={hotel.over_budget ? "opacity-70" : undefined}>
      <CardHeader>
        <CardTitle className="text-base">{hotel.name}</CardTitle>
        <CardDescription>
          {"★".repeat(hotel.stars)} · рейтинг {hotel.rating} · {hotel.distance_to_center_km} км от центра
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <div className="flex flex-wrap gap-1.5">
          <Badge variant="secondary">{hotel.meal}</Badge>
          <Badge variant={hotel.free_cancellation ? "secondary" : "outline"}>
            {hotel.free_cancellation ? "бесплатная отмена" : "без бесплатной отмены"}
          </Badge>
          {hotel.pets_allowed && <Badge variant="secondary">можно с животными</Badge>}
          {hotel.over_budget && <Badge variant="destructive">выше бюджета</Badge>}
        </div>
        <p>
          <span className="text-lg font-semibold">{rub(hotel.total_price_rub)}</span>{" "}
          <span className="text-muted-foreground">за всё, {rub(hotel.price_per_night_rub)} за ночь</span>
        </p>
        {open ? (
          <div className="space-y-2">
            <Input placeholder="Имя гостя" value={guest} maxLength={200} onChange={(e) => setGuest(e.target.value)} />
            <ShimmerButton className="w-full" disabled={pending || !guest.trim()} onClick={book}>
              {pending ? "Оформляю..." : "Оплатить (имитация)"}
            </ShimmerButton>
            {error && <p className="text-destructive">{error}</p>}
          </div>
        ) : (
          <ShimmerButton className="w-full" onClick={() => setOpen(true)}>
            Забронировать
          </ShimmerButton>
        )}
      </CardContent>
    </Card>
  );
}
