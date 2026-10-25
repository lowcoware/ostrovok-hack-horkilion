// Command migrate applies the database migrations and exits. Compose runs it
// once before the services start.
package main

import (
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"horkilion/internal/platform"
	"horkilion/migrations"
)

func main() {
	platform.NewLogger("migrate")

	db, err := goose.OpenDBWithDriver("pgx", platform.MustEnv("DATABASE_URL"))
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		slog.Error("set dialect", "err", err)
		os.Exit(1)
	}
	if err := goose.Up(db, "."); err != nil {
		slog.Error("apply migrations", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")
}
