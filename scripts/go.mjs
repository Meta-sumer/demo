// Runs the Go toolchain inside server-go/ for the npm scripts.
//
// Go is looked up on PATH first, then at the per-user install location
// (%USERPROFILE%\sdk\go), so `npm run dev` works on a machine where Go was
// unpacked without touching PATH.
import { spawn, spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { homedir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const exe = process.platform === 'win32' ? 'go.exe' : 'go';

function findGo() {
  const onPath = spawnSync(exe, ['version'], { stdio: 'ignore' });
  if (onPath.status === 0) return exe;
  const local = join(homedir(), 'sdk', 'go', 'bin', exe);
  if (existsSync(local)) return local;
  console.error('Go was not found. Install Go 1.27+ or unpack it to ' + join(homedir(), 'sdk', 'go') + '.');
  process.exit(1);
}

const child = spawn(findGo(), process.argv.slice(2), { cwd: join(root, 'server-go'), stdio: 'inherit' });
child.on('exit', (code, signal) => process.exit(signal ? 1 : (code ?? 0)));
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => child.kill(sig));
