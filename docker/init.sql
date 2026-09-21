-- Bootstrap schema for the barrage demo stack.
-- Runs once, when the postgres volume is first created
-- (mounted at /docker-entrypoint-initdb.d). It creates the tables the
-- demoserver queries directly, so the app works before bulk seeding.
-- Bulk data goes in via the one-shot seeddb service (cmd/seeddb).

CREATE TABLE IF NOT EXISTS products (
    id    serial PRIMARY KEY,
    name  text NOT NULL,
    price numeric(10,2) NOT NULL
);

INSERT INTO products (name, price) VALUES
    ('widget', 9.99),
    ('gadget', 19.99),
    ('gizmo',  29.99);

CREATE TABLE IF NOT EXISTS orders (
    id         serial PRIMARY KEY,
    customer   text NOT NULL,
    amount     numeric(10,2) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_orders_created_at ON orders (created_at);