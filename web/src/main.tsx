import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import "./index.css";

// VITE_MOCKS=1 runs the frontend without a backend: MSW answers /api/v1 from
// src/mocks. Without the flag this branch and the mocks are not in the bundle.
if (import.meta.env.VITE_MOCKS === "1") {
  const { network } = await import("virtual:msw");
  const { handlers } = await import("./mocks/handlers");
  network.configure({ handlers });
  await network.enable();
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
