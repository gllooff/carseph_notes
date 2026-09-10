# DESIGN — Carseph Notes

A self-hosted web app for storing personal notes and documents.

* **What**: Multi-user, private note store. Users manage three kinds of objects:
  * **Markdown notes** — create, edit, view (rendered), delete.
  * **Images** — upload, view (inline / lightbox), delete.
  * **PDFs** — upload, view (embedded PDF.js), delete.
  * Organization: **folders** + per-item **tags** (notes, images, and PDFs all taggable).
* **Who**: Multiple registered users; **strict per-user isolation** — one account can never see another's data.
* **Auth**: **Passkeys (WebAuthn) only** — no passwords. Browser verifies identity via fingerprint / FaceID / security key.
* **Stack**: HTML + JS + CSS (no framework, no build step) · Golang · SQLite · local file system.
* **Deployment**: Single Go binary + systemd on the existing DigitalOcean Droplet (`137.184.22.193`, Debian 13, 1 vCPU / 1 GB), behind the already-running Caddy, at `notes.jys-reality.win`.
* **Local verification**: Docker Compose.

---

## 1. Architecture

```
┌──────────────────────────────────────────────────────────┐
│ Browser (vanilla HTML/JS/CSS, ES modules, no build step) │
│  · Markdown editor + renderer (marked + DOMPurify)       │
│  · PDF viewer (PDF.js)        · Image viewer             │
│  · WebAuthn client (@simplewebauthn/browser)             │
└──────────────┬───────────────────────────────────────────┘
               │ HTTPS (Let's Encrypt, auto)
┌──────────────▼───────────────┐        Droplet 137.184.22.193
│ Caddy 2.11 (already running) │
│  notes.jys-reality.win →     │
│  reverse_proxy 127.0.0.1:8084│
└──────────────┬───────────────┘
               │ plain HTTP on loopback
┌──────────────▼───────────────────────────────────────────┐
│ Go server (net/http, single static binary)               │
│  · serves embedded static assets (go:embed)              │
│  · /api/auth/*   WebAuthn registration/login, sessions   │
│  · /api/notes/*  markdown CRUD                           │
│  · /api/files/*  image/PDF upload, serve, delete         │
│                                                          │
│  ┌───────────────┐      ┌─────────────────────────────┐  │
│  │ SQLite (WAL)  │      │ File system (DATA_DIR)      │  │
│  │  users        │      │  data/notes/{uid}/{id}.md   │  │
│  │  passkeys     │      │  data/files/{uid}/{id}.bin  │  │
│  │  sessions     │      │   (images & PDFs)           │  │
│  │  notes/files  │      │  data/carseph.db            │  │
│  │  metadata     │      │  data/carseph.db-wal/-shm   │  │
│  └───────────────┘      └─────────────────────────────┘  │
└──────────────────────────────────────────────────────────┘
```

**Why this shape**

* One static Go binary (assets embedded) — trivial to deploy with systemd, no Docker on the Droplet, matches the other services on this host (`recipes`, `health`, … run the same way).
* SQLite in WAL mode is more than enough for a personal-scale app and keeps the 1 GB-RAM Droplet happy.
* Blobs stay on the file system (as required), SQLite holds metadata/indexes.
* Port **8084** — the existing Caddyfile already occupies 8080–8083 and 8090.

## 2. Tech stack & libraries

| Layer | Choice | Notes |
|---|---|---|
| Language | Go ≥ 1.24 | stdlib `net/http` routing with method+path patterns |
| SQLite driver | `modernc.org/sqlite` | pure Go, **no CGO** → `CGO_ENABLED=0` cross-compile; enable WAL |
| WebAuthn server | `github.com/go-webauthn/webauthn` | de-facto Go library |
| Static assets | `go:embed` | single binary; same binary runs in Docker locally |
| Frontend | Vanilla JS ES modules, CSS | no framework, no npm, no bundler |
| Markdown render | `marked.js` (vendored) | output sanitized with `DOMPurify` — never render raw HTML |
| PDF view | `PDF.js` (vendored) | Firefox's PDF viewer; range requests supported |
| WebAuthn client | `@simplewebauthn/browser` (vendored ESM) | handles base64url marshalling |
| Code editor | `<textarea>` + live preview split pane; CodeMirror 6 (vendored) may replace it later as an isolated frontend change |

All third-party JS is **vendored into `web/static/vendor/`** (served same-origin): no CDN dependency, enables a strict CSP. All are MIT/BSD/Apache licensed.

## 3. Data model (SQLite)

```sql
CREATE TABLE users (
    id          TEXT PRIMARY KEY,            -- 128-bit random, url-safe base64
    username    TEXT NOT NULL UNIQUE COLLATE NOCASE,
    created_at  INTEGER NOT NULL,            -- unix seconds
);

CREATE TABLE passkeys (
    id              BLOB PRIMARY KEY,        -- WebAuthn credential ID
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT NOT NULL DEFAULT '',-- user-visible label ("MacBook", "YubiKey")
    public_key      BLOB NOT NULL,           -- COSE key
    attestation     TEXT NOT NULL,
    aaguid          BLOB NOT NULL,
    transports      TEXT NOT NULL DEFAULT '',
    sign_count      INTEGER NOT NULL DEFAULT 0,
    backup_eligible INTEGER NOT NULL DEFAULT 0,
    backup_state    INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    last_used_at    INTEGER
);

CREATE TABLE sessions (
    id            TEXT PRIMARY KEY,          -- sha256(token) hex; raw token only in cookie
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    user_agent    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE folders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (user_id, name)                 -- flat, single-level folders
);

CREATE TABLE notes (                       -- Markdown notes
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id  TEXT REFERENCES folders(id) ON DELETE SET NULL,  -- NULL = Unfiled
    title      TEXT NOT NULL DEFAULT 'Untitled',
    size       INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
    -- body lives at DATA_DIR/notes/{user_id}/{id}.md (file system, per requirement)
);

CREATE TABLE files (                       -- images & PDFs
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id     TEXT REFERENCES folders(id) ON DELETE SET NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('image','pdf')),
    original_name TEXT NOT NULL,
    mime          TEXT NOT NULL,
    size          INTEGER NOT NULL,
    sha256        TEXT NOT NULL,
    created_at    INTEGER NOT NULL
    -- blob lives at DATA_DIR/files/{user_id}/{id} (+ detected extension)
);

CREATE TABLE tags (
    id      TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    TEXT NOT NULL COLLATE NOCASE,
    UNIQUE (user_id, name)
);

CREATE TABLE item_tags (
    item_type TEXT NOT NULL CHECK (item_type IN ('note','file')),
    item_id   TEXT NOT NULL,
    tag_id    TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    user_id   TEXT NOT NULL,               -- denormalized for isolation checks
    PRIMARY KEY (item_type, item_id, tag_id)
);

CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); -- schema_version
```

Migrations: numbered SQL files embedded in the binary (`//go:embed migrations/*.sql`), applied at startup inside a transaction, tracked in `meta`.

**Deletion is hard delete** (confirmed): file + row removed — DB row first, disk file second, orphans swept at startup. No trash/soft-delete.

## 4. HTTP API

All under same origin. JSON bodies; session cookie for auth. All list/get/mutate endpoints are **scoped to the session's `user_id`** — ownership checks live in the storage layer, not handlers.

### Auth (WebAuthn)

| Method & path | Purpose |
|---|---|
| `POST /api/auth/register/begin` | body `{username, invite_code}`; one-time invite code required (see below); returns `PublicKeyCredentialCreationOptions` |
| `POST /api/auth/register/finish` | verify attestation → create user **and first passkey in one transaction** → log in |
| `POST /api/auth/login/begin` | body `{username?}`; empty username ⇒ discoverable-credential ("usernameless") login |
| `POST /api/auth/login/finish` | verify assertion → update sign count → session cookie |
| `POST /api/auth/logout` | destroy session |
| `GET  /api/auth/me` | current user + passkey list (401 if anonymous) |
| `POST /api/passkeys/begin` / `finish` | add another passkey to **authenticated** account |
| `PATCH /api/passkeys/{id}` | rename |
| `DELETE /api/passkeys/{id}` | remove (reject if it's the last one) |

WebAuthn settings: `residentKey: required`, `userVerification: "required"` (registration) / `"preferred"` (login); attestation `none`; challenge lifetime 60 s, single-use, in-memory, tied to the browser by a short-lived `notes_ceremony` cookie. `RP ID = notes.jys-reality.win` in prod, `localhost` locally (config).

**Ceremony wire format**: begin responses return the **inner options object** (the `{rp, user, challenge, …}` the browser's `navigator.credentials` call needs), not the `{publicKey: …}` envelope. Finish bodies wrap the browser's `PublicKeyCredential` as `{ceremony_id?, response}`; the ceremony may also be identified by its cookie. All ceremonies (register, login, add-passkey) follow this shape.

**Invite codes** (registration gate — the form is on the public internet): one-time codes stored as SHA-256 hashes in an `invite_codes` table (`code_hash`, `created_at`, `used_by`, `used_at`), marked used inside the same transaction that creates the user + first passkey. New codes are minted with `notes invite-new` (printable over SSH); no self-service generation. A user row is never created until a valid attestation is verified, so an abandoned ceremony cannot leave a passkey-less account.

### Sessions

* Token: 32 random bytes, base64url; cookie `notes_session` — `HttpOnly; Secure; SameSite=Lax; Path=/`.
* DB stores only the SHA-256 of the token. TTL 30 days, sliding (refresh on use), cleanup of expired rows on a timer.

### Notes

| Method & path | Purpose |
|---|---|
| `GET /api/notes` | list; filters: `?folder={id|none}&tag={name}&q={title substring}` |
| `POST /api/notes` | create; body `{title, body, folder_id?, tags?}` |
| `GET /api/notes/{id}` | `{id, title, body, folder_id, tags, created_at, updated_at}` |
| `PUT /api/notes/{id}` | update title, body, folder_id, tags |
| `DELETE /api/notes/{id}` | delete |

### Files (images & PDFs)

| Method & path | Purpose |
|---|---|
| `GET /api/files?kind=image|pdf` | list metadata; same `folder`/`tag`/`q` filters as notes |
| `POST /api/files` | multipart upload; one file; validated (see §5) |
| `GET /api/files/{id}/raw` | stream blob — `http.ServeContent` (Range/ETag; PDF.js benefits) |
| | `Content-Type` = stored/sanitized type, `inline` by default, `attachment` with `?download=1` |
| `GET /api/files/{id}` | metadata (incl. tags) |
| `PATCH /api/files/{id}` | move to folder, set tags |
| `DELETE /api/files/{id}` | delete |

### Folders & tags

| Method & path | Purpose |
|---|---|
| `GET /api/folders` | list with item counts |
| `POST /api/folders` | create `{name}` |
| `PATCH /api/folders/{id}` | rename |
| `DELETE /api/folders/{id}` | delete; contained items become Unfiled (`folder_id = NULL`) |
| `GET /api/tags` | list with item counts |

Tag rows left orphaned by item deletion are pruned in the same transaction. Folders are flat (no nesting) for v1.

### Conventions

* Errors: `{"error": {"code": "...", "message": "..."}}` with proper status codes (400/401/403/404/409/413/415/500).
* Mutating requests must carry `Content-Type: application/json` (or multipart for upload) **and** pass the Origin check (§6) — this doubles as CSRF defense.
* The app is a same-origin SPA-ish page (no client-side routing framework; a few pages + `history.pushState` where useful).

## 5. Upload & content validation

* Size limits: Markdown body 1 MB; uploads 25 MB (configurable).
* Allowed upload types: `image/png|jpeg|gif|webp|avif` and `application/pdf`. **No SVG** in v1 (text-based, can carry scripts; sanitized-embedding complexity not worth it).
* On disk, blobs are written to temp then renamed; filename is `{id}.{ext}` — **no user-controlled path components anywhere** (IDs are server-generated), which structurally eliminates path traversal.
* `sha256` stored for integrity/dedupe-safety checks.

## 6. Security

| Concern | Mitigation |
|---|---|
| Per-user isolation | Every query/path access goes through the storage layer with `user_id` from the session; integration tests assert cross-user 404s (never 403 — don't leak existence) |
| XSS | DOMPurify-sanitized markdown; no inline JS; strict CSP (§ below) |
| CSRF | `SameSite=Lax` cookie + `Origin` header must match host on all mutating requests + JSON content-type requirement |
| Session theft | HttpOnly + Secure cookies; token hashed at rest; expiry + sliding window |
| Credential stuffing / probing | Per-IP rate limit (30/min) on `/api/auth/*` with `Retry-After` on 429; generic error messages |
| Path traversal | Server-generated IDs only; `filepath` joined from constants; tests with `../` payloads |
| Malicious uploads | See §5 |
| Clickjacking | `X-Frame-Options: DENY`, `frame-ancestors 'none'` |
| Headers | `Content-Security-Policy: default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-src 'self'` (PDF.js runs same-origin), `Referrer-Policy: no-referrer`, HSTS left to Caddy |
| Secrets | None in the binary; WebAuthn needs no server secret beyond RelyingParty config. Config via env file `root:0600` |
| Backups | None configured (user decision #9): data loss on disk failure is accepted; the DB+blobs under `/var/lib/notes` survive ordinary redeploys |

No passwords exist anywhere, so there is no password DB to leak; a stolen session cookie grants access until expiry (mitigated by short-ish TTL and HttpOnly). Loss of **all** passkeys = unrecoverable account (by design; see open questions).

**Multi-device access** (no server changes needed; `residentKey: required` is what enables these):
1. *Synced passkeys* — passkeys saved to iCloud Keychain / Google Password Manager / third-party managers replicate across that ecosystem's devices automatically.
2. *Cross-device (hybrid) login* — on a device without a passkey, the browser offers QR-code sign-in using a passkey on a nearby phone (Bluetooth proximity, works across ecosystems).
3. *Per-device enrollment* — after such a login, Settings → Add passkey enrolls the new device's own platform authenticator.

Users should enroll a second passkey (or a hardware key) as the lockout hedge, since server-side recovery does not exist.

## 7. Frontend

Pages (vanilla JS ES modules, `web/static/`):

| Route | Contents |
|---|---|
| `/login` | username + invite code fields → **Sign in / Create account** buttons driving the two WebAuthn ceremonies; passkey management lives on the main app for authenticated users |
| `/` | Sidebar: folder list (with counts) + tag chips filter + title search; main pane: notes list (sort by updated) and **view** (rendered) ⇄ **edit** (textarea + toolbar, autosave draft to `localStorage`, explicit Save) with live split preview; New / Delete buttons |
| `/files` | Same sidebar (folders/tags); grid gallery for images (thumbnail = same `/raw` URL, lazy-loaded); list rows for PDFs with a viewer pane (PDF.js) or dedicated modal; per-item: move-to-folder, tag editor, copy-Markdown-snippet (images), Delete |
| `/settings` | rename/delete passkeys, add passkey, sign out, (storage usage) |

Rendering pipeline: `marked.parse(md)` → `DOMPurify.sanitize()` → inject; link handling: relative links resolved against `/api/files/...` suggestions; images in markdown reference uploaded files via `![](/api/files/{id}/raw)` — each gallery item has a **copy Markdown snippet** button (confirmed decision #6).

## 8. Configuration

Env vars (12-factor; systemd `EnvironmentFile`):

| Var | Default (local) | Prod |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | `127.0.0.1:8084` |
| `DATA_DIR` | `./data` | `/var/lib/notes` |
| `RP_ID` | `localhost` | `notes.jys-reality.win` |
| `ORIGIN` | `http://localhost:8080` | `https://notes.jys-reality.win` |
| `SESSION_TTL` | `720h` | `720h` |
| `MAX_UPLOAD_MB` | `25` | `25` |

Registration is gated by one-time invite codes (minted via `notes invite-new`), so there is no open-registration flag.

## 9. Repository layout

```
carseph_notes/
├── DESIGN.md                  ← this file
├── go.mod                     module: notes (local-only path)
├── cmd/notes/main.go          flag/env parsing, wiring, graceful shutdown
├── internal/
│   ├── httpapi/               mux, middleware (origin check, rate limit, auth),
│   │                          handlers for auth/notes/files, error envelope
│   ├── webauthn/              ceremony orchestration around go-webauthn
│   ├── sessions/              session store & cookie helpers
│   ├── store/                 all SQL (users, passkeys, sessions, notes, files, migrations)
│   ├── blob/                  file-system blob store (temp+rename, sweep orphans)
│   └── config/
├── web/                       embedded via go:embed
│   ├── index.html  login.html
│   ├── static/app.css  static/{app,auth,notes,files,settings}.js
│   └── static/vendor/  marked, dompurify, pdf.js, @simplewebauthn/browser
├── deploy/
│   ├── notes.service          systemd unit (see §10)
│   └── Caddyfile.notes        site block to append on the Droplet
├── Dockerfile                 multi-stage: golang:1.24 → distroless/static
├── docker-compose.yml         local verification (§11)
├── Makefile                   build / test / compose-up / deploy
└── data/                      runtime only (gitignored)
```

## 10. Deployment (Droplet, systemd — no Docker)

Target host facts (verified): Debian 13, 1 vCPU / 1 GB RAM, Caddy 2.11.4 already running with sites on ports 8080–8083/8090 → notes takes **8084**.

1. **Build**: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" ./cmd/notes` (pure-Go SQLite makes this possible). Single binary, assets embedded.
2. **Install**:
   * `useradd --system --user-group --home-dir /var/lib/notes notes`
   * binary → `/usr/local/bin/notes`; config → `/etc/notes/notes.env` (`root:notes 0640`)
   * data → `/var/lib/notes` (created by systemd `StateDirectory=notes`)
   * mint an invite code: `sudo -u notes env DATA_DIR=/var/lib/notes /usr/local/bin/notes invite-new`
     (offline subcommand; needs only `DATA_DIR`, validated leniently)
3. **systemd** (`deploy/notes.service`): `Restart=on-failure`, `ExecStart=/usr/local/bin/notes`, plus hardening: `DynamicUser=no`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `StateDirectory=notes`, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`.
4. **Caddy**: append `deploy/Caddyfile.notes`:

   ```
   notes.jys-reality.win {
       reverse_proxy 127.0.0.1:8084
   }
   ```

   `systemctl reload caddy` → automatic Let's Encrypt cert (DNS A record for `notes.jys-reality.win` → `137.184.22.193`, DNS-only).
5. **Deploy flow**: `make deploy` = build → `scp` binary → `systemctl restart notes` → `curl -fsS https://notes.jys-reality.win/api/healthz`.
6. **Health**: `GET /api/healthz` (no auth) returns `{status:"ok", version}` for the deploy check and future monitoring; DB retained across deploys since it lives in `/var/lib/notes`.

WebAuthn requires a secure context — satisfied by Caddy's HTTPS in prod and by `localhost` in dev, so passkey ceremonies work in both.

## 11. Local verification (Docker Compose)

```yaml
# docker-compose.yml (sketch)
services:
  notes:
    build: .
    ports: ["8085:8080"]     # host 8080 may be occupied by other local services
    environment:
      LISTEN_ADDR: ":8080"
      DATA_DIR: "/data"
      RP_ID: "localhost"
      ORIGIN: "http://localhost:8085"
      DEV: "true"            # plain-HTTP localhost; non-Secure cookies
    volumes: ["./data:/data"]
```

* `make compose-up` → app at `http://localhost:8085`. `localhost` is a secure context, so **real passkeys work locally** (RP ID `localhost`). Mint a code with `docker compose exec notes /notes invite-new`.
* The distroless image runs as uid 65532; `make compose-up` pre-creates `./data` world-writable so SQLite can write there.
* Since local data is throwaway, passkey/RP mismatch across environments is irrelevant — but never copy a local `data/` dir to prod (RP ID differs).
* Smoke test (grows with each milestone): M1 = healthz, login page, static assets, invite minting. M2/M3 add register → create note → upload → restart container → session persists.

## 12. Testing

| Level | What |
|---|---|
| Unit | storage layer (in-memory SQLite via `modernc`), origin/rate-limit middleware, path construction |
| Integration (`httptest`) | full HTTP flows; WebAuthn ceremonies driven by a fake authenticator implementing the CTAP2 client model (sign challenges with a throwaway ECDSA key, maintain sign counters) |
| Security tests | cross-user access returns 404; `../` in any path component; oversized/rejected MIME uploads; cookie flags asserted |
| Compose smoke | §11 |
| Manual | Chrome + Safari passkey flows ( TouchID / FaceID / security key), PDF.js rendering, image gallery, mobile layout sanity |

## 13. Milestones

1. **M1 — Skeleton + auth**: project layout, SQLite+migrations, WebAuthn register/login, sessions, settings page for passkeys. *Gate: two browser profiles see only their own (empty) accounts.*
2. **M2 — Notes**: CRUD + markdown editor + sanitized preview.
3. **M3 — Files**: upload/list/view/delete for images & PDFs (PDF.js).
4. **M4 — Harden + deploy**: security headers, rate limiting, compose smoke test, systemd + Caddy on the Droplet, DNS cutover.

## 14. Resolved decisions (from review)

1. **Upload limits**: 25 MB/file, ~2 GB/user soft quota — **confirmed**.
2. **Deletion**: hard delete, no trash — **confirmed**.
3. **Organization**: folders (flat) + per-item tags on notes, images, and PDFs — **adopted** (§3, §4, §7).
4. **Editor**: textarea + live preview for v1; CodeMirror 6 optional later — **confirmed**.
5. **Registration**: one-time invite code required, minted via `notes invite-new` CLI — **adopted**.
6. **Markdown image embedding**: copy-Markdown-snippet button in gallery — **confirmed**.
7. **SVG**: dropped in v1 — **confirmed**.
8. **Recovery**: losing all passkeys = losing the account — **accepted**.
9. **Backups**: none — **confirmed** (accepted risk: disk failure loses data).
