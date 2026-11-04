-- Schema of the fixed version: the same tables, plus the constraints that
-- turn "this should never happen" into "this cannot happen".
DROP TABLE IF EXISTS emails_enviados, eventos_recebidos, pagamentos, assinaturas, clientes CASCADE;

CREATE TABLE clientes (
  id    integer PRIMARY KEY,
  nome  text NOT NULL,
  email text NOT NULL
);

CREATE TABLE assinaturas (
  id         serial PRIMARY KEY,
  cliente_id integer NOT NULL REFERENCES clientes (id),
  status     text NOT NULL,
  criada_em  timestamptz NOT NULL DEFAULT now()
);
-- FIX 1: at most one active subscription per customer.
CREATE UNIQUE INDEX assinaturas_uma_ativa ON assinaturas (cliente_id) WHERE status = 'ativa';

CREATE TABLE pagamentos (
  id            serial PRIMARY KEY,
  gateway_id    text NOT NULL UNIQUE,    -- FIX 1: one row per gateway payment
  cliente_id    integer NOT NULL REFERENCES clientes (id),
  assinatura_id integer REFERENCES assinaturas (id),
  valor         integer NOT NULL,
  status        text NOT NULL
);

CREATE TABLE eventos_recebidos (
  id           serial PRIMARY KEY,
  evento_id    text NOT NULL UNIQUE,     -- FIX 1: the event id is the idempotency key
  tipo         text NOT NULL,
  pagamento_id text NOT NULL,
  recebido_em  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE emails_enviados (
  id         serial PRIMARY KEY,
  cliente_id integer NOT NULL REFERENCES clientes (id),
  tipo       text NOT NULL,
  enviado_em timestamptz NOT NULL DEFAULT now(),
  UNIQUE (cliente_id, tipo)              -- FIX 3: claim the e-mail before sending it
);
