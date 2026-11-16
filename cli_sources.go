package main

import (
	"flag"
	"strings"
)

// Go's flag package stops at the first operand. Keep operands at the end so
// shell-expanded globs also work when later flags follow them.
func sourceArguments(fs *flag.FlagSet, args []string) []string {
	var flags, operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			operands = append(operands, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if len(operands) > 0 {
		flags = append(flags, "--")
		flags = append(flags, operands...)
	}
	return flags
}
