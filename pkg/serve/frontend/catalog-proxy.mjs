// catalog-proxy.mjs — serves the catalogue over tailscale.
//
// esbuild's dev server answers 403 to any request whose Host header it does not
// recognise, which is every request arriving through `tailscale serve`
// (Host: dev.taild072ac.ts.net:7301). It is a deliberate anti-DNS-rebinding
// check in esbuild and it has no allow-list option, so the fix is not a flag:
// something has to rewrite the header before esbuild sees it.
//
// This is that something, and nothing else. It does not cache, transform or
// interpret: whatever the catalogue serves is what comes out the other end, so
// what the owner looks at over tailscale is byte-identical to 127.0.0.1:7300.
//
//   node catalog-proxy.mjs          # 7301 -> 7300
//   PORT=9000 TARGET=7300 node catalog-proxy.mjs
import http from "node:http";

const port = Number(process.env.PORT || 7301);
const target = Number(process.env.TARGET || 7300);

http
  .createServer((req, res) => {
    const upstream = http.request(
      {
        host: "127.0.0.1",
        port: target,
        path: req.url,
        method: req.method,
        // The whole point: esbuild only answers to a host it trusts.
        headers: { ...req.headers, host: `127.0.0.1:${target}` },
      },
      (up) => {
        res.writeHead(up.statusCode || 502, up.headers);
        up.pipe(res);
      },
    );
    upstream.on("error", (err) => {
      // The catalogue dying is the common case (it is an esbuild watch that
      // exits on its own), and a hung socket looks like a frontend bug. Say so.
      res.writeHead(502, { "content-type": "text/plain" });
      res.end(`catalogue not reachable on 127.0.0.1:${target}\n${err.message}\n`);
    });
    req.pipe(upstream);
  })
  .listen(port, "127.0.0.1", () => {
    console.log(`catalog proxy: 127.0.0.1:${port} -> 127.0.0.1:${target}`);
  });
