// Names for the generated API types. Do not declare response types by hand:
// edit api/openapi.yaml and run `npm run gen:api`.
import type { components } from "./schema";

type Schemas = components["schemas"];

export type TripPlan = Schemas["TripPlan"];
export type Stage = Schemas["Stage"];
export type ComponentName = Schemas["ComponentName"];
export type Component = Schemas["Component"];
export type Option = Schemas["Option"];
export type StayOption = Schemas["StayOption"];
export type JourneyOption = Schemas["JourneyOption"];
export type TransferOption = Schemas["TransferOption"];
export type Hotel = Schemas["Hotel"];
export type Journey = Schemas["Journey"];
export type Transfer = Schemas["Transfer"];
export type Check = Schemas["Check"];
export type Source = Schemas["Source"];
export type PlanPatch = Schemas["PlanPatch"];
export type Order = Schemas["Order"];
export type SessionDetails = Schemas["SessionDetails"];
export type SessionCreated = Schemas["SessionCreated"];
export type TurnAccepted = Schemas["TurnAccepted"];
export type SourceList = Schemas["SourceList"];
export type ApiErrorBody = Schemas["Error"];
export type AgentEvent = Schemas["AgentEvent"];
