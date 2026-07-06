# Security notes

Operational security guidance for `bakku`. See also the repository format
section in the README.

## Cryptography overview

- **Key hierarchy:** `password → argon2id → KEK` wraps a random per-repository
  **master key** (AES-256-GCM). Purpose-separated subkeys (data / index /
  snapshot / chunker) are derived from the master key with BLAKE3 derive-key.
- **Blob encryption:** every data/index/snapshot blob and pack header is
  authenticated-encrypted with **AES-256-GCM** (confidentiality + integrity).
- **YubiKey slots** (from v0.2.4) derive their KEK with **HKDF-SHA256** over the
  HMAC-SHA1 challenge-response (slots created by earlier versions keep working
  via the legacy derivation).

## AES-GCM nonce limits (large / long-lived repositories)

bakku encrypts each blob with AES-256-GCM using a fresh **random 96-bit
nonce** under the repository data key. Random 96-bit nonces are safe up to the
birthday bound: [NIST SP 800-38D] recommends keeping a single key below roughly
**2³² encryptions** for a comfortable collision-safety margin. In bakku, one
"encryption" is one stored blob (a content chunk) or one pack header.

What this means in practice:

- Deduplication means **only unique chunks** consume nonces — re-backing up
  unchanged data does not.
- With an average chunk around 1 MiB, 2³² blobs corresponds to on the order of
  **petabytes of unique data** under a single repository key. Typical
  repositories are far below this.

Guidance for very large or very long-lived repositories:

- If a single repository is expected to store on the order of **10⁹+ unique
  blobs**, plan to rotate to a **fresh repository** periodically (a new repo has
  an independent master key and therefore an independent nonce space).
- A future release (v0.3.0) is planned to adopt an extended-nonce or
  nonce-misuse-resistant AEAD for blob encryption to remove this ceiling; it
  will be introduced behind a repository-format version so existing
  repositories continue to open.

[NIST SP 800-38D]: https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication800-38d.pdf

## Environment variables that affect security posture

| Variable | Effect | Recommendation |
|---|---|---|
| `BAKKU_SSH_INSECURE=1` | Disables SFTP host-key verification (accepts any host key). bakku prints a stderr warning when this is active. | Do **not** use in production. Add the host to `~/.ssh/known_hosts` instead. |
| `BAKKU_NOTIFY_ALLOW_PRIVATE=1` | Allows webhook notifications to be sent to private / loopback / link-local addresses (default blocks them as an SSRF guard). | Only set when your notification webhook is a self-hosted service on an internal address you trust. |
| `BAKKU_PASSWORD` | Supplies the repository password via the environment. | Visible in the process environment; prefer `--password-file` or `--password-command` (external secret manager) for scheduled/unattended jobs. |

## On-disk protection

- Local repository directories are created with mode **0700** and object files
  with **0600**, so key names, object sizes and structure are not exposed to
  other local users.
- The config file (`config.toml`) is written **0600**. Prefer
  `password_command` (e.g. 1Password `op`, `pass`, Bitwarden `bw`) over storing
  a password anywhere in plaintext.

## Restore safety

Restore confines every written path to the restore target and validates each
snapshot entry name, so a tampered or corrupted snapshot cannot escape the
target directory via `..` or absolute paths (path-traversal / zip-slip). Backend
storage keys are likewise validated to reject `..` and absolute paths.

## Reporting

Found a security issue? Please open a private report per the repository's
security policy rather than a public issue.
