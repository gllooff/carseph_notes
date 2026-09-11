# Carseph Notes

A self-hosted web app for storing personal notes and documents — Markdown
notes, images, and PDFs — with **passkey-only authentication** (WebAuthn) and
strict per-user isolation.

**Live instance**: <https://notes.jys-reality.win>

## Features

- **Markdown notes** — create, edit (textarea + live sanitized preview),
  render, delete. Bodies are stored as real `.md` files on disk.
- **Images, PDFs & Markdown files** — upload (magic-byte validated; Markdown
  requires a `.md`/`.markdown` name), view in a lightbox / embedded PDF.js
  viewer / rendered Markdown pane, `?download=1` downloads.
- **Organization** — flat folders and tags on notes *and* files, title search.
- **Copy Markdown snippet** — every image offers a `![](url)` snippet for
  embedding into notes.
- **Passkey auth** — no passwords. Register with a one-time invite code, sign
  in with fingerprint / FaceID / Windows Hello / security key. Discoverable
  credentials allow usernameless login. Add multiple passkeys per account
  (recommended: enroll a second one as your lockout hedge — see
  [DESIGN.md §6](DESIGN.md) for the multi-device story).
- **Per-user isolation** — every query and blob path is scoped server-side;
  cross-user access returns 404.

## Stack

- **Backend**: Go (stdlib `net/http`), SQLite via `modernc.org/sqlite`
  (pure Go, WAL mode), `go-webauthn/webauthn`. Single static binary with all
  assets embedded (`go:embed`).
- **Frontend**: vanilla HTML/JS/CSS ES modules, no build step. Vendored:
  `marked` + `DOMPurify` (markdown), `PDF.js`, `@simplewebauthn/browser`.
- **Storage**: SQLite for metadata, file system for note bodies and blobs.
- **Ops**: systemd + Caddy on a DigitalOcean droplet; Docker Compose for local
  verification. Full architecture in [DESIGN.md](DESIGN.md).

## Getting started (local)

### Run from source

```sh
make run            # serves http://localhost:8080 (DEV mode)
make invite         # mint an invite code (prints it)
```

Open <http://localhost:8080>, create an account with the invite code, and
enroll a passkey. `localhost` is a secure context, so real passkeys work.

### Docker Compose

```sh
make compose-up     # builds + starts; app at http://localhost:8085
docker compose exec notes /notes invite-new
```

Local data lives in `./data/` (throwaway; never copy it to production — the
WebAuthn RP ID differs).

### Tests

```sh
go test ./...
```

The suite covers store scoping (cross-user access → 404), upload validation
(magic bytes, size caps), rate limiting, and the full WebAuthn
register/login/add-passkey flow driven by a fake CTAP2 authenticator.

## Invites

Registration is gated by **one-time invite codes** (stored as SHA-256 hashes,
consumed atomically with account creation). Mint codes on the server:

```sh
make invite-prod    # ssh + sudo, prints a fresh code
```

## Deployment

Production runs on a DigitalOcean droplet (Debian 13) as a single binary under
systemd behind Caddy (auto-HTTPS), listening on `127.0.0.1:8084` — see
[DESIGN.md §10](DESIGN.md) for the full layout and first-time provisioning.

```sh
make deploy         # cross-compile, scp, restart, healthz check
```

Key files: `deploy/notes.service` (hardened systemd unit),
`deploy/Caddyfile.notes` (site block), `/etc/notes/notes.env` on the host.

## Security notes

- Session cookies are `HttpOnly; Secure; SameSite=Lax`; only SHA-256 hashes of
  tokens are stored; sliding 30-day expiry.
- CSRF: `Origin` check on all mutating requests.
- Strict CSP (`default-src 'self'`), no inline JS, all third-party JS vendored.
- Markdown is sanitized with DOMPurify before rendering; uploads are
  extension + magic-byte validated (PNG/JPEG/GIF/WebP/AVIF/PDF, plus Markdown
  `.md`/`.markdown` text, 25 MB cap).
- Auth endpoints are rate-limited per IP (30/min).
- Passkeys are the only credential: **losing all of them loses the account**
  (accepted trade-off; no server-side recovery).

### Manual DB work

If you ever run SQL against the production database (`/var/lib/notes/carseph.db`
on the Droplet) directly:

1. **Stop the service first**: `systemctl stop notes`
2. **Enable foreign keys in your session** — the `sqlite3` CLI defaults to
   `foreign_keys=OFF`, so `DELETE` statements silently skip cascades and leave
   orphan rows behind:
   ```sql
   PRAGMA foreign_keys=ON;
   ```
3. Orphaned blobs are harmless: the server removes blobs whose DB rows (or
   users) no longer exist on the next startup — but rows only get cleaned by
   you, so delete what you intend to delete.

Then `systemctl start notes` (or restart) and check
`https://notes.jys-reality.win/api/healthz`.

## Repository layout

```
cmd/notes/          entrypoint (serve, invite-new)
internal/config/    env-based configuration
internal/store/     SQLite: migrations, users, passkeys, sessions, notes,
                    folders, tags, files, invite codes
internal/blob/      file-system blobs (note bodies, uploads) + orphan sweep
internal/webauthn/  WebAuthn ceremony orchestration
internal/sessions/  cookie/token management
internal/httpapi/   mux, middleware (origin, rate limit, security headers),
                    all handlers
internal/web/       embedded frontend (static/ — pages, styles, vendored libs)
deploy/             systemd unit + Caddy site block
```

See [DESIGN.md](DESIGN.md) for the complete design document (architecture,
data model, API, security model, deployment).
