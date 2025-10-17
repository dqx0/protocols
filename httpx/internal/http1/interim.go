package http1

import (
    "bufio"
    "fmt"
)

// WriteContinue writes an interim 100 Continue response.
func WriteContinue(bw *bufio.Writer) error {
    _, err := fmt.Fprint(bw, "HTTP/1.1 100 Continue\r\n\r\n")
    return err
}

