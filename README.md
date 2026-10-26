# Parishram AI (परिश्रम AI)
> **Smarter Decisions. Safer Payments.**

Parishram AI is an AI-powered payment intelligence and consumer financial assistant built for the **PayPal AI Hackathon**.

The fundamental principle:
> **AI provides intelligence, explanations, and recommendations, while the backend deterministically enforces financial rules and the user retains absolute control over payment approval.**

---

## 🏛️ System Architecture

```
Routes ──► Controllers ──► db.Querier ──► SQLC-Generated Code ──► PostgreSQL (Supabase)
                              │
                    ┌─────────┴─────────┐
                    ▼                   ▼
           Finance Engine         PayPal Sandbox
       (Integer Deterministic)      (Orders API)
```

- **Backend**: Go (`gin-gonic/gin`, `jackc/pgx/v5`)
- **Database**: PostgreSQL (Supabase with Row Level Security & connection pooling)
- **Type-Safe Access**: Hand-authored `.sql` queries with `sqlc` code generation
- **Financial Arithmetic**: Pure integer-only minor units (`BIGINT` / `int64`) using `math/bits` for overflow-safe utilization calculations

---

## 🚀 Key Modules Completed

- **Phase 1: Database & Backend Foundation**
  - Live PostgreSQL migrations (`000001_init_schema.up.sql`) with numeric checks, ASCII regex currency enforcement, RLS, and PayPal webhook idempotency (`paypal_event_id UNIQUE`).
  - Gin HTTP routing, graceful server shutdown, and live Supabase health check endpoint.
  - SQLC type-safe database query bindings.
- **Phase 2.1: Deterministic Finance Engine**
  - Integer-only utilization `floor((spent / limit) * 100)` preventing 64-bit overflow via 128-bit unsigned arithmetic (`math/bits`).
  - Strict 3-letter currency verification, matching checks, and budget impact evaluations.
  - Pre-payment risk evaluation without false claims of autonomous fraud detection.

---

## 🛠️ Local Development

### Prerequisites
- Go 1.22+
- PostgreSQL or Supabase instance
- `sqlc` v1.26+
- `golang-migrate`

### Environment Configuration
Copy `.env.example` to `.env`:
```bash
cp backend/.env.example backend/.env
```

### Running Migrations
```powershell
cd backend
.\scripts\migrate.ps1 -Action up
```

### Running Tests
```powershell
cd backend
go test -v ./...
```

### Running the Server
```powershell
cd backend
go run cmd/server/main.go
```
Health endpoint: `http://localhost:8080/health`
