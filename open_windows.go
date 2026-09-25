package main

import "os/exec"

func openFolder(path string) error { return exec.Command("explorer.exe", path).Start() }
