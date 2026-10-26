// Command agent consumes user messages from AGENT_TURNS and executes agent turns.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	catalogv1 "horkilion/gen/catalog/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/agent"
	"horkilion/internal/analytics"
	"horkilion/internal/platform"
	"horkilion/internal/tracing"
)

const maxDeliver = 5

func main() {
	platform.HandleHealthcheck()
	platform.NewLogger("agent")
	if err := run(); err != nil {
		slog.Error("agent failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := platform.SignalContext()
	defer stop()

	rules, err := agent.LoadRules(platform.Env("RULES_PATH", "config/rules.yaml"))
	if err != nil {
		return err
	}
	llmCfg, err := agent.LLMConfigFromEnv()
	if err != nil {
		return err
	}

	shutdownTracing, err := tracing.Setup(ctx, tracing.Config{
		Service:   "agent",
		Host:      platform.Env("LANGFUSE_HOST", "http://langfuse-web:3000"),
		PublicKey: os.Getenv("LANGFUSE_PUBLIC_KEY"),
		SecretKey: os.Getenv("LANGFUSE_SECRET_KEY"),
	})
	if err != nil {
		return err
	}
	defer func() {
		// Flush buffered spans on shutdown.
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(flushCtx)
	}()

	db, err := platform.ConnectPostgres(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	nc, err := platform.ConnectNATS("agent")
	if err != nil {
		return err
	}
	defer nc.Drain()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	if err := platform.EnsureStreams(ctx, js); err != nil {
		return err
	}

	invConn, err := platform.DialGRPC(platform.Env("INVENTORY_ADDR", "inventory:9000"))
	if err != nil {
		return err
	}
	defer invConn.Close()

	llm := agent.NewLLM(llmCfg)
	if llm == nil {
		slog.Warn("LLM_API_KEY is not set, running with the rule router and template replies")
	}
	an := analytics.New(os.Getenv("CLICKHOUSE_URL"), platform.Env("CLICKHOUSE_USER", "default"), os.Getenv("CLICKHOUSE_PASSWORD"))
	ag := agent.New(agent.NewStore(db), llm, rules, agent.Suppliers{
		Lodging:   lodgingv1.NewLodgingServiceClient(invConn),
		Catalog:   catalogv1.NewCatalogServiceClient(invConn),
		Transport: transportv1.NewTransportServiceClient(invConn),
		Transfer:  transferv1.NewTransferServiceClient(invConn),
	}, nc, an)

	// impulse: MaxAckPending 1 serializes all turns so one session's turns never race, replace with a per-session lock in NATS KV when one worker stops keeping up.
	cons, err := js.CreateOrUpdateConsumer(ctx, platform.StreamAgentTurns, jetstream.ConsumerConfig{
		Durable:       "agent",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    maxDeliver,
		MaxAckPending: 1,
	})
	if err != nil {
		return err
	}

	platform.StartOps(ctx, platform.PostgresReady(db), platform.NATSReady(nc))
	cc, err := cons.Consume(func(msg jetstream.Msg) { handle(ctx, ag, msg) })
	if err != nil {
		return err
	}
	// Graceful shutdown: stop taking new turns, let the current one finish.
	<-ctx.Done()
	cc.Drain()
	<-cc.Closed()
	return nil
}

func handle(ctx context.Context, ag *agent.Agent, msg jetstream.Msg) {
	var turn platform.TurnMessage
	if err := json.Unmarshal(msg.Data(), &turn); err != nil || turn.SessionID == "" || turn.MessageID == "" {
		slog.Error("malformed turn message, dropping", "err", err)
		_ = msg.Term()
		return
	}
	log := slog.With("session_id", turn.SessionID, "message_id", turn.MessageID)

	// The turn must not be cut off by shutdown midway; it gets its own deadline.
	turnCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 110*time.Second)
	defer cancel()
	err := ag.HandleTurn(turnCtx, turn)
	if err == nil {
		_ = msg.Ack()
		return
	}

	meta, _ := msg.Metadata()
	if meta != nil && meta.NumDelivered >= maxDeliver {
		// impulse: exhausted turns are terminated and logged, add a dead-letter stream when they need replaying.
		log.Error("turn failed for good", "err", err, "deliveries", meta.NumDelivered)
		ag.EmitError(turn, "Не получилось обработать сообщение. Попробуйте ещё раз чуть позже.")
		_ = msg.Term()
		return
	}
	log.Warn("turn failed, will retry", "err", err)
	_ = msg.NakWithDelay(3 * time.Second)
}
