package main

import (
	"fmt"
	"io"
	"log"

	"dqx0.com/go/protcols/httpx"
)

func main() {
	c := &httpx.Client{}
	res, err := c.Get("https://api.dqx0.com")
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	fmt.Println(res.StatusCode, string(b))
}
