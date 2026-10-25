/// <reference types="vite/client" />
/// <reference types="msw/vite/client" />

interface ImportMetaEnv {
  /** "1" serves /api/v1 from src/mocks instead of the backend. */
  readonly VITE_MOCKS?: string;
}
