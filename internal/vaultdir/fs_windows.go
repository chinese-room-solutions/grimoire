package vaultdir

// isCaseInsensitiveFS reports whether the platform's filesystem treats paths
// case-insensitively, so the vault-path hash key can be normalized accordingly.
// Windows (NTFS) is case-insensitive in practice.
const isCaseInsensitiveFS = true
