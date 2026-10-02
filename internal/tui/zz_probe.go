package tui

import "golang.org/x/term"

var _ = term.IsTerminal
var _ = term.GetSize
var _ = term.MakeRaw
var _ = term.Restore
