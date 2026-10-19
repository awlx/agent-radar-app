//go:build !windows && (!darwin || !cgo)

package tray

import "context"

const Supported = false

func Run(context.Context, Options, Source) {}
