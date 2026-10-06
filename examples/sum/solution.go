//go:build ignore

package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	in := bufio.NewReader(os.Stdin)
	var a, b int64
	fmt.Fscan(in, &a, &b)
	fmt.Println(a + b)
}
