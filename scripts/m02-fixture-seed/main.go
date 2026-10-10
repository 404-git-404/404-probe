// Local isolated acceptance fixture. Never connects to a deployed Server.
package main

import (
	"404-probe/internal/auth"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		panic("output hash path required")
	}
	h, e := auth.HashPassword([]byte("isolated-m02-password"))
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(os.Args[1], []byte(h+"\n"), 0600); e != nil {
		panic(e)
	}
	fmt.Println("fixture hash written")
}
