// +build windows

package main

import (
	log "github.com/sirupsen/logrus"
	"os"
	"os/exec"
)

func Daemonize(logfile string, proc func()) {
	var newArgs []string
	for _, arg := range os.Args[1:] {
		if arg == "-d" || arg == "--daemon" ||
			arg == "/d" {
			continue
		}
		newArgs = append(newArgs, arg)
	}
	cmd := exec.Command(os.Args[0], newArgs...)

	err := cmd.Start()
	if err != nil {
		log.Fatal("start failed %v. ", err)
	}

	cmd.Process.Release()
}
