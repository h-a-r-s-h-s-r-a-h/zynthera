// Command zynthera-file-bridge runs inside each guest and moves files in and
// out of the user's home directory. It is implemented in milestone 6.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "zynthera-file-bridge: not implemented yet (milestone 6)")
	os.Exit(1)
}
