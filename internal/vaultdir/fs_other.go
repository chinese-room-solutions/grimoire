//go:build !windows && !darwin

package vaultdir

// isCaseInsensitiveFS is false on Linux and other Unix-likes, whose default
// filesystems are case-sensitive.
const isCaseInsensitiveFS = false
