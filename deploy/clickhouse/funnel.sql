-- Funnel queries over analytics.events (P1-BE-12). Reference queries for
-- Grafana panels and ad hoc analysis; nothing runs them automatically.

-- Drop-off by stage: how far each session got within 7 days.
-- level 1 = started, 2 = saw hotels, 3 = chose an option, 4 = paid.
SELECT level, count() AS sessions
FROM
(
    SELECT
        session_id,
        windowFunnel(604800)(
            toDateTime(event_time),
            event = 'session_started',
            event = 'stage_entered' AND stage IN ('compare', 'assemble'),
            event = 'option_selected',
            event = 'checkout_completed'
        ) AS level
    FROM analytics.events
    GROUP BY session_id
)
GROUP BY level
ORDER BY level;

-- Share of orders bought as a whole trip (hotel, both journeys, both
-- transfers) and the average order.
SELECT
    count() AS orders,
    countIf(JSONExtractBool(props, 'full_trip')) / count() AS full_trip_share,
    avg(JSONExtractInt(props, 'total_rub')) AS average_order_rub
FROM analytics.events
WHERE event = 'checkout_completed';
