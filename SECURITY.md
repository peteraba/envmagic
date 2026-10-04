# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's private vulnerability reporting:
open the repository's Security tab and use "Report a vulnerability"
(https://github.com/peteraba/envmagic/security/advisories/new).

Do not open a public issue for security problems.

Include:

- The affected version or commit.
- Your OS and shell.
- Steps to reproduce.
- The impact.

The maintainer aims to acknowledge reports within 7 days. There is no bug bounty.

## Supported versions

Only the latest released minor version receives security fixes.
Older versions do not. Update to the latest release.

## Scope

In scope:

- Anything that exposes stored values or the key to another user or another
  user's processes.
- Anything that lets a stored value or crafted input execute code through the
  shell wrappers.
- Anything that weakens the encryption or its binding of values to namespace
  and name.
- Anything that corrupts stored data.

Out of scope:

- An attacker who already runs code as the same user or can read the user's
  files. The key and the store are readable by that user by design.
- Variable names and namespaces being visible. They are plaintext by design.
- Loss of the key. There is no recovery by design. Back up your key.
- Denial of service by a local user who can already modify the store.

