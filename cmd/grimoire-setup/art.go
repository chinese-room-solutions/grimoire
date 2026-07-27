package main

// grimoireArt is the Grimoire wordmark (figlet "standard"). The colour, not the
// glyphs, carries the flourish, so it stays ASCII and renders in any font. Every
// row is padded to the SAME width (41) with trailing spaces: term.Banner centers
// each row independently by its own width, so unequal rows would offset the
// glyphs per-row and misalign the columns. The term.Banner applies the synthwave
// gradient and centers it.
var grimoireArt = []string{
	"  ____      _                 _          ",
	" / ___|_ __(_)_ __ ___   ___ (_)_ __ ___ ",
	"| |  _| '__| | '_ ` _ \\ / _ \\| | '__/ _ \\",
	"| |_| | |  | | | | | | | (_) | | | |  __/",
	" \\____|_|  |_|_| |_| |_|\\___/|_|_|  \\___|",
}
