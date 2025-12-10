# ns-reloader

ns-reloader is a generic Kubernetes namespace watcher that automatically restarts a wrapped process when namespaces matching a specified label selector change. It dynamically updates an environment variable with the list of watched namespaces and gracefully restarts the wrapped process to reflect those changes.

## Configuration

ns-reloader is configured via environment variables:

| Environment Variable          | Required | Description                                                          | Example           |
|-------------------------------|----------|----------------------------------------------------------------------|-------------------|
| `RELOADER_NAMESPACE_SELECTOR` | Yes      | Kubernetes label selector for namespaces to watch                    | `team=platform`   |
| `RELOADER_TARGET_ENV_KEY`     | Yes      | Name of the environment variable to populate with the namespace list | `WATCH_NAMESPACE` |

## Usage

ns-reloader wraps any process and manages its lifecycle based on namespace changes. Use the `--` separator to distinguish reloader flags from the wrapped command. 

```
Usage: reloader [flags] -- <command> [args...]
```

### Example: Wrapping KEDA Operator

This example shows how to wrap KEDA operator to dynamically manage autoscaling across namespaces labeled with `keda.sh/enabled=true`.

```Dockerfile
FROM ghcr.io/pfnet/ns-reloader AS base
FROM ghcr.io/kedacore/keda:2.17.2
COPY --from=base /reloader /reloader
ENTRYPOINT ["/reloader"]
```

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: keda-operator-with-reloader
  namespace: keda
spec:
  replicas: 1
  selector:
    matchLabels:
      app: keda-operator
  template:
    metadata:
      labels:
        app: keda-operator
    spec:
      serviceAccountName: keda-operator
      containers:
        - name: keda-operator
          image: <wapped_keda_operator_image>
          env:
            - name: RELOADER_NAMESPACE_SELECTOR
              value: "keda.sh/enabled=true"
            - name: RELOADER_TARGET_ENV_KEY
              value: "WATCH_NAMESPACE"
          command: ["/reloader", "--"]
          args:
            - "/keda"
            - "--leader-elect"
            - "--zap-log-level=info"
            - "--zap-encoder=console"
```

## License

[Apache License 2.0](LICENSE)
