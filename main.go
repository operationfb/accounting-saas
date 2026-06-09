package main

// main.go
// =============================================================================
// Entry point for the expense service.
//
// This file's only job is to:
//   1. Read configuration (database URL, port) from environment variables
//   2. Open the database connection pool
//   3. Build the Server and start listening
//
// Keeping main.go minimal means it's easy to understand at a glance what the
// programme does when it starts. All the real wiring lives in server.go.
// =============================================================================

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	// This import path matches the `out` directory in sqlc.yaml.
	// After running `sqlc generate`, the generated files live here.
	expenses "github.com/operationfb/accounting-saas/db/expenses"
)

func main() {
	_ = godotenv.Load()
	// -------------------------------------------------------------------------
	// 1. Read config from environment variables.
	//    Never hardcode credentials. In local development you set these in a
	//    .env file (loaded by a tool like direnv or godotenv). In production
	//    they come from GCP Secret Manager / Cloud Run environment config.
	// -------------------------------------------------------------------------
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// log.Fatal prints the message then calls os.Exit(1) — terminates the
		// programme immediately. Appropriate here because there's no point
		// starting without a database.
		log.Fatal("DATABASE_URL environment variable is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // sensible default for local development
	}

	// -------------------------------------------------------------------------
	// 2. Open a connection pool to PostgreSQL using pgxpool.
	//
	//    Why a pool and not a single connection?
	//    An HTTP server handles many concurrent requests. Each request needs
	//    its own database connection. pgxpool manages a set of connections
	//    (default max: 4 per CPU core) and lends them out to goroutines as
	//    needed. Without a pool, concurrent requests would queue waiting for
	//    one connection.
	//
	//    context.Background() is the root context — it never cancels.
	//    We use it here because this is startup code; there's no request
	//    deadline to inherit yet.
	// -------------------------------------------------------------------------
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("unable to connect to database: %v", err)
	}
	// defer schedules pool.Close() to run when main() returns.
	// This cleanly closes all connections when the server shuts down.
	defer pool.Close()

	// Ping the database so we fail fast at startup if the connection is wrong,
	// rather than discovering the problem on the first real request.
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("database ping failed: %v", err)
	}
	log.Println("database connection established")

	// -------------------------------------------------------------------------
	// 3. Build the application layer.
	//
	//    expenses.New(pool) is the sqlc-generated constructor. It returns a
	//    *Queries struct that wraps the pool and knows how to run every query
	//    in query.sql.go.
	//
	//    NewExpenseService wraps *Queries with business logic (validation,
	//    transactions, etc.) — defined in expense_service.go.
	//
	//    NewServer wires the service into a Gin router — defined in server.go.
	// -------------------------------------------------------------------------
	queries := expenses.New(pool)
	service := NewExpenseService(pool, queries)
	server := NewServer(service)

	// -------------------------------------------------------------------------
	// 4. Start the HTTP server.
	// -------------------------------------------------------------------------
	log.Printf("server listening on :%s", port)
	if err := server.Run(":" + port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
