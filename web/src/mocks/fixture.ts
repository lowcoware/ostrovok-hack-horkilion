// A full trip as the agent would build it: transport both ways, a hotel, two
// transfers and connection checks. Mocks serve it, and test/fixture.test.ts
// checks it against api/openapi.yaml, so it is also the reference example of
// TripPlan v2 for every client.
import type { Source, TripPlan } from "../api/types";

const synthetic: Source = { provider: "synthetic", kind: "synthetic", field_scope: "*" };
const osm: Source = {
  provider: "osm",
  kind: "real",
  field_scope: "location",
  url: "https://www.openstreetmap.org",
  license: "ODbL-1.0",
  fetched_at: "2026-10-20T08:00:00Z",
};
const openMeteo: Source = {
  provider: "open-meteo",
  kind: "real",
  field_scope: "*",
  url: "https://open-meteo.com/en/docs",
  license: "CC-BY-4.0",
  fetched_at: "2026-10-25T09:00:00Z",
};

const svo = { name: "Шереметьево", kind: "airport", code: "SVO", country_code: "RU", timezone: "Europe/Moscow" } as const;
const aer = { name: "Сочи", kind: "airport", code: "AER", country_code: "RU", timezone: "Europe/Moscow" } as const;
const hotelPlace = (name: string, lat: number, lon: number) =>
  ({ name, kind: "hotel", country_code: "RU", point: { lat, lon }, timezone: "Europe/Moscow" }) as const;

export function tripFixture(sessionId: string, version = 3): TripPlan {
  return {
    session_id: sessionId,
    version,
    stage: "assemble",
    constraints: {
      origin: "Москва",
      destination: "Сочи",
      dates: { depart: "2026-11-20", return: "2026-11-25", nights: 5, flex_days: 1 },
      adults: 2,
      child_ages: [6],
      pets: false,
      budget: { amount_rub: 150000, strict: false },
      preferences: { free_cancellation: true, transport_modes: [], max_travel_hours: 6, moods: ["sea", "warm", "kids"] },
    },
    assumptions: [{ field: "child_ages", value: "[6]", reason: "возраст ребёнка не назван, взяли 6 лет" }],
    destination_options: [],
    components: {
      outbound: {
        selected_id: "air.SU1124.2026-11-20",
        options: [
          {
            id: "air.SU1124.2026-11-20",
            kind: "journey",
            price_rub: 31800,
            offer: {
              id: "air.SU1124.2026-11-20",
              segments: [
                {
                  mode: "air",
                  carrier: "Аэрофлот",
                  number: "SU 1124",
                  from: svo,
                  to: aer,
                  depart_at: "2026-11-20T09:40:00+03:00",
                  arrive_at: "2026-11-20T12:05:00+03:00",
                },
              ],
              duration_minutes: 145,
              transfers: 0,
              baggage_included: true,
              refundable: false,
              price_rub: 31800,
              sources: [synthetic],
            },
            explanation: {
              why: "Прямой утренний рейс: с ребёнком меньше всего времени в дороге, к заселению успеваете.",
              tradeoffs: ["Дороже вечернего рейса на 6 400 ₽"],
              risks: ["Невозвратный тариф"],
            },
            flags: [],
            sources: [synthetic],
          },
          {
            id: "rail.104В.2026-11-19",
            kind: "journey",
            price_rub: 24600,
            offer: {
              id: "rail.104В.2026-11-19",
              segments: [
                {
                  mode: "rail",
                  carrier: "РЖД",
                  number: "104В",
                  from: { name: "Москва Казанская", kind: "station", code: "2000003", timezone: "Europe/Moscow" },
                  to: { name: "Сочи", kind: "station", code: "2064130", timezone: "Europe/Moscow" },
                  depart_at: "2026-11-19T12:20:00+03:00",
                  arrive_at: "2026-11-20T15:51:00+03:00",
                },
              ],
              duration_minutes: 1651,
              transfers: 0,
              baggage_included: true,
              refundable: true,
              price_rub: 24600,
              sources: [synthetic],
            },
            explanation: {
              why: "Дешевле самолёта и можно сдать билет.",
              tradeoffs: ["27 часов в пути — больше лимита в 6 часов"],
              risks: [],
            },
            flags: [],
            sources: [synthetic],
          },
        ],
      },
      transfer_arrival: {
        selected_id: "taxi.AER.grand",
        options: [
          {
            id: "taxi.AER.grand",
            kind: "transfer",
            price_rub: 1900,
            offer: {
              id: "taxi.AER.grand",
              mode: "taxi",
              from: aer,
              to: hotelPlace("Отель «Морской бриз»", 43.585, 39.72),
              depart_at: "2026-11-20T12:35:00+03:00",
              duration_minutes: 45,
              price_rub: 1900,
              sources: [synthetic, osm],
            },
            flags: [],
            sources: [synthetic, osm],
          },
          {
            id: "train.AER.lastochka",
            kind: "transfer",
            price_rub: 420,
            offer: {
              id: "train.AER.lastochka",
              mode: "public_transport",
              from: aer,
              to: hotelPlace("Отель «Морской бриз»", 43.585, 39.72),
              depart_at: "2026-11-20T13:10:00+03:00",
              duration_minutes: 70,
              price_rub: 420,
              sources: [synthetic, osm],
            },
            flags: [],
            sources: [synthetic, osm],
          },
        ],
      },
      stay: {
        selected_id: "syn.0KHQvtGH0Lg.1",
        options: [
          {
            id: "syn.0KHQvtGH0Lg.1",
            kind: "stay",
            price_rub: 68500,
            offer: {
              id: "syn.0KHQvtGH0Lg.1",
              name: "Отель «Морской бриз»",
              city: "Сочи",
              place: hotelPlace("Отель «Морской бриз»", 43.585, 39.72),
              stars: 4,
              rating: 8.9,
              meal: "breakfast",
              free_cancellation: true,
              cancellation_policy: "бесплатная отмена до 18:00 за сутки до заезда",
              pets_allowed: false,
              check_in_time: "14:00",
              check_out_time: "12:00",
              price_per_night_rub: 13700,
              total_price_rub: 68500,
              distance_to_center_km: 1.2,
              sources: [synthetic, osm],
            },
            explanation: {
              why: "Пять минут до моря, завтраки и бесплатная отмена, как вы просили.",
              tradeoffs: ["Не самый дешёвый: «Уют» на 21 000 ₽ дешевле"],
              risks: ["Заселение с 14:00, а прилёт в 12:05 — придётся подождать"],
            },
            flags: [],
            sources: [synthetic, osm],
          },
          {
            id: "syn.0KHQvtGH0Lg.4",
            kind: "stay",
            price_rub: 47500,
            offer: {
              id: "syn.0KHQvtGH0Lg.4",
              name: "Гостевой дом «Уют»",
              city: "Сочи",
              place: hotelPlace("Гостевой дом «Уют»", 43.59, 39.735),
              stars: 3,
              rating: 8.1,
              meal: "room_only",
              free_cancellation: true,
              cancellation_policy: "бесплатная отмена до 18:00 за сутки до заезда",
              pets_allowed: true,
              check_in_time: "13:00",
              check_out_time: "11:00",
              price_per_night_rub: 9500,
              total_price_rub: 47500,
              distance_to_center_km: 2.8,
              sources: [synthetic, osm],
            },
            flags: [],
            sources: [synthetic, osm],
          },
          {
            id: "syn.0KHQvtGH0Lg.6",
            kind: "stay",
            price_rub: 112000,
            offer: {
              id: "syn.0KHQvtGH0Lg.6",
              name: "Отель «Ривьера»",
              city: "Сочи",
              place: hotelPlace("Отель «Ривьера»", 43.578, 39.716),
              stars: 5,
              rating: 9.4,
              meal: "all_inclusive",
              free_cancellation: false,
              cancellation_policy: "невозвратный тариф: при отмене стоимость не возвращается",
              pets_allowed: false,
              check_in_time: "15:00",
              check_out_time: "12:00",
              price_per_night_rub: 22400,
              total_price_rub: 112000,
              distance_to_center_km: 0.6,
              sources: [synthetic, osm],
            },
            flags: ["over_budget"],
            sources: [synthetic, osm],
          },
        ],
      },
      transfer_departure: {
        selected_id: "taxi.grand.AER",
        options: [
          {
            id: "taxi.grand.AER",
            kind: "transfer",
            price_rub: 1900,
            offer: {
              id: "taxi.grand.AER",
              mode: "taxi",
              from: hotelPlace("Отель «Морской бриз»", 43.585, 39.72),
              to: aer,
              depart_at: "2026-11-25T14:30:00+03:00",
              duration_minutes: 45,
              price_rub: 1900,
              sources: [synthetic, osm],
            },
            flags: [],
            sources: [synthetic, osm],
          },
        ],
      },
      return: {
        selected_id: "air.SU1125.2026-11-25",
        options: [
          {
            id: "air.SU1125.2026-11-25",
            kind: "journey",
            price_rub: 29400,
            offer: {
              id: "air.SU1125.2026-11-25",
              segments: [
                {
                  mode: "air",
                  carrier: "Аэрофлот",
                  number: "SU 1125",
                  from: aer,
                  to: svo,
                  depart_at: "2026-11-25T17:15:00+03:00",
                  arrive_at: "2026-11-25T19:50:00+03:00",
                },
              ],
              duration_minutes: 155,
              transfers: 0,
              baggage_included: true,
              refundable: false,
              price_rub: 29400,
              sources: [synthetic],
            },
            flags: [],
            sources: [synthetic],
          },
        ],
      },
    },
    weather: {
      place: { name: "Сочи", kind: "city", country_code: "RU", point: { lat: 43.6, lon: 39.73 } },
      date: "2026-11-20",
      temp_max_c: 15.8,
      temp_min_c: 9.4,
      precipitation_mm: 3.1,
      kind: "climate_normal",
      sources: [openMeteo],
    },
    checks: [
      {
        kind: "arrival_before_checkin",
        status: "warn",
        message: "Прилёт в 12:05, дорога 45 минут, заселение с 14:00: около часа ожидания. Можно попросить ранний заезд.",
        components: ["outbound", "transfer_arrival", "stay"],
      },
      {
        kind: "transfer_fits",
        status: "ok",
        message: "Такси в аэропорт к 15:15, за 2 часа до вылета.",
        components: ["stay", "transfer_departure", "return"],
      },
      {
        kind: "checkout_before_departure",
        status: "ok",
        message: "Выезд в 12:00, вылет в 17:15.",
        components: ["stay", "return"],
      },
      {
        kind: "dates_consistent",
        status: "ok",
        message: "Даты перелётов совпадают с датами проживания.",
        components: ["outbound", "stay", "return"],
      },
    ],
    conflicts: [],
    total_rub: 133500,
    over_budget: false,
  } satisfies TripPlan;
}

/** An empty plan of a session before the first turn. */
export function emptyPlan(sessionId: string): TripPlan {
  return {
    session_id: sessionId,
    version: 0,
    stage: "clarify",
    constraints: { dates: {}, child_ages: [], pets: false, preferences: { transport_modes: [], moods: [] } },
    assumptions: [],
    destination_options: [],
    components: {
      outbound: { options: [] },
      stay: { options: [] },
      return: { options: [] },
      transfer_arrival: { options: [] },
      transfer_departure: { options: [] },
    },
    checks: [],
    conflicts: [],
    total_rub: 0,
    over_budget: false,
  };
}
