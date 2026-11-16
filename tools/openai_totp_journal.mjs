// Durable encrypted recovery journal. Secrets arrive on stdin, never argv or logs.
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { fileURLToPath } from 'node:url'

function syncDirectory(dir) {
  const fd = fs.openSync(dir, 'r')
  try { fs.fsyncSync(fd) } finally { fs.closeSync(fd) }
}
export function openJournal(root) {
  fs.mkdirSync(root, { recursive: true, mode: 0o700 })
  const info = fs.lstatSync(root)
  if (!info.isDirectory() || info.isSymbolicLink() || (info.mode & 0o077)) throw Error('unsafe_journal_directory')
  const keyPath = path.join(root, 'key.bin')
  // Never silently regenerate a missing key when encrypted records exist.
  if (!fs.existsSync(keyPath)) {
    if (fs.readdirSync(root).some(x => x.endsWith('.sealed'))) throw Error('journal_key_missing')
    try {
      const fd = fs.openSync(keyPath, 'wx', 0o600)
      try { fs.writeFileSync(fd, crypto.randomBytes(32)); fs.fsyncSync(fd) } finally { fs.closeSync(fd) }
      syncDirectory(root)
    } catch (error) { if (error.code !== 'EEXIST') throw error }
  }
  const keyInfo = fs.lstatSync(keyPath)
  if (!keyInfo.isFile() || keyInfo.isSymbolicLink() || (keyInfo.mode & 0o077)) throw Error('unsafe_journal_key')
  const key = fs.readFileSync(keyPath)
  if (key.length !== 32) throw Error('invalid_journal_key')
  function read(id) {
    const raw = fs.readFileSync(path.join(root, `${id}.sealed`))
    const decipher = crypto.createDecipheriv('aes-256-gcm', key, raw.subarray(0, 12))
    decipher.setAAD(Buffer.from(String(id)))
    decipher.setAuthTag(raw.subarray(12, 28))
    return JSON.parse(Buffer.concat([decipher.update(raw.subarray(28)), decipher.final()]).toString('utf8'))
  }
  function write(record) {
    const id = record.task_id
    if (!Number.isSafeInteger(id) || id <= 0) throw Error('invalid_task')
    const target = path.join(root, `${id}.sealed`)
    if (fs.existsSync(target)) {
      const old = read(id)
      if (old.candidate && old.candidate !== record.candidate) throw Error('candidate_overwrite_refused')
      for (const key of ['factor_id', 'session_id', 'previous_factor_id', 'email', 'previous']) {
        if (old[key] && old[key] !== record[key]) throw Error('provenance_overwrite_refused')
      }
    }
    const nonce = crypto.randomBytes(12)
    const cipher = crypto.createCipheriv('aes-256-gcm', key, nonce)
    cipher.setAAD(Buffer.from(String(id)))
    const data = Buffer.concat([cipher.update(JSON.stringify(record), 'utf8'), cipher.final()])
    const tmp = path.join(root, `.${id}-${crypto.randomUUID()}`)
    const fd = fs.openSync(tmp, 'wx', 0o600)
    try { fs.writeFileSync(fd, Buffer.concat([nonce, cipher.getAuthTag(), data])); fs.fsyncSync(fd) } finally { fs.closeSync(fd) }
    fs.renameSync(tmp, target)
    syncDirectory(root)
  }
  return { write, read, list: () => fs.readdirSync(root).filter(x => /^\d+\.sealed$/.test(x)).map(x => read(Number(x.split('.')[0]))) }
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const raw = fs.readFileSync(0, 'utf8')
    if (raw.length > 1024 * 1024) throw Error('oversized_input')
    const input = JSON.parse(raw)
    const journal = openJournal(process.env.OPENAI_TOTP_JOURNAL_DIR)
    let result = { ok: true }
    if (input.action === 'write') journal.write(input.record)
    else if (input.action === 'list') result = journal.list()
    else if (input.action === 'acknowledge') {
      const record = journal.read(input.task_id)
      journal.write({ ...record, database_acknowledged: true })
    }
    else if (input.action !== 'check') throw Error('invalid_action')
    process.stdout.write(JSON.stringify(result))
  } catch { process.stderr.write('TOTP recovery journal unavailable\n'); process.exitCode = 1 }
}
