// Colaunch runs models and anything else on Google Colab's GPUs and TPUs
// from the desktop and the phone.
package main

import (
	"log"

	"colaunch/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
