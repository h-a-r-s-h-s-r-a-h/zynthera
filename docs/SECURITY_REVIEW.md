# Security review

Reviewed against the fourteen requirements in `PROJECT_SPEC.md` section 16, on 2026-11-19.
"Verified" names the test or the manual check that shows it. Where something could not be
verified on this machine, it says so.

## Result by requirement

| # | Requirement | Status | How it is met, and how it was verified |
|---|---|---|---|
| 1 | No shell injection | Met | Every host command goes through `internal/agent/run` as an argument list; nothing passes through a shell. Ids are checked as lower-case UUIDs before they reach a path or a command (`vmtypes.IsUUID`, `VMSpec.Validate`, `qcow2.checkID`). Verified: `TestSpecValidation`, `TestIDsNeverBecomePaths`, `TestIDsAndBodiesAreValidated`. |
| 2 | Structured rendering | Met | cloud-init comes from a YAML serializer, domain XML from `encoding/xml`. Verified with hostile values: `TestValuesCannotBreakOutOfTheirField`, `TestHostileValuesStayInsideTheirAttribute`; the XML also passes `virt-xml-validate`. |
| 3 | Nothing in a VM reachable from outside | Met in code, partly verified | VNC listens on host loopback only; the guest agent is a host-side socket; the file bridge is reached only through the agent. Verified: `TestSecurityRelevantChoices`, the stream and file proxy tests. The host firewall that enforces the network side is written and loads cleanly, but has **not been tested against real VMs** (needs root). |
| 4 | The agent trusts two callers | Met | Agent token compared in constant time on every control-plane route; browsers need a signed, purpose-bound token from the allowed origin. The agent listens on loopback. `single-server.Caddyfile` exposes only `/stream` and `/files/*`; `host.Caddyfile` limits everything else to the control plane's address. Verified: `TestControlPlaneRoutesNeedTheToken`, `TestTokenChecks`, `TestOriginCheck`, both Caddy files validated. |
| 5 | Ownership checks on every VM route | Met | Ownership is part of the lookup query. Verified: `TestOtherUsersGet404Everywhere` covers 14 routes and checks that a foreign id and an unknown id look identical. |
| 6 | Secrets | Met | `.env` is git-ignored; host credentials are AES-256-GCM encrypted in the database; production refuses placeholder secrets. Access logs record the path only, and the Caddy log filter removes the `token` query value. Verified: `TestCreateHostShowsSecretsOnce`, `TestOpenRejectsTamperingAndWrongKey`, the production start test. |
| 7 | Passwords | Met | argon2id in PHC format; the Linux password is hashed in the request handler, and the hash is removed from the database after provisioning. Verified: `TestVMLifecycle`, `TestMatchesOpenSSL`. |
| 8 | Sessions and CSRF | Met | `HttpOnly`, `SameSite=Lax`, `Secure` in production; only the SHA-256 of the cookie is stored; Origin check on unsafe methods; rotation on login; all sessions revoked on password reset. Verified: `TestWrongOriginIsRejected`, `TestLoginRotatesTheSession`, `TestPasswordResetFlow`, production cookie check. |
| 9 | Rate limits | Met | All six limits of spec 10.2, in Redis. If Redis is unavailable the limiter refuses requests rather than letting them through. Verified: `TestLoginRateLimit`, `TestSignupAndForgotRateLimits`. |
| 10 | Network isolation | Written, not verified | Port isolation and `clean-traffic` are in every VM definition (verified by test). The nftables rules (no VM to host beyond DHCP/DNS, no port 25, no private ranges) load in a test namespace. Whether a real VM is actually blocked has **not been tested**. |
| 11 | Resource limits | Met | RAM and vCPUs fixed per VM; per-user limits and host capacity enforced under row locks; request bodies limited (1 MiB API, 64 KiB agent, configurable upload limit enforced while streaming). Verified: `TestStartRefusedWithoutCapacity`, `TestCreateValidationAndLimits`, `TestFileTransferEndToEnd`. |
| 12 | Web hardening | Met in production config | The API sends `nosniff`, `no-referrer`, `frame-ancestors 'none'`. The web app's Content Security Policy, HSTS and the rest are set by Caddy. User text is only ever rendered as text by React. See "Open points" for the CSP's weak spot. |
| 13 | Deletion is real | Met | Verified on real KVM: `TestDeleteLeavesNothingBehind` and the manual check (directory, domain, DHCP entry, reservation, process, saved image all gone). |
| 14 | Audit trail | Partly met | Every VM state change is in `vm_events` with its actor (user, admin, system). Admin actions and logins are in the structured log, and logins also in `sessions` (IP, user agent). There is **no dedicated audit table** for admin actions and logins yet. |

## Fixed during this review

- The cloud-init seed (password hash and file bridge token) was readable by every local user
  on the host (mode 0644). It is now 0600. libvirt hands it to the QEMU user while the VM
  runs, which was verified on real VMs.

## Dependency scans

- `govulncheck ./...`: no vulnerabilities found.
- `npm audit --omit=dev`: 0 vulnerabilities.

Both were run on 2026-11-19 and should be repeated before each release.

## Open points

| Point | Risk | Suggested action |
|---|---|---|
| Firewall and isolation untested on real VMs | A mistake in the rules would let a PC reach the host or the provider's network | Run acceptance step 10 on a real host: from inside a PC, try the host's SSH port, the agent port, another PC, `169.254.169.254` and port 25 |
| CSP allows inline scripts | Next.js emits inline scripts without a nonce in this setup, so `script-src` includes `'unsafe-inline'`. An HTML injection would not be stopped by the CSP | Move to a nonce-based CSP through the Next.js proxy file |
| No audit table for admin actions and logins | Logs can be rotated away | Add an `audit_log` table written in the same transaction as the action |
| No abuse detection | A PC can be used for crypto mining or attacks on others; only bandwidth is capped and port 25 is blocked | CPU-usage alerts per PC, outbound connection-rate limits, terms of use |
| Files token lives 10 minutes and is reusable | A leaked download link gives access to that PC's home folder for up to 10 minutes | Acceptable for V1; shorten `ZYN_FILES_TOKEN_TTL_SECONDS` if needed |
| One host, no backups | A disk failure loses users' PCs | Milestone 7.7 (backups); mirrored disks until then |
| `install.sh` not yet run on a fresh server | The first production install may hit an untested step | Do a trial install on a throwaway server before real users |
