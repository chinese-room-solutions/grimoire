// Standalone module for Grimoire's yaegi Go kernel runner. It is intentionally
// separate from Grimoire's own module so yaegi (the embedded Go interpreter)
// never becomes a dependency of the Grimoire binary — this kernel is installed
// alongside Grimoire, not built into it.
module grimoire-kernel-yaegi

go 1.26

require github.com/traefik/yaegi v0.16.1
