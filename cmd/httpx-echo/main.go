package main

import (
    "fmt"
    "log"
    "strings"

    "dqx0.com/go/protcols/httpx"
)

func main() {
    s := &httpx.Server{Addr: ":8080"}
    s.Handler = httpx.HandlerFunc(func(w httpx.ResponseWriter, r *httpx.Request) {
        w.Header().Set("Content-Type", "text/plain; charset=utf-8")
        w.WriteHeader(200)
        var b strings.Builder
        fmt.Fprintf(&b, "%s %s\n", r.Method, r.RequestURI)
        for k, v := range r.Header {
            fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(v, ", "))
        }
        w.Write([]byte(b.String()))
    })
    if err := s.ListenAndServe(); err != nil {
        log.Fatal(err)
    }
}

