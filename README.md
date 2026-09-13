A simple but flexible webserver.

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

all config keys are canonicallized into lower case strings with underscores removed, so you never have to worry about whether a setting
is supposed to be named `private_key` or `privateKey` - both are equivalent to webd.

## About webd

webd is tiny webserver built in Go with minimal external dependencies. It compiles into a single static binary.

It can host static content, serve as an ingress proxy, or host applications packaged as container images using podman, docker or runc.
