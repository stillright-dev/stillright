// The fixed version. Same endpoint, same payloads; three changes:
//
//   1. Idempotency: the event id is unique, so a redelivered event is a no-op,
//      and unique constraints back up every "only one" rule.
//   2. Ordering: events for one customer are processed one at a time (row
//      lock on the customer), and a payment checks whether its refund
//      already arrived.
//   3. E-mail: the send is claimed with INSERT ... ON CONFLICT inside the
//      transaction and made only after commit, by whoever won the claim.
//      (A production system would hand it to an outbox worker that retries.)
import express from 'express';
import pg from 'pg';
import { enviarEmail } from '../email.js';

const db = new pg.Pool({ connectionString: process.env.DATABASE_URL });
const app = express();
app.use(express.json());

app.get('/health', (_req, res) => res.json({ ok: true }));

app.post('/webhooks/pagamento', async (req, res) => {
  const ev = req.body;
  const client = await db.connect();
  let email = null;
  try {
    await client.query('BEGIN');
    // FIX 2: serialize events of the same customer.
    await client.query('SELECT 1 FROM clientes WHERE id = $1 FOR UPDATE', [ev.cliente_id]);

    // FIX 1: a redelivered event changes nothing.
    const novo = await client.query(
      `INSERT INTO eventos_recebidos (evento_id, tipo, pagamento_id) VALUES ($1, $2, $3)
       ON CONFLICT (evento_id) DO NOTHING RETURNING id`,
      [ev.id, ev.tipo, ev.pagamento_id],
    );
    if (novo.rowCount === 1) {
      if (ev.tipo === 'pagamento.confirmado') {
        email = await confirmarPagamento(client, ev);
      } else if (ev.tipo === 'pagamento.estornado') {
        await estornarPagamento(client, ev);
      }
    }
    await client.query('COMMIT');
  } catch (err) {
    await client.query('ROLLBACK');
    console.error(err);
    return res.status(500).json({ error: err.message });
  } finally {
    client.release();
  }

  if (email) {
    try {
      await enviarEmail(email.para, 'boas-vindas', { nome: email.nome });
    } catch (err) {
      console.error('welcome e-mail failed (an outbox worker would retry):', err.message);
    }
  }
  res.json({ ok: true });
});

async function confirmarPagamento(client, ev) {
  // FIX 2: the refund may have arrived first.
  const estorno = await client.query(
    "SELECT 1 FROM eventos_recebidos WHERE pagamento_id = $1 AND tipo = 'pagamento.estornado'",
    [ev.pagamento_id],
  );
  if (estorno.rowCount > 0) {
    await client.query(
      `INSERT INTO pagamentos (gateway_id, cliente_id, valor, status) VALUES ($1, $2, $3, 'estornado')
       ON CONFLICT (gateway_id) DO NOTHING`,
      [ev.pagamento_id, ev.cliente_id, ev.valor],
    );
    return null;
  }

  // FIX 1: insert-or-reuse, guarded by the partial unique index.
  await client.query(
    `INSERT INTO assinaturas (cliente_id, status) VALUES ($1, 'ativa')
     ON CONFLICT (cliente_id) WHERE status = 'ativa' DO NOTHING`,
    [ev.cliente_id],
  );
  const { rows: [assinatura] } = await client.query(
    "SELECT id FROM assinaturas WHERE cliente_id = $1 AND status = 'ativa'",
    [ev.cliente_id],
  );
  await client.query(
    `INSERT INTO pagamentos (gateway_id, cliente_id, assinatura_id, valor, status) VALUES ($1, $2, $3, $4, 'pago')
     ON CONFLICT (gateway_id) DO NOTHING`,
    [ev.pagamento_id, ev.cliente_id, assinatura.id, ev.valor],
  );

  // FIX 3: only the delivery that wins this claim sends the e-mail.
  const claim = await client.query(
    `INSERT INTO emails_enviados (cliente_id, tipo) VALUES ($1, 'boas-vindas')
     ON CONFLICT (cliente_id, tipo) DO NOTHING RETURNING id`,
    [ev.cliente_id],
  );
  if (claim.rowCount === 0) return null;
  const { rows: [cliente] } = await client.query('SELECT email, nome FROM clientes WHERE id = $1', [ev.cliente_id]);
  return { para: cliente.email, nome: cliente.nome };
}

async function estornarPagamento(client, ev) {
  const { rows } = await client.query(
    "UPDATE pagamentos SET status = 'estornado' WHERE gateway_id = $1 RETURNING assinatura_id",
    [ev.pagamento_id],
  );
  for (const { assinatura_id } of rows) {
    await client.query("UPDATE assinaturas SET status = 'cancelada' WHERE id = $1", [assinatura_id]);
  }
}

const port = process.env.PORT ?? 3000;
app.listen(port, () => console.log(`assinaturas (fixed) listening on :${port}`));
