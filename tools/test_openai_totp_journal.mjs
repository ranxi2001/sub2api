import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { openJournal } from './openai_totp_journal.mjs'

function setup(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'totp-journal-test-'))
  t.after(() => fs.rmSync(root, { recursive: true, force: true }))
  return root
}
test('encrypted candidate survives restart and never appears in plaintext files', t => {
  const root = setup(t)
  const record = { task_id: 7, candidate: 'SYNTHETIC-SECRET', previous: 'OLD-SECRET', password: 'fake-password' }
  openJournal(root).write(record)
  assert.deepEqual(openJournal(root).read(7), record)
  assert.equal(fs.statSync(path.join(root, '7.sealed')).mode & 0o777, 0o600)
  for (const name of fs.readdirSync(root)) {
    const text = fs.readFileSync(path.join(root, name)).toString()
    assert.ok(!text.includes(record.candidate) && !text.includes(record.password))
  }
})
test('an existing candidate cannot be overwritten by a retry', t => {
  const journal = openJournal(setup(t))
  journal.write({ task_id: 7, candidate: 'NEW' })
  assert.throws(() => journal.write({ task_id: 7, candidate: 'OTHER' }), /overwrite/)
  assert.equal(journal.read(7).candidate, 'NEW')
})
test('missing key is never regenerated over existing credentials', t => {
  const root = setup(t)
  openJournal(root).write({ task_id: 7, candidate: 'NEW' })
  fs.unlinkSync(path.join(root, 'key.bin'))
  assert.throws(() => openJournal(root), /key_missing/)
  assert.equal(fs.existsSync(path.join(root, 'key.bin')), false)
})
test('disk corruption fails authentication without returning guessed credentials', t => {
  const root = setup(t)
  openJournal(root).write({ task_id: 7, candidate: 'NEW' })
  const file = path.join(root, '7.sealed'), raw = fs.readFileSync(file)
  raw[raw.length-1] ^= 1; fs.writeFileSync(file, raw)
  assert.throws(() => openJournal(root).read(7))
})
test('unsafe directory permissions fail before writing credentials', t => {
  const root = setup(t); fs.chmodSync(root, 0o755)
  assert.throws(() => openJournal(root), /unsafe_journal_directory/)
})
test('CLI invoked through a current symlink executes journal operations', t => {
  const root = setup(t)
  const release = path.join(root, 'release with spaces')
  fs.mkdirSync(release)
  const script = 'openai_totp_journal.mjs'
  fs.copyFileSync(fileURLToPath(new URL(`./${script}`, import.meta.url)), path.join(release, script))
  const current = path.join(root, 'current')
  fs.symlinkSync(release, current, 'dir')
  const journal = path.join(root, 'recovery')
  const invoke = input => spawnSync(process.execPath, [path.join(current, script)], {
    input: JSON.stringify(input), encoding: 'utf8',
    env: { ...process.env, OPENAI_TOTP_JOURNAL_DIR: journal }
  })
  const check = invoke({ action: 'check' })
  assert.equal(check.status, 0, check.stderr)
  assert.deepEqual(JSON.parse(check.stdout), { ok: true })
  assert.equal(fs.statSync(path.join(journal, 'key.bin')).mode & 0o777, 0o600)
  const record = { task_id: 11, candidate: 'SYNTHETIC-SECRET' }
  const write = invoke({ action: 'write', record })
  assert.equal(write.status, 0, write.stderr)
  const list = invoke({ action: 'list' })
  assert.equal(list.status, 0, list.stderr)
  assert.deepEqual(JSON.parse(list.stdout), [record])
})

test('candidate provenance cannot be replaced or stripped while keeping the secret', t => {
  const journal = openJournal(setup(t));
  journal.write({task_id:7,candidate:'NEW',factor_id:'F2',session_id:'enrollment'});
  for (const change of [{factor_id:'F1'},{factor_id:''},{session_id:'other'}]) {
    assert.throws(()=>journal.write({task_id:7,candidate:'NEW',factor_id:'F2',session_id:'enrollment',...change}), /overwrite/);
  }
  assert.equal(journal.read(7).factor_id,'F2');
});
