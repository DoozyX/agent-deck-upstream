package main

import (
	"strings"
	"testing"
)

func TestHandleLaunchMarksExplicitConductor(t *testing.T) {
	body := foldSpaces(mustExtractFuncBody(t, "launch_cmd.go", "handleLaunch"))

	if !strings.Contains(body, `conductor := fs.Bool("conductor", false,`) {
		t.Fatal("handleLaunch must expose --conductor so role selection does not require --no-parent")
	}
	if !strings.Contains(body, "if *conductor { newInstance.IsConductor = true }") {
		t.Fatal("handleLaunch must persist --conductor on the new session before it starts")
	}
}
