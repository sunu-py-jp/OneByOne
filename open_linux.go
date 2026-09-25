package main

import "os/exec"

func openFolder(path string) error { return exec.Command("xdg-open", path).Start() }
