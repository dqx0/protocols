httpx — small HTTP/1.1 server/client for Go
==========================================

httpx is a small, security‑minded HTTP/1.1 server and client implementation
designed for learning, control, and embeddability. It favors explicit behavior,
safe defaults, and simple extension points over feature sprawl.

Status
- Experimental. Public API may evolve before 1.0. Read the docs and verify your use cases.

Features (Server)
- HTTP/1.1, streaming ResponseWriter, keep‑alive, chunked transfer encoding
- Expect: 100‑continue（遅延送信: Body読み出し開始で100を送出、待機時間を計測/制御）
- gzip 応答圧縮（EnableGzip）/ gzip リクエスト自動解凍（DecompressRequest）
- ヘッダ堅牢化: 行長/総サイズ上限、ヘッダ名/値の検証、Host ヘッダの形式検証
- ボディ決定の厳密化: CL/TE 検証、矛盾検出、chunked の厳密処理
- Panic 回復: 500 応答＋接続制御、スタックログ、メトリクス
- Graceful shutdown: `Shutdown(ctx)` で新規 Accept 停止、既存接続を期限付きで待機
- 観測: `Logger`（構造化対応しやすい）/`Meter`（Counter/Histogram）フック

Features (Client)
- Minimal HTTP/1.1 transport（BasicTransport）: 接続プール、Keep‑Alive、再利用
- プロキシ: HTTP プロキシ、HTTPS は CONNECT、環境変数（HTTP(S)_PROXY/ALL_PROXY/NO_PROXY）対応
  - NO_PROXY 拡張: ドメイン/サフィックス/host:port/CIDR/IPv6 bracket/スキーム付き
- TLS: SNI/ALPN（http/1.1）サポート
- リダイレクト: 301/302/303/307/308 に対応、ポリシー差し替え/上限回数指定
- タイムアウト/キャンセル: `Request.Context()` と `Client.Timeout` を併用
- トレース: `Traceparent` 自動付与、`Tracestate` 継承、Context/ヘルパで伝播容易
- 明示的なリソース制御: `CloseIdleConnections()`/`(*BasicTransport).Close()`

Security Defaults
- 受信ヘッダ検証（token/値の制御文字禁止）、レスポンスヘッダ値のサニタイズ
- Content‑Length/Transfer‑Encoding の矛盾検出（スマグリング対策）
- Host ヘッダ: HTTP/1.1 で必須、形式検証（reg‑name/IPv4/[IPv6][:port]）
- サイズ上限: 行長/総ヘッダサイズの既定あり（カスタマイズ可）

Observability & Tracing
- Logger: `Logf(level, format, args...)` を実装し差し替え
- Meter: `Counter`/`Histogram` を実装し差し替え
- RequestID/CorrelationID のヘルパ（Context 連携）
- Trace: `Trace` と `WithTrace`/`TraceFrom`、`TraceStateBuilder` による tracestate 構築

Quick start
- Server
  ```go
  s := &httpx.Server{Addr: ":8080"}
  s.Handler = httpx.HandlerFunc(func(w httpx.ResponseWriter, r *httpx.Request) {
      w.Header().Set("Content-Type", "text/plain; charset=utf-8")
      w.WriteHeader(200)
      w.Write([]byte("hello"))
  })
  if err := s.ListenAndServe(); err != nil { log.Fatal(err) }
  ```

- Client
  ```go
  c := &httpx.Client{}
  res, err := c.Get("http://127.0.0.1:8080/")
  if err != nil { log.Fatal(err) }
  defer res.Body.Close()
  b, _ := io.ReadAll(res.Body)
  fmt.Println(res.StatusCode, string(b))
  ```

Configuration (Server)
- `Addr`/`Handler`
- Timeouts: `ReadTimeout`/`ReadHeaderTimeout`/`WriteTimeout`/`IdleTimeout`
- Limits: `MaxHeaderBytes`（行長）, `MaxTotalHeaderBytes`（総ヘッダ）, `MaxBodyBytes`
- 100‑continue: `ExpectContinueTimeout`
- Compression: `EnableGzip`, `DecompressRequest`
- TLS: `TLSConfig`, `ListenAndServeTLS(certFile, keyFile)`, `ServeTLS(listener, cfg)`

Configuration (Client/Transport)
- `Client.Timeout`（全体の締切）
- Redirects: `MaxRedirects`, `RedirectPolicy`
- Transport（BasicTransport; `NewBasicTransport()` 推奨）
  - Pool: `MaxConnsPerHost`, `IdleConnTimeout`, `CloseIdleConnections()`, `Close()`
  - TLS: `TLSConfig`（SNI/ALPNを自動設定）
  - Proxy: `Proxy` 関数 or `ProxyFromEnvironment`

Library usage notes
- `Client` は `Transport` が nil の場合に毎回 `NewBasicTransport()` を生成し、
  ライブラリ間でプールが共有されることを避けます。
- リソース解放は `Client.CloseIdleConnections()` もしくは `(*BasicTransport).Close()` を呼んで明示。
- サーバは `Shutdown(ctx)` で停止（Graceful）。
- トレース/相関: `WithTrace`/`TraceFrom`、`WithRequestID`/`WithCorrelationID` を活用。

Docs
- Design/architecture: `docs/architecture/folder-architecture.md`
- HTTP requirements: `docs/http/requirements.md`

Module
- go.mod: `module dqx0.com/go/protcols`

Snippets
- Enable gzip and Expect: 100‑continue policy
  ```go
  s := &httpx.Server{
      Addr: ":8080",
      EnableGzip: true,
      ExpectContinueTimeout: 5 * time.Second,
  }
  s.Handler = httpx.HandlerFunc(func(w httpx.ResponseWriter, r *httpx.Request) {
      // Read body (triggers 100-continue if requested by client)
      io.Copy(io.Discard, r.Body)
      w.Header().Set("Content-Type", "text/plain")
      w.WriteHeader(200)
      w.Write([]byte("ok"))
  })
  log.Fatal(s.ListenAndServe())
  ```

- Graceful shutdown
  ```go
  s := &httpx.Server{Addr: ":8080", Handler: h}
  go func(){ _ = s.ListenAndServe() }()
  // ...
  ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer cancel()
  if err := s.Shutdown(ctx); err != nil { log.Println("shutdown:", err) }
  ```

- Streaming response (chunked) with Flusher
  ```go
  s := &httpx.Server{Addr: ":8080"}
  s.Handler = httpx.HandlerFunc(func(w httpx.ResponseWriter, r *httpx.Request) {
      w.Header().Set("Content-Type", "text/plain")
      w.WriteHeader(200)
      for i := 0; i < 5; i++ {
          w.Write([]byte(fmt.Sprintf("line %d\n", i)))
          if f, ok := w.(httpx.Flusher); ok { _ = f.Flush() }
          time.Sleep(500 * time.Millisecond)
      }
  })
  ```

- Client with proxy from environment (HTTP_PROXY/NO_PROXY)
  ```go
  os.Setenv("HTTP_PROXY", "http://127.0.0.1:8080")
  os.Setenv("NO_PROXY", "localhost,10.0.0.0/8,*.example.com")
  c := &httpx.Client{}
  res, err := c.Get("http://example.com/")
  // ProxyFromEnvironment is applied by BasicTransport when Proxy is nil.
  ```

- Client with explicit Proxy and TLS config
  ```go
  bt := httpx.NewBasicTransport()
  bt.Proxy = func(r *httpx.Request) (*url.URL, error) {
      return url.Parse("http://user:pass@proxy.local:3128")
  }
  bt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
  c := &httpx.Client{Transport: bt}
  res, err := c.Get("https://example.com/")
  ```

- Tracing propagation (Traceparent/Tracestate)
  ```go
  // Inbound handler reads trace, outbound request reuses it
  s := &httpx.Server{}
  s.Handler = httpx.HandlerFunc(func(w httpx.ResponseWriter, r *httpx.Request) {
      tr, _ := httpx.TraceFrom(r.Context())
      // Build outbound request and propagate TraceID
      u, _ := url.Parse("http://downstream.local/")
      req := &httpx.Request{Method: "GET", URL: u, Header: httpx.Header{}}
      req.TraceID = tr.TraceID
      // Optional tracestate composition
      b := httpx.NewTraceStateBuilder(r.TraceState)
      b.Set("vendor", "abc")
      req.TraceState = b.String()
      res, err := (&httpx.Client{}).Do(req)
      if err == nil { res.Body.Close() }
      w.WriteHeader(204)
  })
  ```

- Client with Expect: 100‑continue (server supports delayed 100)
  ```go
  req := &httpx.Request{Method: "POST", URL: u, Header: httpx.Header{}}
  req.Header.Set("Content-Type", "text/plain")
  req.Header.Set("Expect", "100-continue")
  req.Body = io.NopCloser(strings.NewReader(largePayload))
  req.ContentLength = int64(len(largePayload))
  res, err := (&httpx.Client{}).Do(req)
  ```

- Custom redirect policy (block cross‑host)
  ```go
  c := &httpx.Client{MaxRedirects: 5}
  c.RedirectPolicy = func(prev *httpx.Request, res *httpx.Response, n int) (*httpx.Request, error) {
      loc := res.Header.Get("Location")
      if loc == "" { return nil, nil }
      u, _ := url.Parse(loc)
      if !u.IsAbs() && prev.URL != nil { u = prev.URL.ResolveReference(u) }
      if u.Host != prev.URL.Host { return nil, fmt.Errorf("blocked redirect to %s", u.Host) }
      return &httpx.Request{Method: prev.Method, URL: u, Header: httpx.Header{}}, nil
  }
  ```

- Client resource lifecycle
  ```go
  c := &httpx.Client{}
  // ... perform requests ...
  c.CloseIdleConnections() // release pooled connections when done
  ```
