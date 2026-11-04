// The buggy version. It works when the gateway delivers each webhook once,
// in order, one at a time, which is exactly what gateways do not promise.
import express from 'express';
import pg from 'pg';
import { enviarEmail } from './email.js';

const db = new pg.Pool({ connectionString: process.env.DATABASE_URL });
const app = express();
app.use(express.json());

app.get('/health', (_req, res) => res.json({ ok: true }));

app.post('/webhooks/pagamento', async (req, res) => {
  const ev = req.body;
  try {
    await db.query(
      'INSERT INTO eventos_recebidos (evento_id, tipo, pagamento_id) VALUES ($1, $2, $3)',
      [ev.id, ev.tipo, ev.pagamento_id],
    );

    if (ev.tipo === 'pagamento.confirmado') {
      await confirmarPagamento(ev);
    } else if (ev.tipo === 'pagamento.estornado') {
      await estornarPagamento(ev);
    }
    res.json({ ok: true });
  } catch (err) {
    console.error(err);
    res.status(500).json({ error: err.message });
  }
});

async function confirmarPagamento(ev) {
  // BUG 1: check, then insert. Two deliveries of the same event both see "no
  // active subscription" and both insert one. Nothing in the schema stops it.
  const { rows } = await db.query(
    "SELECT id FROM assinaturas WHERE cliente_id = $1 AND status = 'ativa'",
    [ev.cliente_id],
  );
  let assinaturaId = rows[0]?.id;
  if (!assinaturaId) {
    const nova = await db.query(
      "INSERT INTO assinaturas (cliente_id, status) VALUES ($1, 'ativa') RETURNING id",
      [ev.cliente_id],
    );
    assinaturaId = nova.rows[0].id;
  }

  await db.query(
    "INSERT INTO pagamentos (gateway_id, cliente_id, assinatura_id, valor, status) VALUES ($1, $2, $3, $4, 'pago')",
    [ev.pagamento_id, ev.cliente_id, assinaturaId, ev.valor],
  );

  // BUG 3: same check-then-act, on an outbound call this time.
  const enviados = await db.query(
    "SELECT 1 FROM emails_enviados WHERE cliente_id = $1 AND tipo = 'boas-vindas'",
    [ev.cliente_id],
  );
  if (enviados.rowCount === 0) {
    const { rows: [cliente] } = await db.query('SELECT email, nome FROM clientes WHERE id = $1', [ev.cliente_id]);
    await enviarEmail(cliente.email, 'boas-vindas', { nome: cliente.nome });
    await db.query("INSERT INTO emails_enviados (cliente_id, tipo) VALUES ($1, 'boas-vindas')", [ev.cliente_id]);
  }
}

async function estornarPagamento(ev) {
  // BUG 2: assumes the payment already exists. A refund delivered before (or
  // at the same time as) its payment updates nothing and still answers 200;
  // the payment that arrives next activates a subscription for money that
  // was already returned.
  const { rows } = await db.query(
    "UPDATE pagamentos SET status = 'estornado' WHERE gateway_id = $1 RETURNING assinatura_id",
    [ev.pagamento_id],
  );
  for (const { assinatura_id } of rows) {
    await db.query("UPDATE assinaturas SET status = 'cancelada' WHERE id = $1", [assinatura_id]);
  }
}

const port = process.env.PORT ?? 3000;
app.listen(port, () => console.log(`assinaturas (buggy) listening on :${port}`));
