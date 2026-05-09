# ADR-0004: Publish the agent image to GHCR; CX23 pulls instead of builds

- **Status:** accepted
- **Date:** 2026-05-09
- **Deciders:** Emil Østergaard
- **Supersedes:** —
- **Superseded by:** —
- **Related:** [ADR-0001](0001-local-only-inference.md)

## The escape hatch comes first

The operator can rebuild the agent binary on their laptop, save the
image to a tarball with `docker save`, and `docker load` it on the
CX23 — without GHCR, without GitHub Actions, without an internet
connection beyond the laptop's. The dev compose
(`docker-compose.dev.yml`) already builds from local source and is
preserved as the canonical sovereign-build path:

```bash
# On the operator's laptop
docker compose -f docker-compose.dev.yml build aceso
docker save aceso:dev | ssh deploy@cx23 'docker load'

# On the CX23
docker tag aceso:dev ghcr.io/emil-oestergaard/aceso:local
ACESO_IMAGE=ghcr.io/emil-oestergaard/aceso:local docker compose up -d
```

This is not a hypothetical. It is the path that runs if GitHub
disappears, if GHCR is down, if the operator decides they no longer
trust the GitHub build environment. The fact that the local-build
path remains *first-class* — same Dockerfile, same compose graph,
no special flags — is what keeps this decision consistent with
[ADR-0001](0001-local-only-inference.md)'s sovereignty stance. GHCR
is the *default convenience*, not the *only path*.

This ADR exists because that distinction is load-bearing and not
self-evident from reading the workflow file.

## Context

[ADR-0001](0001-local-only-inference.md) commits Aceso to local-only
inference: production data (logs, prompts, diagnoses) does not leave
operator infrastructure. The corollary the operator inherited was
"build the agent binary on the CX23 too" — `docker-compose.yml` used
`build: ./agent`, so deploying meant cloning the repo to the VPS,
running `docker compose up --build`, and trusting the CX23's build
environment.

That posture has three problems for V0:

1. **Source on the production host.** The CX23 carried the full repo
   (source, tests, ADRs, scripts) at runtime. None of it is needed
   to *run* the agent — only the image, `docker-compose.yml`, `.env`,
   and the `/data` volume.
2. **Build provenance disappears.** "What version is running on the
   CX23?" answered by `git rev-parse HEAD` requires the source tree
   to be there and untouched. `docker image inspect` gives nothing
   useful when the image was built locally with no labels.
3. **No regression gate before deploy.** The operator's discipline
   was to run `go test -race -cover ./...` on the laptop before
   `git push` — but nothing forced it. An accidental `--build -d`
   on a bad commit would have shipped a broken binary.

The alternative is to build the image once, in a known environment,
and have the CX23 pull it. The two reasonable build environments are:

- **GitHub Actions + GHCR.** Public repo gets unlimited free
  Actions minutes and unlimited GHCR storage. Image labels record
  the source SHA. CI gates the build on `go test -race -cover`.
  Operator does not run a build host.
- **Self-hosted CI.** Drone, Woodpecker, or Forgejo Actions on a
  third box. Same outcome, more operator surface area. Reasonable
  if GitHub is rejected on principle, but introduces another box
  to keep patched.

GitHub is already where the source lives. The marginal trust
expansion is "GitHub Actions builds my source faithfully" — a trust
already implicit in "GitHub hosts my source faithfully." Adding a
third box to avoid that marginal trust does not net out.

## Decision

The agent image is built and published by GitHub Actions on every
push to `main`:

- `.github/workflows/build.yml` runs `go vet` + `go test -race -cover`
  on every push and PR. Failure blocks the build job.
- On `push` to `main`, the build job builds `agent/Dockerfile` and
  pushes two tags to `ghcr.io/emil-oestergaard/aceso`:
  - `:latest` — rolling, points at the most recent green main build.
  - `:sha-<short-sha>` — immutable, pinned by SHA.
- The production `docker-compose.yml` no longer carries `build:`. It
  uses `image: ${ACESO_IMAGE:-ghcr.io/emil-oestergaard/aceso:latest}`,
  so the operator pins a SHA in `.env` for production
  (`ACESO_IMAGE=ghcr.io/emil-oestergaard/aceso:sha-<short-sha>`)
  and rolls back by editing the SHA.
- `docker-compose.dev.yml` keeps `build: ./agent` unchanged. The
  build-from-source path is preserved as a first-class flow for
  development and as the sovereign escape hatch.

## Trust delta vs ADR-0001

| Surface | Pre-ADR-0004 | Post-ADR-0004 |
|---------|--------------|---------------|
| Production data (logs, prompts, diagnoses) | Stays on operator infra | **Unchanged** — stays on operator infra. The image runs on the operator's CX23, talks to the operator's Pi, persists to the operator's volume. |
| Source code | Public on GitHub | Unchanged |
| Build environment | Operator's CX23 | GitHub Actions (Microsoft-hosted Linux runner) |
| Image registry | None (built in place) | GHCR (public) |
| Build artefact integrity | Whatever the CX23 produced | Pinned by SHA tag; SLSA provenance attestation can be added (deferred to follow-up; see workflow comment) |

The data trust boundary is unchanged. The new trust expansion is
"GitHub Actions builds my source faithfully" — bounded by the
public Dockerfile and the public source. No prompt content, no
incident data, no operator config, no secret material flows through
GitHub's build environment.

## Why this is consistent with sovereignty (not a compromise of it)

Three properties keep this consistent:

1. **The escape hatch above is first-class, not a fallback.** The
   dev compose still builds from local source with the same
   Dockerfile. An operator who decides GHCR is unacceptable can
   build locally and `docker save | docker load` to the CX23
   without modifying anything in the repo.
2. **The build artefact is verifiable.** SHA-pinned tags + image
   labels carrying the source commit mean "what's running on the
   CX23?" is answerable. Attestations (deferred to a follow-up
   commit on the workflow) make it cryptographically verifiable.
3. **Nothing operator-private flows through GitHub.** The trust
   we extend to GitHub Actions is bounded by what the binary does
   at build time — it compiles Go and assembles a container image.
   It does not see the CX23's `.env`, the WG keys, the ntfy topic,
   or any production data.

If a future change ever proposes flowing operator data through
GitHub (build-time secrets, deploy-time webhooks into the CX23,
remote-triggered actions), this ADR is the place it gets debated.

## Consequences

### Positive

- The CX23 carries `docker-compose.yml`, `.env`, and the `/data`
  volume. No source, no toolchain, no Dockerfile.
- `docker pull && docker compose up -d` is the entire update path.
- Production deploys are SHA-pinnable; rollback is a one-line `.env`
  edit and a `docker compose up -d`.
- Tests run in a known environment before any image ships. An
  accidental "I forgot to run the test suite" cannot reach
  production.
- The image is public, so anyone can audit what's running. The
  operator does not need a GHCR auth token on the CX23 for a public
  package.

### Negative

- One more piece of infrastructure operationally relevant to the
  operator: the GitHub Actions tab. If a build fails, the deploy
  is blocked until the failure is understood. This is a feature
  most days, but a constraint on bad days.
- Trust expansion to GitHub Actions is real, even if narrow. If
  GitHub were ever compromised at the build-environment level, an
  attacker could ship a malicious image. SHA pinning + attestations
  make this detectable post-hoc; they do not prevent it.
- A GHCR outage blocks pulls, which means it blocks deploys. It
  does *not* affect already-running containers. The escape hatch
  remains available throughout.

### Neutral

- The `:latest` tag is convenient for soak but should not be used
  in production once the soak is over. SHA pinning is cheap and
  removes ambiguity.

## Alternatives considered

| Option | Rejected because |
|--------|------------------|
| Build on the CX23 (the prior posture) | Source on the production host; no test gate before deploy; "what's running?" hard to answer. |
| Self-hosted CI (Woodpecker, Forgejo Actions) | Adds another box for the operator to keep patched; the marginal sovereignty gain over GitHub Actions does not justify the operational cost for a one-host fleet. Worth revisiting if multi-host enters scope. |
| Push to Docker Hub instead of GHCR | Docker Hub free tier has pull-rate limits that affect unauthenticated pulls; adds a second account/credential for an operator who already has a GitHub one. GHCR is co-located with the source. |
| Sign images with cosign + keyless OIDC | Would strengthen the verifiability story but adds workflow complexity. Worth doing as a follow-up once SLSA attestations are confirmed working. |

## Implementation

- `.github/workflows/build.yml` — test job + build-and-push job
  (provenance/SBOM attestations deferred to follow-up; see TODO in
  the workflow file).
- `docker-compose.yml` — `image: ${ACESO_IMAGE:-ghcr.io/...}`,
  `pull_policy: always`, no `build:` block.
- `docker-compose.dev.yml` — unchanged; preserves the local-build
  escape hatch as a first-class development flow.
- `README.md` "Running with Docker (production)" section — `pull`
  + `up -d` flow + SHA-pinning guidance.
- `docs/status.md` "CI pipeline" row.
- Commit `7075166` (ci: build + publish image to GHCR; switch prod
  compose to pull-not-build).
