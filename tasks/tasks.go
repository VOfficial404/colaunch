// Package tasks holds the community's tasks: the packs of this folder and
// index.json, which the app's community library reads from GitHub. The
// app keeps JupyterLab's as the example it starts users with.
package tasks

import (
	_ "embed"

	"colaunch/internal/plugins"
)

//go:embed jupyterlab.colaunch.json
var jupyterLab []byte

// Example is the task users can start from: JupyterLab.
func Example() *plugins.Pack {
	p, err := plugins.Parse(jupyterLab)
	if err != nil {
		panic(err)
	}
	return p
}
