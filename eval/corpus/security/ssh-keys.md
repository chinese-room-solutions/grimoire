---
tags: [security, ssh, keys]
---

# SSH key management

## Key types

Ed25519 is the default answer everywhere OpenSSH 6.5+ is installed, which is
everywhere:

```
ssh-keygen -t ed25519 -a 100 -C "kerne@laptop-2026"
```

Small keys, fast verification, no parameter soup to get wrong. RSA remains
only for talking to elderly appliances that never learned a second algorithm
— if forced, 4096 bits and a dedicated key per host, so the weak trust is at
least quarantined. ECDSA had the curve-validation mess in its history and
buys nothing over Ed25519.

Per-purpose keys, not one keyring to rule them all: one per client device
per identity (personal, work). A laptop sold without a `rm -f
~/.ssh/id_ed25519*` then has a blast radius of one row in one
`authorized_keys`, and revocation is editing that one row.

## Passphrases and the agent

Every key gets a passphrase — an unencrypted private key file turns any
read of the home directory into every host the key reaches. The agent makes
the passphrase cost once:

```
AddKeysToAgent yes
IdentitiesOnly yes
```

in `~/.ssh/config`. The first stops the re-typing tax; the second stops the
client offering five identities at every host until one works — servers
count failed offers (`MaxAuthTries` defaults to 6) and a keychain of
everything will lock you out of strict hosts while leaking which keys exist
where.

Agent forwarding (`ForwardAgent yes`) is the habit to break: a forwarded
agent is usable by root on every hop, and the hop is exactly the
untrusted-ish machine you SSH *through*. `ProxyJump` reaches the destination
directly through the bastion without exposing the agent to it, and covers
99% of the cases forwarding was doing.

## Hardware keys

FIDO2 keys with the OpenSSH sk type:

```
ssh-keygen -t ed25519-sk
```

The private key lives in the token and cannot be exported — malware on the
laptop can *use* it while plugged in and touched, but cannot *take* it.
`-O resident` writes a discoverable credential the key can carry to a new
machine (`ssh-add -K`), trading that portability for the key being usable
wherever the physical token is. The resident variant plus a spare token,
enrolled *before* the first one goes through the wash, is the whole
availability story.

## Certificates instead of authorized_keys

The mechanism people run into late: an SSH certificate authority signs
public keys, servers trust the CA, and access becomes "hold a short-lived
certificate for these principals" instead of "appear in this file":

```
ssh-keygen -s ca_key -I kerne-laptop -n kerne,ops -V +8h id_ed25519.pub
```

The server side is one `TrustedUserCAKeys` line, plus
`AuthorizedPrincipalsFile` when different hosts should accept different
principals. What this buys: no more editing `authorized_keys` across a
fleet, revocation that is expiry rather than an audit of every file, and
access rules expressed as names a person can read. The same
identity-not-addresses argument as [[mutual-tls#Identity, not addresses]],
with the same operational shape — issuance has to be automatic, and a
certificate nobody can mint at need is an outage with extra steps.

For a fleet of two and a bastion, plain keys with per-device entries remain
correct; certificates start paying at "I cannot name every machine I log
into".

## Host keys

`HashKnownHosts yes` so the known_hosts file is not a plaintext map of every
host you have visited. The TOFU (trust on first use) model is fine until a
host is rebuilt and the key legitimately changes — the warning that follows
is the correct behaviour, and the reflex to type `yes` without comparing
fingerprints is how a MitM becomes Tuesday. The fingerprint goes in the
provisioning output; compare once per new host, then never think about it
again.

`StrictHostKeyChecking accept-new` is the reasonable middle: first
connection auto-trusts, a *changed* key still refuses. Not `no`, which
silently accepts both.

## Into the homelab

Keys only, passwords disabled (`PasswordAuthentication no` on every
listener), and the only path in is the WireGuard tunnel, so the SSH surface
is the VPN surface, [[router#Getting to services from outside]]. The
bastion's own key is the hardware token; everything else rides the agent.
Rotating the underlying secrets when someone leaves or a laptop dies is its
own topic, [[secrets-rotation]].
