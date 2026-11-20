/**
 * バンドル済み OpenAPI (JSON) の `components.schemas` を抽出し、TypeScript
 * モジュールとして書き出す。
 *
 * docs/api の OpenAPI を単一ソースとし、クライアント側 (client/lib/api) の
 * 「型 (json-schema-to-ts の FromSchema)」を 1 枚の component schema から得る
 * ための成果物を生成する。Go サーバの型は同じ docs/api から oapi-codegen が
 * 生成する。生成後はルートの oxfmt (pnpm exec oxfmt) で整形してコミットする。
 *
 * usage: emit-server-schemas.ts <input api.json> <output .ts>
 */
import { mkdir, readFile, writeFile } from "node:fs/promises";
import * as path from "node:path";

const [inputPath, outputPath] = process.argv.slice(2);
if (!inputPath || !outputPath) {
  console.error("usage: emit-server-schemas.ts <input api.json> <output .ts>");
  process.exit(1);
}

const spec = JSON.parse(await readFile(inputPath, "utf8"));
const schemas = spec?.components?.schemas ?? {};

const header = `// This file was auto-generated from the OpenAPI definition in docs/api.
// Do not make direct changes to the file. Regenerate with:
//   npm --prefix ./docs/api run bundle:json
//   npm --prefix ./docs/api run emit-server-schemas -- ../client/lib/api/internal-schemas.ts
//   pnpm exec oxfmt client/lib/api/internal-schemas.ts
//
// docs/api の component schema (= JSON Schema)。クライアントはこれ 1 枚から
// 型 (json-schema-to-ts の FromSchema) を得る (Go サーバの型は oapi-codegen が
// 同じ docs/api から生成する)。
`;

const body = `export const internalSchemas = ${JSON.stringify(schemas, null, 2)} as const;\n`;

await mkdir(path.dirname(outputPath), { recursive: true });
await writeFile(outputPath, `${header}\n${body}`);
console.log(`emitted: ${outputPath} (${Object.keys(schemas).length} schemas)`);
