// Checks the mock fixture against api/openapi.yaml. TypeScript already checks
// it against the generated types (`satisfies TripPlan`); the schema also
// catches formats, ranges and enum values that types do not express.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { parse } from "yaml";
import { emptyPlan, tripFixture } from "../src/mocks/fixture.ts";

const spec = parse(readFileSync(new URL("../../api/openapi.yaml", import.meta.url), "utf8"));
// OpenAPI keywords such as discriminator are not JSON Schema; strict mode would reject them.
const ajv = new Ajv2020({ strict: false, allErrors: true });
addFormats(ajv);
ajv.addSchema(spec, "openapi");
const validate = (schema: string) => ajv.compile({ $ref: `openapi#/components/schemas/${schema}` });

const sessionId = "4b0c5a5e-8f0e-4a43-9c55-2f3b9e3c7a10";

test("full trip fixture matches the TripPlan schema", () => {
  const check = validate("TripPlan");
  assert.ok(check(tripFixture(sessionId)), JSON.stringify(check.errors, null, 2));
});

test("empty plan matches the TripPlan schema", () => {
  const check = validate("TripPlan");
  assert.ok(check(emptyPlan(sessionId)), JSON.stringify(check.errors, null, 2));
});

test("fixture covers the whole trip", () => {
  const plan = tripFixture(sessionId);
  for (const name of ["outbound", "stay", "return", "transfer_arrival", "transfer_departure"] as const) {
    const comp = plan.components[name];
    assert.ok(comp.options.some((o) => o.id === comp.selected_id), `${name} must have a selected option`);
  }
  assert.ok(plan.checks.length > 0, "fixture must have connection checks");
  const total = Object.values(plan.components)
    .map((c) => c.options.find((o) => o.id === c.selected_id)?.price_rub ?? 0)
    .reduce((a, b) => a + b, 0);
  assert.equal(plan.total_rub, total, "total_rub must be the sum of the selected options");
});

test("schema rejects a broken plan", () => {
  const broken = { ...tripFixture(sessionId), stage: "flying", session_id: "not-a-uuid" };
  assert.equal(validate("TripPlan")(broken), false);
});
