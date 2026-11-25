// `npm run build` runs this after `vite build`: node scripts/precompress/cli.ts [dir]
// (default: the embedded build directory, internal/webui/dist).
import path from 'node:path';
import { precompress } from './precompress.ts';

const dir = process.argv[2] ?? path.join(import.meta.dirname, '../../../internal/webui/dist');
const done = precompress(dir);
console.log(`precompress: ${done.length} files gzipped in ${dir}`);
