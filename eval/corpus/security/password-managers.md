---
tags: [security, passwords, auth]
---

# Password managers

Every password unique, every password random, one master passphrase you
actually remember. The manager is not a convenience app; it is the only
realistic way to have hundreds of unrelated credentials, and the alternative
— password reuse — is the attack that actually works, because credentials
leak from the weakest site you used them on and then get replayed against
the strongest.

## What runs here

- **Vaultwarden** self-hosted on the cluster, server-compatible with the
  Bitwarden clients — every browser and phone has a first-party client that
  speaks the protocol, which is the property that makes leaving the official
  server painless. The data volume is in the backup set like everything else
  stateful; a password database with no backup is a hostage situation.
- **KeePassXC** as the offline fallback: one database file, keyfile plus
  passphrase, kept in sync with the vault only by an occasional manual
  export. If the cluster is down, this is what opens things.
- **pass** for machine-facing secrets that live in git repos — GPG-encrypted
  files, one per secret, because it diffs and reviews like code. The overlap
  with [[secrets-rotation]] is deliberate: same secrets, different consumers.

The browser extension is the load-bearing client: autofill fills the right
credential *on the right domain*, which is phishing protection that no amount
of user discipline replicates. A manager used via copy-paste from a window
loses that property — you can paste the banking password into the banking
lookalike. Turn on "only autofill on the exact domain", and let it warn on
duplicate or similar-looking domains.

## Master passphrase and KDF

The master passphrase is the one secret with a human in the loop, so it
needs to be long rather than weird: five random words beat a mangled
eight-character string on both entropy and your ability to type it for the
next decade. The KDF is the other half — Argon2id with memory cost raised
to whatever the hardware tolerates (~64MB, 3 iterations) turns an offline
attack on a stolen database from "hours per guess" to "not happening".
Older databases on PBKDF2 should be migrated; the iterations counter is
visible in the client's settings and worth checking once.

Two-factor on the vault itself: TOTP is the floor (and the TOTP seed does
not live *in* the same vault — storing the second factor next to the first
is one factor with extra steps). A FIDO2 key is better and is the same
hardware used for [[ssh-keys]], which is a nice consolidation.

## TOTP, and where it belongs

TOTP — time-based one-time codes, the six digits — is the second factor on
everything that offers one, with the seeds in a separate app, not the main
vault. Enrol everywhere during account setup; the marginal cost is scanning
one more QR code and the marginal benefit is that a phished or keylogged
password is not the whole account. The code is derived from a shared secret
and the time, which quietly requires the clock to be right, [[ntp]] — a
phone with a dead time sync generates correct-looking codes that are wrong,
in the most confusing possible way.

## Breach hygiene

The manager's breach reports (Have I Been Pwned integration) get read when
they arrive, not quarterly. A hit means: change that password, and change it
everywhere it was ever reused in the dark years before the manager, which
the password history feature makes archaeology rather than guesswork.

## Sharing and succession

- **Family** — collections shared to the partner's account for anything
  that matters jointly (utilities, insurance, the router). Individual items
  stay individual.
- **Emergency access** — configured with a waiting period, so the partner
  can request access and a misconfigured device cannot leak the vault
  instantly. Test it once; untested succession plans are a genre of tragedy.
- **Work** — the work vault is the employer's, exported nothing, treated as
  a different trust domain entirely. The credentials should not cross, in
  either direction, and the client makes that easy with separate accounts.

## Failure modes

The realistic ones, in order of how often I have watched them happen:
forgetting the master passphrase after an injury break (write it down, store
it with the passport — the threat model is your memory, not a burglar who
reads), the self-hosted server dying on certificate expiry with no offline
copy ([[tls#Operational notes]]), and sync conflicts from two devices edited
in airplane mode. All three are survivable with the export file that exists
because backups are boring, [[backups#The rule]].
