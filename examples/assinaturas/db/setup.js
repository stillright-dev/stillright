// Applies a schema file to $DATABASE_URL. Usage: node db/setup.js <file.sql>
import { readFile } from 'node:fs/promises';
import pg from 'pg';

const file = process.argv[2];
if (!file || !process.env.DATABASE_URL) {
  console.error('usage: DATABASE_URL=postgres://... node db/setup.js <schema.sql>');
  process.exit(2);
}
const client = new pg.Client({ connectionString: process.env.DATABASE_URL });
await client.connect();
await client.query(await readFile(file, 'utf8'));
await client.end();
console.log(`applied ${file}`);
