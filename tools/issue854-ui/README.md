# Issue854 browser evidence

This harness renders the actual OpsErrorLogTable and OpsErrorDetailModal,
including DataTable's mobile cards, BaseDialog, translations, and Tailwind CSS.
It archives immutable Git revisions, renders baseline first, then candidate,
and intercepts only the synthetic detail API. No backend or provider is called.

Install frontend dependencies using the locked package manager. Provide an
installed Playwright or playwright-core ES module and Chromium executable:

```sh
ISSUE854_PLAYWRIGHT_MODULE=/absolute/path/to/playwright-core/index.mjs \
ISSUE854_CHROME=/absolute/path/to/chromium \
node tools/issue854-ui/capture.mjs "$PWD" /outside/repository/evidence \
  aa8e596e6d6f3e4648d6beba73b39b240dd5357a CANDIDATE_SHA
```

Use a new output directory per invocation. The manifest records source SHA/tree,
component/CSS/lockfile Git blobs, fixture/harness hashes, browser, UTC capture time,
viewport, PNG SHA256, and named assertions. Captures cover English/Chinese,
1440×1000 and 390×844, table/cards, HTTP detail, and WS detail/diagnostics.

The HTTP fixture preserves status200 + transport_error. The WS fixture matches
Ops recovery persistence: outer phase internal / owner platform / source gateway,
logical status409 and nested error.wire_status101, turn2, request_written=false,
outcome=failed. Its baseline rendering is a display comparison using a synthetic
candidate-shaped row, not a claim that baseline persisted that missing record.

The fixture contains no production data or credentials. Browser requests are
limited to loopback. PNG files and raw capture artifacts must stay outside Git;
upload screenshots as native PR attachments and verify authenticated downloads.
