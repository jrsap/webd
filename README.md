# A simple but flexible webserver.

## Sample config:

```
{
  443: {
    "tls": {
      "certificate": "/etc/webd/server.crt",
      "privateKey": "/etc/webd/server.key"
    },
    "routes": {
      "/": {
        "path": "/var/www/",
        "description": "static file hosting - Content-Type are inferred from extension or UNIX `file` command if available"
      },
      "/sub_path": {
        "proxy": "http://localhost:8080",
        "description": "forward request to another port or server, managed outside of webd"
      },
      "/sub_path_2": {
        "container": {
          "image": "quay.io/wvdschel/tafels:latest",
          "idleTimeout": "5m",
          "autostart": false,
          "description": "path backed by a server from a container image, that is terminated after 5 minutes of no traffic, and is only started when a request is sent for the first time. Images are pulled in the background when webd starts."
        }
      }
    }
  }
}
```

All config keys are canonicalized into lower case strings with underscores removed, so you never have to worry about whether a setting
is supposed to be named `private_key` or `privateKey` - both are equivalent to webd. Documentation and examples use camelCase.

If two keys in the same object canonicalize to the same setting (`{ "privateKey": "a", "private_key": "b" }`), webd refuses to start.
If the values are identical it only logs a warning and continues.

## Route matching

Route keys are directories, not string prefixes: `/sub_path` matches `/sub_path` and `/sub_path/x`, but never `/sub_path_b`.
The longest matching route wins; `/` is the fallback.

## About webd

webd is a tiny webserver written in Go with minimal external dependencies. It compiles into a single static binary.

It can host static content, act as an ingress proxy, or host applications packaged as container images using podman -
rootless and daemonless, started as a subprocess. Runtimes sit behind a small interface, so docker support could follow later.

With `autostart: true` a container is started as soon as its image has been pulled in the background at webd startup.
With `autostart: false` it is started by the first request; concurrent requests wait for that single startup.
After `idleTimeout` of no traffic the container is terminated.
