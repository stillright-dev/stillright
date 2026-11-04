-- Schema of the buggy version: the tables a typical first version has,
-- without the constraints that would make duplicate deliveries harmless.
DROP TABLE IF EXISTS emails_enviados, eventos_recebidos, pagamentos, assinaturas, clientes CASCADE;

CREATE TABLE clientes (
  id    integer PRIMARY KEY,
  nome  text NOT NULL,
  email text NOT NULL
);

CREATE TABLE assinaturas (
  id         serial PRIMARY KEY,
  cliente_id integer NOT NULL REFERENCES clientes (id),
  status     text NOT NULL,              -- ativa | cancelada
  criada_em  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE pagamentos (
  id            serial PRIMARY KEY,
  gateway_id    text NOT NULL,           -- the payment id sent by the gateway
  cliente_id    integer NOT NULL REFERENCES clientes (id),
  assinatura_id integer REFERENCES assinaturas (id),
  valor         integer NOT NULL,        -- cents
  status        text NOT NULL            -- pago | estornado
);

-- Every webhook delivery, as received.
CREATE TABLE eventos_recebidos (
  id           serial PRIMARY KEY,
  evento_id    text NOT NULL,
  tipo         text NOT NULL,
  pagamento_id text NOT NULL,
  recebido_em  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE emails_enviados (
  id         serial PRIMARY KEY,
  cliente_id integer NOT NULL REFERENCES clientes (id),
  tipo       text NOT NULL,
  enviado_em timestamptz NOT NULL DEFAULT now()
);
