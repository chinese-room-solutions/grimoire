---
tags: [sre, incidents, postmortems]
---

# Postmortems

The written record of an incident: timeline, cause analysis, action items.
The point of the document is not accountability, it is that the same failure
should have to find a new way to happen next time.

## Blameless, meaning structurally

"Blameless" is not politeness, it is epistemics. If the writeup concludes
"X made a mistake", the analysis stops at the first human, and everything
upstream that made the action reasonable at the time stays invisible — the
runbook that did not mention the interaction, the alert that fired 13
minutes late, the staging environment that could not reproduce the load.
People act on the information and incentives in front of them; a postmortem
that names a person has documented nothing about the system.

The test question is not "who broke it" but "what made that look like the
right thing to do at the time". The July backfill incident
([[2026-07-14#Cause]]) is the local example: nobody was wrong, the deploy was
not gated on the migration job, and the fix was a pipeline gate — a system
change, not a conversation about being careful.

## The sections that earn their place

1. **Summary** — impact in user terms, duration, severity. Writeable last,
   readable first.
2. **Timeline** — UTC, everything, including the boring parts. The sequence
   is the data; the cause is an interpretation of it. Conflicting timestamps
   are expected when clocks drift, which is its own finding, [[ntp]].
3. **Contributing factors** — plural, always. The "root cause" framing
   assumes one hole in one slice of cheese; real incidents are a particular
   alignment of several, and the useful fixes are usually in the slices
   nobody was looking at.
4. **What went well** — the rollback that worked, the alert that fired on
   time. Reinforces the behaviours that paid off, and stops the document
   being a wall of self-criticism nobody reads.
5. **Action items** — each with an owner and a date. An action item without
   both is a wish.

## Severity and when to write one

Any incident that burned error budget, hit users, or took longer than it
should have to diagnose. The budget framing from
[[service-level-objectives]] makes the threshold objective: services with
explicit SLOs get an arithmetic answer (a blip inside the budget is a
ticket, a multi-percent burn is a document), and services without them get
an argument, which is one of the quiet arguments for writing SLOs down at
all. "We came close to finding out" (a single-replica dependency discovered
during an unrelated deploy) earns a lightweight one too, because next time
it will not come close, it will arrive.

Not every outage needs a postmortem, and the failure mode of mandatory
postmortems is paperwork — the third one this quarter for the same cause is
not a postmortem, it is an escalation of the first one's unshipped action
items.

## During the incident

The roles are worth assigning at the start, out loud: one person runs the
incident (decides, communicates, does not touch keyboards), one scribes
timestamps and observations into the channel, everyone else mitigates. The
scribe's stream of consciousness becomes the timeline — reconstructing one
afterwards from log timestamps and memory produces fiction.

Mitigate first, diagnose second. Restoring service destroys evidence, and
that is the correct trade: a postmortem with missing data beats an outage
extended for forensics. Snapshot what is cheap (current logs, `kubectl get`
output, the panel you were staring at) before rolling back.

## Aftermath, where they fail

Action item rot is the failure mode: a great document, five TODOs, and the
same incident eight weeks later because three of them slipped. Two defences
that work: action items filed as real tickets linked from the document (not
checkboxes in a wiki), and a review at the next release planning — the items
that survive contact with the roadmap are the ones someone argues for, the
rest were theatre.

The other failure is the postmortem written to be agreed with rather than
to be correct. If a draft surprises nobody and contradicts nothing, either
the incident was trivial or the draft is flattering. The review meeting
exists to find the sentence that is wrong; the document is done when
somebody in it learned something they did not know during the incident.
