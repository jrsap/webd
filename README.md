# A simple but flexible webserver.

## Sample config:

```
{
  "443": {
    "tls": {
      "certificate": "/etc/webd/server.crt",
      "privateKey": "/etc/webd/server.key"
    },
    "routes": {
      "/": "/var/www",
      "/sub_path": "http://localhost:8080",
      "/sub_path_2": "tafels:3000"
    }
  },
  "containers": {
    "tafels": {
      "image": "quay.io/wvdschel/tafels:latest",
      "idleTimeout": "5m",
      "autostart": false,
      "volumes": [
        { "host": "/srv/tafels/data", "container": "/data" },
        { "host": "/etc/tafels.conf", "container": "/etc/tafels.conf", "readOnly": true }
      ]
    }
  }
}
```

All config keys are canonicalized into lower case strings with underscores removed, so you never have to worry about whether a setting
is supposed to be named `private_key` or `privateKey` - both are equivalent to webd. Documentation and examples use camelCase.

If two keys in the same object canonicalize to the same setting (`{ "privateKey": "a", "private_key": "b" }`), webd refuses to start.
If the values are identical it only logs a warning and continues.

## Route targets

A route value is one string: `/…` serves that directory statically (Content-Type inferred from the extension, falling back to magic-byte
sniffing), `http://…` or `https://…` proxies to that URL, and `name:port`
routes to port `port` of the container named `name`, which must be defined in the top-level `containers` block (the only reserved
key besides port numbers). Anything else is a startup error. Container names may contain letters, digits, `-` and `_`.

## Container volumes

A container's optional `volumes` array binds host directories into the container when it starts, as `podman run -v` flags.
Each entry needs an absolute `host` path and an absolute `container` mount point; `readOnly: true` mounts it read-only (`:ro`).
Each container destination may be mounted only once; anything invalid is a startup error.

## Route matching

Route keys are directories, not string prefixes: `/sub_path` matches `/sub_path` and `/sub_path/x`, but never `/sub_path_b`.
The longest matching route wins; `/` is the fallback.

## About webd

webd is a tiny webserver written in Go with minimal external dependencies. It compiles into a single static binary.

It can host static content, act as an ingress proxy, or host applications packaged as container images using podman -
rootless and daemonless, started as a subprocess. Runtimes sit behind a small interface, so docker support could follow later.

With `autostart: true` a container is started as soon as its image has been pulled in the background at webd startup.
With `autostart: false` it is started by the first request; concurrent requests wait for that single startup.
Startup is complete when the container accepts connections on its published ports, up to a 30 second timeout.
After `idleTimeout` of no traffic on any route referencing it, the container is terminated.

A container is defined once but can be referenced by many routes, on many listening ports: they all share one process and one
lifecycle. Only the ports referenced by routes are published, bound to 127.0.0.1, so a container's other ports stay unreachable.
