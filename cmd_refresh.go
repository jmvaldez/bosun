package main

import (
	"github.com/jmvaldez/bosun/internal/refresh"
)

func (a *app) refresh(_ []string) int {
	cfg := a.loadConfig()
	a.withIdentity(cfg)
	return refresh.Run(a.ad, cfg, a.out, a.errw)
}
