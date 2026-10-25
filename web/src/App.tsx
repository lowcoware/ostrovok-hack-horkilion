import { useEffect, useRef, useState } from "react";
import { createSession, openEvents, sendMessage, type AgentEvent, type TripPlan } from "@/api";
import { AnimatedShinyText } from "@/components/ui/animated-shiny-text";
import { BlurFade } from "@/components/ui/blur-fade";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PlanPanel } from "@/PlanPanel";

interface Message {
  role: "user" | "assistant";
  text: string;
}

const SESSION_KEY = "horkilion.session";

function loadSession(): string | null {
  try {
    return localStorage.getItem(SESSION_KEY);
  } catch {
    return null;
  }
}

function saveSession(id: string) {
  try {
    localStorage.setItem(SESSION_KEY, id);
  } catch {
    // Storage can be unavailable (private mode); the session just won't survive a reload.
  }
}

export default function App() {
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [connected, setConnected] = useState(false);
  const [messages, setMessages] = useState<Message[]>([]);
  const [plan, setPlan] = useState<TripPlan | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const streaming = useRef(false);
  const bottom = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let source: EventSource | null = null;
    let cancelled = false;

    (async () => {
      try {
        const id = loadSession() ?? (await createSession());
        if (cancelled) return;
        saveSession(id);
        setSessionId(id);
        source = openEvents(id);
        source.onopen = () => setConnected(true);
        source.onerror = () => setConnected(false);
        source.onmessage = (e) => handleEvent(JSON.parse(e.data) as AgentEvent);
      } catch (err) {
        setError(err instanceof Error ? err.message : "Не удалось подключиться");
      }
    })();

    return () => {
      cancelled = true;
      source?.close();
    };
  }, []);

  useEffect(() => {
    bottom.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages, status]);

  function handleEvent(ev: AgentEvent) {
    switch (ev.type) {
      case "status":
        setStatus(ev.text ?? null);
        break;
      case "token":
        setStatus(null);
        setMessages((prev) => {
          const last = prev[prev.length - 1];
          if (streaming.current && last?.role === "assistant") {
            return [...prev.slice(0, -1), { ...last, text: last.text + ev.text }];
          }
          streaming.current = true;
          return [...prev, { role: "assistant", text: ev.text }];
        });
        break;
      case "plan":
        setPlan(ev.plan);
        break;
      case "done":
        streaming.current = false;
        setBusy(false);
        setStatus(null);
        break;
      case "error":
        streaming.current = false;
        setBusy(false);
        setStatus(null);
        setError(ev.message);
        break;
    }
  }

  async function send(text: string) {
    if (!sessionId || !text.trim() || busy) return;
    setError(null);
    setBusy(true);
    streaming.current = false;
    setMessages((prev) => [...prev, { role: "user", text }]);
    setDraft("");
    try {
      await sendMessage(sessionId, text);
    } catch (err) {
      setBusy(false);
      setError(err instanceof Error ? err.message : "Не удалось отправить сообщение");
    }
  }

  function newTrip() {
    try {
      localStorage.removeItem(SESSION_KEY);
    } catch {
      // ignore
    }
    location.reload();
  }

  return (
    <div className="mx-auto flex min-h-dvh max-w-7xl flex-col px-4 py-4">
      <header className="mb-4 flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">
            <AnimatedShinyText>О!дин запрос</AnimatedShinyText>
          </h1>
          <p className="text-sm text-muted-foreground">Скажи, куда хочешь отправиться. Остальное устроит агент.</p>
        </div>
        <Button variant="outline" size="sm" onClick={newTrip}>
          Новая поездка
        </Button>
      </header>

      <main className="grid flex-1 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
        <section className="flex min-h-[60dvh] flex-col rounded-xl border bg-card">
          <div className="flex-1 space-y-3 overflow-y-auto p-4">
            {messages.length === 0 && (
              <div className="space-y-2 text-sm text-muted-foreground">
                <p>Например:</p>
                {["Хочу в Сочи на 5 ночей, нас двое", "Казань, 2 взрослых и ребёнок, бюджет 60 тысяч", "С нами едет собака"].map(
                  (s) => (
                    <button
                      key={s}
                      className="block rounded-md border px-3 py-2 text-left hover:bg-accent disabled:opacity-50"
                      disabled={!connected || busy}
                      onClick={() => send(s)}
                    >
                      {s}
                    </button>
                  ),
                )}
              </div>
            )}
            {messages.map((m, i) => (
              <BlurFade key={i} delay={0.05} inView={false}>
                <div className={m.role === "user" ? "flex justify-end" : "flex justify-start"}>
                  <div
                    className={
                      "max-w-[85%] whitespace-pre-wrap rounded-2xl px-4 py-2 text-sm " +
                      (m.role === "user" ? "bg-primary text-primary-foreground" : "bg-muted")
                    }
                  >
                    {m.text}
                  </div>
                </div>
              </BlurFade>
            ))}
            {status && <p className="text-sm text-muted-foreground">{status}...</p>}
            {error && <p className="text-sm text-destructive">{error}</p>}
            <div ref={bottom} />
          </div>
          <form
            className="flex gap-2 border-t p-3"
            onSubmit={(e) => {
              e.preventDefault();
              send(draft);
            }}
          >
            <Input
              value={draft}
              maxLength={2000}
              placeholder={connected ? "Куда и когда хотите поехать?" : "Подключаюсь..."}
              onChange={(e) => setDraft(e.target.value)}
              disabled={!connected}
            />
            <Button type="submit" disabled={!connected || busy || !draft.trim()}>
              Отправить
            </Button>
          </form>
        </section>

        <PlanPanel plan={plan} sessionId={sessionId} />
      </main>
    </div>
  );
}
