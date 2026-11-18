// github-core-clone-01 — node22 entrypoint: launcher for the forge Go binary.
//
// The kit mounts the entry read-only at /bot and the checkout may be
// noexec, so the launcher tries, in order:
//   1. exec ./forge directly
//   2. copy ./forge to a temp dir and exec it there
//   3. load ./forge into an anonymous memfd (via python3, present in the
//      sandbox image) and exec /proc/self/fd/N
// The binary replaces the child process's image, so signals and the exit
// code pass through unchanged. Only stdlib (node:*) is used.
import { spawn } from 'node:child_process';
import { chmodSync, copyFileSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const BIN = join(HERE, 'forge');
const ARGS = process.argv.slice(2);

const MEMFD_SCRIPT = `import os, sys
BIN = sys.argv[1]
fd = os.memfd_create("forge", 0)
with open(BIN, "rb") as f:
    while True:
        chunk = f.read(1024 * 1024)
        if not chunk:
            break
        view = memoryview(chunk)
        while view:
            n = os.write(fd, view)
            view = view[n:]
os.execv("/proc/self/fd/%d" % fd, ["/proc/self/fd/%d" % fd] + sys.argv[2:])
`;

interface Rung {
  cmd: string;
  args: string[];
}

function rungs(): Rung[] {
  const out: Rung[] = [{ cmd: BIN, args: ARGS }];
  // Rung 2: copy to a temp dir (read-only / noexec checkout).
  try {
    const dir = mkdtempSync(join(tmpdir(), 'forge-'));
    const dest = join(dir, 'forge');
    copyFileSync(BIN, dest);
    chmodSync(dest, 0o755);
    out.push({ cmd: dest, args: ARGS });
  } catch {
    // fall through to the memfd rung
  }
  // Rung 3: memfd via python3.
  try {
    const dir = mkdtempSync(join(tmpdir(), 'forge-'));
    const script = join(dir, 'memfd.py');
    writeFileSync(script, MEMFD_SCRIPT);
    out.push({ cmd: 'python3', args: [script, BIN, ...ARGS] });
  } catch {
    // no more rungs
  }
  return out;
}

function attempt(list: Rung[], i: number): void {
  if (i >= list.length) {
    process.stderr.write('forge: could not execute the binary\n');
    process.exit(1);
  }
  const child = spawn(list[i].cmd, list[i].args, { stdio: 'inherit' });
  let settled = false;
  child.on('error', () => {
    if (!settled) {
      settled = true;
      attempt(list, i + 1);
    }
  });
  child.on('exit', (code, signal) => {
    if (settled) return;
    settled = true;
    if (signal) {
      try {
        process.kill(process.pid, signal);
      } catch {
        process.exit(1);
      }
      return;
    }
    process.exit(code ?? 1);
  });
}

attempt(rungs(), 0);
