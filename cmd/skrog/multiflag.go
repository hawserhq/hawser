package main

import "strings"

// multiFlag collects a repeatable string flag: --only a --only b -> [a, b].
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
