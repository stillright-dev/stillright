// Sends e-mail through an HTTP provider. In tests, point EMAIL_API_URL at the
// stillright proxy; nothing reaches a real provider.
const EMAIL_API_URL = process.env.EMAIL_API_URL ?? 'http://api.email.local';

export async function enviarEmail(para, template, dados) {
  const res = await fetch(`${EMAIL_API_URL}/send`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ para, template, dados }),
  });
  if (!res.ok) throw new Error(`e-mail provider answered ${res.status}`);
}
