package main

import (
	"fmt"

	"github.com/jmvaldez/bosun/internal/config"
)

func (a *app) config(_ []string) int {
	cfg, res, err := config.Load(a.ad.Paths.ConfigFile())
	if err != nil {
		fmt.Fprintf(a.out, "# error: %s: %v (showing defaults)\n\n", a.ad.Paths.ConfigFile(), err)
	}
	fmt.Fprint(a.out, config.Format(cfg, res))
	if err != nil {
		return 1
	}
	return 0
}
