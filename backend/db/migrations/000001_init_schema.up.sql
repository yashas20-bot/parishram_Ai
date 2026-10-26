-- 000001_init_schema.up.sql
-- Parishram AI Database Schema Migration (Hardened Foundation)

-- 1. Users Table
CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    name VARCHAR(255) NOT NULL,
    spending_limit BIGINT NOT NULL DEFAULT 0 CHECK (spending_limit >= 0), -- Minor currency units (e.g., paise/cents)
    currency VARCHAR(3) NOT NULL DEFAULT 'INR' CHECK (currency ~ '^[A-Z]{3}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 2. Budgets Table
CREATE TABLE IF NOT EXISTS budgets (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    monthly_limit BIGINT NOT NULL CHECK (monthly_limit > 0), -- Minor currency units
    alert_threshold_percent INT NOT NULL DEFAULT 80 CHECK (alert_threshold_percent >= 1 AND alert_threshold_percent <= 100),
    currency VARCHAR(3) NOT NULL DEFAULT 'INR' CHECK (currency ~ '^[A-Z]{3}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_budgets_user_id UNIQUE (user_id)
);

-- 3. Expenses Table
CREATE TABLE IF NOT EXISTS expenses (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category VARCHAR(64) NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0), -- Minor currency units
    currency VARCHAR(3) NOT NULL DEFAULT 'INR' CHECK (currency ~ '^[A-Z]{3}$'),
    description TEXT NOT NULL DEFAULT '',
    spent_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_expenses_user_spent_at ON expenses(user_id, spent_at DESC);

-- 4. Purchases Table (Proposals & Risk Checks)
CREATE TABLE IF NOT EXISTS purchases (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    merchant_name VARCHAR(128) NOT NULL,
    item_name VARCHAR(255) NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0), -- Minor currency units
    currency VARCHAR(3) NOT NULL DEFAULT 'INR' CHECK (currency ~ '^[A-Z]{3}$'),
    status VARCHAR(32) NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'reviewed', 'rejected', 'approved', 'abandoned')),
    risk_score INT NOT NULL DEFAULT 0 CHECK (risk_score >= 0 AND risk_score <= 100),
    risk_factors TEXT NOT NULL DEFAULT '[]',
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_purchases_user_status ON purchases(user_id, status);

-- 5. Payments Table (Authoritative State Machine)
CREATE TABLE IF NOT EXISTS payments (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purchase_id BIGINT REFERENCES purchases(id) ON DELETE SET NULL,
    paypal_order_id VARCHAR(64) NOT NULL UNIQUE,
    amount BIGINT NOT NULL CHECK (amount > 0), -- Minor currency units
    currency VARCHAR(3) NOT NULL DEFAULT 'INR' CHECK (currency ~ '^[A-Z]{3}$'),
    status VARCHAR(32) NOT NULL DEFAULT 'proposed' CHECK (status IN (
        'proposed',
        'awaiting_approval',
        'approved',
        'capture_pending',
        'completed',
        'failed',
        'cancelled',
        'requires_reconciliation'
    )),
    capture_id VARCHAR(64),
    failure_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_payments_user_status ON payments(user_id, status);
CREATE INDEX IF NOT EXISTS idx_payments_paypal_order_id ON payments(paypal_order_id);

-- 6. Payment Events Table (Idempotent Webhook Log)
CREATE TABLE IF NOT EXISTS payment_events (
    id BIGSERIAL PRIMARY KEY,
    payment_id BIGINT REFERENCES payments(id) ON DELETE SET NULL,
    paypal_order_id VARCHAR(64) NOT NULL,
    paypal_event_id VARCHAR(64) NOT NULL UNIQUE, -- Crucial for webhook delivery idempotency
    event_type VARCHAR(64) NOT NULL,
    event_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(32) NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'processed', 'ignored', 'error')),
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_payment_events_order_type ON payment_events(paypal_order_id, event_type);

-- 7. Audit Logs Table (Tamper-Resistant Operations Trail)
CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action VARCHAR(64) NOT NULL,
    entity_type VARCHAR(64) NOT NULL,
    entity_id VARCHAR(64) NOT NULL,
    details TEXT NOT NULL DEFAULT '{}',
    ip_address VARCHAR(45) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_user_created ON audit_logs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_entity ON audit_logs(entity_type, entity_id);

-- 8. Support Drafts Table (Refund/Dispute Preparation)
CREATE TABLE IF NOT EXISTS support_drafts (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    payment_id BIGINT REFERENCES payments(id) ON DELETE SET NULL,
    request_type VARCHAR(32) NOT NULL CHECK (request_type IN ('refund', 'dispute', 'inquiry')),
    subject VARCHAR(255) NOT NULL,
    draft_body TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'reviewed', 'sent', 'closed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_support_drafts_user_status ON support_drafts(user_id, status);

-- 9. Row Level Security (RLS) Enablement
-- Enabling RLS without public policies prevents unauthorized direct client access via PostgREST/Supabase API.
-- The Go backend connects via direct DB connection using the table owner role (postgres), bypassing RLS.
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE budgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE expenses ENABLE ROW LEVEL SECURITY;
ALTER TABLE purchases ENABLE ROW LEVEL SECURITY;
ALTER TABLE payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE support_drafts ENABLE ROW LEVEL SECURITY;
